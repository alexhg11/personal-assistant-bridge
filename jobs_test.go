package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newJobsTest(t *testing.T) (*Config, chan inbound, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "morning-brief.md"), []byte("List what is due.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("America/Monterrey")
	cfg := &Config{JobsDir: dir, JobTZ: loc, AllowedWAID: "5218100000000"}
	queue := make(chan inbound, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /jobs/{name}", jobsHandler(cfg, queue))
	return cfg, queue, mux
}

func postJob(h http.Handler, name, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/jobs/"+name, nil)
	req.RemoteAddr = remote
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)
	return rw
}

func TestJobsHandlerQueuesPrompt(t *testing.T) {
	cfg, queue, h := newJobsTest(t)
	rw := postJob(h, "morning-brief", "127.0.0.1:4321")
	if rw.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rw.Code, rw.Body.String())
	}
	if len(queue) != 1 {
		t.Fatalf("want 1 queued, got %d", len(queue))
	}
	m := <-queue
	if m.Type != "job" || m.Job != "morning-brief" || m.From != cfg.AllowedWAID {
		t.Errorf("queued item wrong: %+v", m)
	}
	if !strings.HasPrefix(m.Text, "Today is ") || !strings.Contains(m.Text, "List what is due.") || !strings.Contains(m.Text, nothingReply) {
		t.Errorf("prompt not assembled: %q", m.Text)
	}
	if !strings.HasPrefix(m.MessageID, "job:morning-brief:") {
		t.Errorf("message id: %q", m.MessageID)
	}
}

func TestJobsHandlerRejects(t *testing.T) {
	_, queue, h := newJobsTest(t)
	cases := []struct {
		name, remote string
		want         int
	}{
		{"morning-brief", "10.0.0.5:1", http.StatusForbidden},
		{"nope", "127.0.0.1:1", http.StatusNotFound},
		{"Bad_Name", "127.0.0.1:1", http.StatusBadRequest},
		{"a.b", "127.0.0.1:1", http.StatusBadRequest},
	}
	for _, c := range cases {
		if rw := postJob(h, c.name, c.remote); rw.Code != c.want {
			t.Errorf("%s from %s: want %d, got %d", c.name, c.remote, c.want, rw.Code)
		}
	}
	if len(queue) != 0 {
		t.Fatalf("rejected requests queued %d items", len(queue))
	}
	// Path traversal is stopped by the mux (it redirects cleaned paths), and
	// by the name pattern as a second line.
	for _, bad := range []string{"..", "../x", "a/b", "-lead", "", strings.Repeat("a", 65)} {
		if jobName.MatchString(bad) {
			t.Errorf("job name %q accepted", bad)
		}
	}
}

func TestJobsHandlerQueueFull(t *testing.T) {
	_, queue, h := newJobsTest(t)
	queue <- inbound{}
	queue <- inbound{}
	if rw := postJob(h, "morning-brief", "127.0.0.1:1"); rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("full queue: status %d", rw.Code)
	}
}

func TestJobPromptDate(t *testing.T) {
	loc, _ := time.LoadLocation("America/Monterrey")
	now := time.Date(2026, 9, 30, 7, 0, 0, 0, loc)
	p := jobPrompt("  body  ", now)
	if !strings.HasPrefix(p, "Today is Wednesday, 2026-09-30 (America/Monterrey).") {
		t.Errorf("date line: %q", p)
	}
	if !strings.Contains(p, "\n\nbody\n\n") {
		t.Errorf("body not trimmed and framed: %q", p)
	}
}

func TestParkedMessages(t *testing.T) {
	store, err := openStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got, _ := store.TakeParked("a"); len(got) != 0 {
		t.Fatalf("unexpected parked: %v", got)
	}
	_ = store.Park("a", "first")
	_ = store.Park("b", "other")
	_ = store.Park("a", "second")
	got, err := store.TakeParked("a")
	if err != nil || len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("take: %v err=%v", got, err)
	}
	if got, _ := store.TakeParked("a"); len(got) != 0 {
		t.Fatal("parked messages not removed")
	}
	if got, _ := store.TakeParked("b"); len(got) != 1 {
		t.Fatal("other recipient's message lost")
	}
}

func TestGraphErrorAndWindow(t *testing.T) {
	body := []byte(`{"error":{"message":"Re-engagement message","type":"OAuthException","code":131047}}`)
	err := parseGraphError(400, body)
	if !outsideWindow(err) {
		t.Fatalf("131047 not recognised: %v", err)
	}
	if !strings.Contains(err.Error(), "131047") || !strings.Contains(err.Error(), "Re-engagement") {
		t.Errorf("error text: %v", err)
	}
	if outsideWindow(parseGraphError(500, []byte("<html>oops</html>"))) {
		t.Error("non-JSON body treated as window error")
	}
	if outsideWindow(parseGraphError(400, []byte(`{"error":{"code":100,"message":"bad"}}`))) {
		t.Error("other code treated as window error")
	}
}

func TestTemplateParam(t *testing.T) {
	if got := templateParam("  a\n\nb   c\t d "); got != "a b c d" {
		t.Errorf("whitespace: %q", got)
	}
	long := strings.Repeat("x", templateParamLimit+50)
	if got := templateParam(long); len([]rune(got)) != templateParamLimit || !strings.HasSuffix(got, "…") {
		t.Errorf("cap: len %d, %q", len([]rune(got)), got[len(got)-3:])
	}
	if got := templateParam("   "); got != "(empty)" {
		t.Errorf("empty: %q", got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("\n*Brief*\nmore\n"); got != "*Brief*" {
		t.Errorf("%q", got)
	}
	if got := firstLine("single"); got != "single" {
		t.Errorf("%q", got)
	}
}
