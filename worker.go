package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type worker struct {
	cfg    *Config
	store  *Store
	wa     *WhatsApp
	runner *Runner
	repo   *Repo
}

// run processes messages one at a time: a single Claude session per sender
// cannot be resumed concurrently, and the box is small.
func (w *worker) run(ctx context.Context, queue <-chan inbound) {
	for m := range queue {
		w.handle(ctx, m)
	}
}

func (w *worker) handle(ctx context.Context, m inbound) {
	fresh, err := w.store.MarkProcessed(m.MessageID)
	if err != nil {
		log.Printf("store: %v", err)
	}
	if !fresh {
		log.Printf("duplicate delivery of %s, skipping", m.MessageID)
		return
	}

	if m.Type == "job" {
		w.handleJob(ctx, m)
		return
	}

	// The sender wrote, so the 24-hour window is open: deliver anything a
	// job could not send free-form since their last message.
	w.flushParked(ctx, m.From)

	if err := w.wa.MarkReadTyping(ctx, m.MessageID); err != nil {
		log.Printf("typing indicator: %v", err)
	}

	reply, err := w.process(ctx, m)
	if err != nil {
		log.Printf("process %s: %v", m.MessageID, err)
		reply = "Something went wrong on my side: " + err.Error()
	}
	if err := w.wa.SendText(ctx, m.From, reply); err != nil {
		log.Printf("send reply: %v", err)
	}
}

// handleJob runs a scheduled prompt in a throwaway session and delivers the
// result proactively. Errors are logged, never sent: a failing job at 07:00
// should not wake anyone up with a stack trace.
func (w *worker) handleJob(ctx context.Context, m inbound) {
	res, err := w.runClaude(ctx, m.MessageID, m.Text, "")
	if err != nil {
		log.Printf("job %s: %v", m.Job, err)
		return
	}
	w.commit(ctx, m.MessageID, "job "+m.Job)

	reply := strings.TrimSpace(res.Result)
	if reply == "" || strings.EqualFold(reply, nothingReply) {
		log.Printf("job %s: nothing to report", m.Job)
		return
	}
	w.deliver(ctx, m.From, reply)
}

// deliver sends a proactive message. Outside the 24-hour window Meta only
// accepts a template, so the headline goes out that way and the full text
// waits in the store until the recipient writes back.
func (w *worker) deliver(ctx context.Context, to, text string) {
	err := w.wa.SendText(ctx, to, text)
	if err == nil {
		return
	}
	if !outsideWindow(err) {
		log.Printf("deliver: %v", err)
		return
	}
	if perr := w.store.Park(to, text); perr != nil {
		log.Printf("deliver: park: %v", perr)
	}
	if terr := w.wa.SendTemplate(ctx, to, firstLine(text)); terr != nil {
		log.Printf("deliver: window closed and template failed: %v (message parked)", terr)
		return
	}
	log.Print("deliver: window closed, sent template and parked the full message")
}

func (w *worker) flushParked(ctx context.Context, to string) {
	bodies, err := w.store.TakeParked(to)
	if err != nil {
		log.Printf("parked: %v", err)
	}
	for _, body := range bodies {
		if err := w.wa.SendText(ctx, to, body); err != nil {
			log.Printf("parked: send: %v", err)
		}
	}
}

func (w *worker) process(ctx context.Context, m inbound) (string, error) {
	switch m.Type {
	case "text":
		if cmd := strings.ToLower(strings.TrimSpace(m.Text)); strings.HasPrefix(cmd, "/") {
			if handled, reply, err := w.command(cmd, m); handled {
				return reply, err
			}
		}
	case "image":
	default:
		return "I can only handle text and images for now.", nil
	}

	prompt, err := w.buildPrompt(ctx, m)
	if err != nil {
		return "", err
	}

	sessionID, err := w.store.Session(m.From)
	if err != nil {
		return "", err
	}

	res, err := w.runClaude(ctx, m.MessageID, prompt, sessionID)
	if errors.Is(err, errStaleSession) {
		log.Printf("session %s for %s is gone, starting a new one", sessionID, m.From)
		_ = w.store.ClearSession(m.From)
		sessionID = ""
		res, err = w.runClaude(ctx, m.MessageID, prompt, "")
	}
	if err != nil {
		return "", err
	}

	if res.SessionID != "" && res.SessionID != sessionID {
		if err := w.store.SetSession(m.From, res.SessionID); err != nil {
			log.Printf("store session: %v", err)
		}
	}

	w.commit(ctx, m.MessageID, prompt)
	return res.Result, nil
}

func (w *worker) runClaude(ctx context.Context, id, prompt, sessionID string) (*claudeResult, error) {
	started := time.Now()
	res, err := w.runner.Run(ctx, prompt, sessionID)
	if err != nil {
		return nil, err
	}
	log.Printf("claude: %s turns=%d cost=$%.4f elapsed=%s", id, res.NumTurns, res.Cost, time.Since(started).Round(time.Second))
	return res, nil
}

func (w *worker) commit(ctx context.Context, id, label string) {
	if committed, err := w.repo.CommitAndPush(ctx, label); err != nil {
		log.Printf("git: %v", err)
	} else if committed {
		log.Printf("git: committed vault changes for %s", id)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (w *worker) command(cmd string, m inbound) (handled bool, reply string, err error) {
	switch strings.Fields(cmd)[0] {
	case "/clear", "/new":
		if err := w.store.ClearSession(m.From); err != nil {
			return true, "", err
		}
		return true, "New session started.", nil
	case "/ping":
		return true, "pong", nil
	case "/help":
		return true, "Send text or an image. /clear starts a fresh session. /ping checks I'm alive.", nil
	}
	return false, "", nil
}

// buildPrompt turns the message into what Claude receives. Images are saved
// into the vault's media folder so Claude can Read them by path.
func (w *worker) buildPrompt(ctx context.Context, m inbound) (string, error) {
	if m.Type == "text" {
		return m.Text, nil
	}

	data, mime, err := w.wa.DownloadMedia(ctx, m.Image.MediaID)
	if err != nil {
		return "", err
	}
	if m.Image.MimeType != "" {
		mime = m.Image.MimeType
	}
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}[mime]
	if ext == "" {
		ext = ".bin"
	}
	dir := filepath.Join(w.cfg.VaultDir, w.cfg.MediaDir)
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return "", err
	}
	name := time.Now().UTC().Format("20060102-150405") + "-" + sanitize(m.Image.MediaID) + ext
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o664); err != nil {
		return "", err
	}
	rel := filepath.Join(w.cfg.MediaDir, name)

	var b strings.Builder
	fmt.Fprintf(&b, "I sent you an image over WhatsApp. It is saved in the vault at %s (relative to the vault root). Look at it with the Read tool.", rel)
	if c := strings.TrimSpace(m.Image.Caption); c != "" {
		fmt.Fprintf(&b, "\n\nMy message with it: %s", c)
	} else {
		b.WriteString("\n\nI did not add a caption. Describe what you see and ask what I want done with it, unless it is obvious.")
	}
	return b.String(), nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() > 24 {
		return b.String()[:24]
	}
	return b.String()
}
