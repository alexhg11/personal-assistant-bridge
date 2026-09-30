package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WhatsApp text bodies are capped at 4096 characters; keep headroom.
const chunkLimit = 3900

type WhatsApp struct {
	cfg    *Config
	client *http.Client
}

func newWhatsApp(cfg *Config) *WhatsApp {
	return &WhatsApp{cfg: cfg, client: &http.Client{Timeout: 60 * time.Second}}
}

func (w *WhatsApp) messagesURL() string {
	return fmt.Sprintf("https://graph.facebook.com/%s/%s/messages", w.cfg.GraphVersion, w.cfg.WAPhoneNumberID)
}

// SendText delivers text to a recipient, split into WhatsApp-sized chunks.
func (w *WhatsApp) SendText(ctx context.Context, to, text string) error {
	for _, part := range chunk(text, chunkLimit) {
		payload := map[string]any{
			"messaging_product": "whatsapp",
			"recipient_type":    "individual",
			"to":                to,
			"type":              "text",
			"text":              map[string]any{"preview_url": false, "body": part},
		}
		if err := w.post(ctx, w.messagesURL(), payload); err != nil {
			return err
		}
	}
	return nil
}

// MarkReadTyping sends a read receipt with a typing indicator so the phone
// shows activity while Claude works. Failures are not fatal.
func (w *WhatsApp) MarkReadTyping(ctx context.Context, messageID string) error {
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"status":            "read",
		"message_id":        messageID,
		"typing_indicator":  map[string]any{"type": "text"},
	}
	return w.post(ctx, w.messagesURL(), payload)
}

// DownloadMedia resolves a media id to bytes.
func (w *WhatsApp) DownloadMedia(ctx context.Context, mediaID string) ([]byte, string, error) {
	metaURL := fmt.Sprintf("https://graph.facebook.com/%s/%s", w.cfg.GraphVersion, mediaID)
	var meta struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
	}
	if err := w.getJSON(ctx, metaURL, &meta); err != nil {
		return nil, "", fmt.Errorf("media lookup: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.URL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.WAToken)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("media download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("media download: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		return nil, "", err
	}
	return data, meta.MimeType, nil
}

func (w *WhatsApp) post(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.WAToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("graph api: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (w *WhatsApp) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.WAToken)
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("graph api: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// chunk splits text into pieces of at most limit runes, preferring paragraph,
// then line, then word boundaries.
func chunk(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{"(empty reply)"}
	}
	var out []string
	runes := []rune(text)
	for len(runes) > limit {
		cut := -1
		window := string(runes[:limit])
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(window, sep); i > limit/4 {
				cut = len([]rune(window[:i]))
				break
			}
		}
		if cut <= 0 {
			cut = limit
		}
		out = append(out, strings.TrimSpace(string(runes[:cut])))
		runes = []rune(strings.TrimLeft(string(runes[cut:]), " \n"))
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}
