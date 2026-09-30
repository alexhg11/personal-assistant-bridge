package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
)

const maxBody = 1 << 20 // 1 MiB; webhook payloads are small

// inbound is one user message pulled out of a webhook payload.
type inbound struct {
	MessageID string
	From      string // wa_id
	UserID    string // business-scoped user id, may be empty
	Type      string // text | image | other
	Text      string
	Image     *inboundImage
}

type inboundImage struct {
	MediaID  string
	MimeType string
	Caption  string
}

// verifyHandler answers Meta's subscription handshake.
func verifyHandler(cfg *Config) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("hub.mode") != "subscribe" || q.Get("hub.verify_token") != cfg.WAVerifyToken {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte(q.Get("hub.challenge")))
	}
}

// webhookHandler verifies the signature, extracts messages from the sender we
// trust, enqueues them, and returns 200 right away. Meta retries anything
// slow or non-2xx, so the actual work happens in the worker.
func webhookHandler(cfg *Config, store *Store, queue chan<- inbound) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			http.Error(rw, "bad request", http.StatusBadRequest)
			return
		}
		if !validSignature(cfg.WAAppSecret, r.Header.Get("X-Hub-Signature-256"), body) {
			log.Print("webhook: bad signature")
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}

		msgs, err := parsePayload(body)
		if err != nil {
			log.Printf("webhook: parse: %v", err)
			// Still 200: a payload we cannot parse should not be retried.
			rw.WriteHeader(http.StatusOK)
			return
		}

		for _, m := range msgs {
			if !allowed(cfg, m) {
				log.Printf("webhook: dropping message from %s (user_id=%q)", m.From, m.UserID)
				continue
			}
			select {
			case queue <- m:
			default:
				log.Printf("webhook: queue full, dropping %s", m.MessageID)
			}
		}
		rw.WriteHeader(http.StatusOK)
	}
}

func validSignature(appSecret, header string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func allowed(cfg *Config, m inbound) bool {
	if m.From != cfg.AllowedWAID {
		return false
	}
	if cfg.AllowedUserID != "" && m.UserID != cfg.AllowedUserID {
		return false
	}
	return true
}

// Only the fields we use. Everything else in the payload is ignored.
type waPayload struct {
	Entry []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Contacts []map[string]any `json:"contacts"`
				Messages []struct {
					ID   string `json:"id"`
					From string `json:"from"`
					Type string `json:"type"`
					Text *struct {
						Body string `json:"body"`
					} `json:"text"`
					Image *struct {
						ID       string `json:"id"`
						MimeType string `json:"mime_type"`
						Caption  string `json:"caption"`
					} `json:"image"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func parsePayload(body []byte) ([]inbound, error) {
	var p waPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	var out []inbound
	for _, e := range p.Entry {
		for _, c := range e.Changes {
			if c.Field != "messages" {
				continue
			}
			userIDs := contactUserIDs(c.Value.Contacts)
			for _, m := range c.Value.Messages {
				in := inbound{MessageID: m.ID, From: m.From, Type: m.Type, UserID: userIDs[m.From]}
				switch m.Type {
				case "text":
					if m.Text != nil {
						in.Text = m.Text.Body
					}
				case "image":
					if m.Image != nil {
						in.Image = &inboundImage{MediaID: m.Image.ID, MimeType: m.Image.MimeType, Caption: m.Image.Caption}
					}
				default:
					in.Type = "other"
				}
				out = append(out, in)
			}
		}
	}
	return out, nil
}

// contactUserIDs maps wa_id -> business-scoped user id. Meta documents the
// BSUID under the contact object; the key is read loosely so a rename on
// their side degrades to "no user id" rather than a parse failure.
func contactUserIDs(contacts []map[string]any) map[string]string {
	out := map[string]string{}
	for _, c := range contacts {
		waID, _ := c["wa_id"].(string)
		if waID == "" {
			continue
		}
		for _, key := range []string{"user_id", "bsuid"} {
			if v, ok := c[key].(string); ok && v != "" {
				out[waID] = v
				break
			}
		}
	}
	return out
}
