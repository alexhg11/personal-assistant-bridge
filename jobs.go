package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Scheduled jobs are prompt files in JobsDir, one per job, run through the
// same worker queue as WhatsApp messages so a job never overlaps a chat turn.
// They are triggered by POST /jobs/<name>, which only answers loopback: nginx
// does not proxy /jobs, and systemd timers on the box curl it directly.
//
// A job runs in a fresh Claude session that is never stored, so the brief
// does not pollute the chat session and the chat does not leak into the
// brief. The result goes to the allow-listed sender; a reply that is exactly
// "NOTHING" is dropped, so a job can stay quiet when there is nothing to say.

var jobName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// nothingReply is what a job prompt tells Claude to answer when it has
// nothing worth sending.
const nothingReply = "NOTHING"

func jobsHandler(cfg *Config, queue chan<- inbound) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !fromLoopback(r.RemoteAddr) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		name := r.PathValue("name")
		if !jobName.MatchString(name) {
			http.Error(rw, "bad job name", http.StatusBadRequest)
			return
		}
		prompt, err := os.ReadFile(filepath.Join(cfg.JobsDir, name+".md"))
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(rw, "no such job", http.StatusNotFound)
				return
			}
			log.Printf("jobs: read %s: %v", name, err)
			http.Error(rw, "cannot read job", http.StatusInternalServerError)
			return
		}
		now := time.Now().In(cfg.JobTZ)
		m := inbound{
			MessageID: fmt.Sprintf("job:%s:%s", name, now.UTC().Format("20060102T150405Z")),
			From:      cfg.AllowedWAID,
			Type:      "job",
			Job:       name,
			Text:      jobPrompt(string(prompt), now),
		}
		select {
		case queue <- m:
			log.Printf("jobs: queued %s", name)
			rw.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintf(rw, "queued %s\n", name)
		default:
			log.Printf("jobs: queue full, dropping %s", name)
			http.Error(rw, "queue full", http.StatusServiceUnavailable)
		}
	}
}

// jobPrompt prefixes the prompt file with the local date so Claude does not
// have to guess it, and appends the quiet-reply contract.
func jobPrompt(body string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Today is %s, %s (%s). This is a scheduled job, not a chat message: nobody will read a question, so do not ask any.\n\n",
		now.Weekday(), now.Format("2006-01-02"), now.Location())
	b.WriteString(strings.TrimSpace(body))
	fmt.Fprintf(&b, "\n\nIf there is nothing worth reporting, reply with exactly the word %s and nothing else.", nothingReply)
	return b.String()
}

func fromLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
