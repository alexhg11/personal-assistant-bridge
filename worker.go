package main

import (
	"context"
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

	started := time.Now()
	res, err := w.runner.Run(ctx, prompt, sessionID)
	if err != nil {
		return "", err
	}
	log.Printf("claude: %s turns=%d cost=$%.4f elapsed=%s", m.MessageID, res.NumTurns, res.Cost, time.Since(started).Round(time.Second))

	if res.SessionID != "" && res.SessionID != sessionID {
		if err := w.store.SetSession(m.From, res.SessionID); err != nil {
			log.Printf("store session: %v", err)
		}
	}

	if committed, err := w.repo.CommitAndPush(ctx, prompt); err != nil {
		log.Printf("git: %v", err)
	} else if committed {
		log.Printf("git: committed vault changes for %s", m.MessageID)
	}

	return res.Result, nil
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
