package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidSignature(t *testing.T) {
	body := []byte(`{"object":"whatsapp_business_account"}`)
	if !validSignature("s3cret", sign("s3cret", body), body) {
		t.Fatal("valid signature rejected")
	}
	if validSignature("s3cret", sign("other", body), body) {
		t.Fatal("wrong secret accepted")
	}
	if validSignature("s3cret", "", body) {
		t.Fatal("missing header accepted")
	}
	if validSignature("s3cret", "sha256=zz", body) {
		t.Fatal("garbage hex accepted")
	}
}

const samplePayload = `{
 "object":"whatsapp_business_account",
 "entry":[{"id":"4387104754873181","changes":[{"field":"messages","value":{
   "messaging_product":"whatsapp",
   "metadata":{"display_phone_number":"15553504885","phone_number_id":"1310849255447468"},
   "contacts":[{"profile":{"name":"Alex"},"wa_id":"5218100000000","user_id":"bsuid-123"}],
   "messages":[
     {"from":"5218100000000","id":"wamid.1","timestamp":"1","type":"text","text":{"body":"hola"}},
     {"from":"5218100000000","id":"wamid.2","timestamp":"2","type":"image","image":{"id":"m1","mime_type":"image/jpeg","caption":"cap"}},
     {"from":"5218100000000","id":"wamid.3","timestamp":"3","type":"audio","audio":{"id":"a1"}}
   ]}}]}]}`

func TestParsePayload(t *testing.T) {
	msgs, err := parsePayload([]byte(samplePayload))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	if msgs[0].Type != "text" || msgs[0].Text != "hola" || msgs[0].UserID != "bsuid-123" {
		t.Errorf("text message parsed wrong: %+v", msgs[0])
	}
	if msgs[1].Type != "image" || msgs[1].Image == nil || msgs[1].Image.MediaID != "m1" || msgs[1].Image.Caption != "cap" {
		t.Errorf("image message parsed wrong: %+v", msgs[1])
	}
	if msgs[2].Type != "other" {
		t.Errorf("audio should be 'other', got %q", msgs[2].Type)
	}
}

func TestParsePayloadIgnoresStatuses(t *testing.T) {
	body := `{"entry":[{"changes":[{"field":"messages","value":{"statuses":[{"id":"x","status":"delivered"}]}}]}]}`
	msgs, err := parsePayload([]byte(body))
	if err != nil || len(msgs) != 0 {
		t.Fatalf("want no messages, got %v err=%v", msgs, err)
	}
}

func TestAllowed(t *testing.T) {
	cfg := &Config{AllowedWAID: "5218100000000"}
	if !allowed(cfg, inbound{From: "5218100000000"}) {
		t.Error("allowed sender rejected")
	}
	if allowed(cfg, inbound{From: "1555"}) {
		t.Error("stranger accepted")
	}
	cfg.AllowedUserID = "bsuid-123"
	if allowed(cfg, inbound{From: "5218100000000", UserID: ""}) {
		t.Error("missing user id accepted when one is required")
	}
	if !allowed(cfg, inbound{From: "5218100000000", UserID: "bsuid-123"}) {
		t.Error("matching user id rejected")
	}
}

func TestWebhookHandler(t *testing.T) {
	cfg := &Config{WAAppSecret: "s3cret", AllowedWAID: "5218100000000"}
	store, err := openStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	queue := make(chan inbound, 10)
	h := webhookHandler(cfg, store, queue)

	body := []byte(samplePayload)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", sign("s3cret", body))
	rw := httptest.NewRecorder()
	h(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("status %d", rw.Code)
	}
	if len(queue) != 3 {
		t.Fatalf("want 3 queued, got %d", len(queue))
	}

	req = httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", sign("wrong", body))
	rw = httptest.NewRecorder()
	h(rw, req)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("bad signature: status %d", rw.Code)
	}
}

func TestVerifyHandler(t *testing.T) {
	cfg := &Config{WAVerifyToken: "vt"}
	h := verifyHandler(cfg)
	req := httptest.NewRequest(http.MethodGet, "/webhook?hub.mode=subscribe&hub.verify_token=vt&hub.challenge=abc", nil)
	rw := httptest.NewRecorder()
	h(rw, req)
	if rw.Code != 200 || rw.Body.String() != "abc" {
		t.Fatalf("handshake: %d %q", rw.Code, rw.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/webhook?hub.mode=subscribe&hub.verify_token=nope&hub.challenge=abc", nil)
	rw = httptest.NewRecorder()
	h(rw, req)
	if rw.Code != 403 {
		t.Fatalf("bad token: %d", rw.Code)
	}
}

func TestChunk(t *testing.T) {
	if got := chunk("short", 100); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short: %v", got)
	}
	para := strings.Repeat("word ", 30) // 150 chars
	text := para + "\n\n" + para + "\n\n" + para
	parts := chunk(text, 200)
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d: %q", len(parts), parts)
	}
	for _, p := range parts {
		if len([]rune(p)) > 200 {
			t.Errorf("part too long: %d", len([]rune(p)))
		}
	}
	long := strings.Repeat("x", 500)
	parts = chunk(long, 200)
	if len(parts) != 3 {
		t.Fatalf("no-boundary split: want 3, got %d", len(parts))
	}
	if strings.Join(parts, "") != long {
		t.Error("no-boundary split lost characters")
	}
}

func TestStoreSessionsAndDedup(t *testing.T) {
	store, err := openStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if fresh, _ := store.MarkProcessed("m1"); !fresh {
		t.Fatal("first delivery not fresh")
	}
	if fresh, _ := store.MarkProcessed("m1"); fresh {
		t.Fatal("redelivery treated as fresh")
	}
	if id, _ := store.Session("a"); id != "" {
		t.Fatal("unexpected session")
	}
	_ = store.SetSession("a", "s1")
	_ = store.SetSession("a", "s2")
	if id, _ := store.Session("a"); id != "s2" {
		t.Fatalf("session upsert: %q", id)
	}
	_ = store.ClearSession("a")
	if id, _ := store.Session("a"); id != "" {
		t.Fatal("clear failed")
	}
}
