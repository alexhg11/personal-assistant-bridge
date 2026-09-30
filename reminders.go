package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Ad-hoc reminders. Claude turns "remind me Friday at 9 to call Ricardo"
// into a line of the form
//
//	REMIND: 2026-10-02 09:00 | Call Ricardo
//	REMIND: in 20m | Check the oven
//
// anywhere in its reply (the contract lives in the server-side CLAUDE.md).
// The bridge strips those lines, stores the reminders, and appends its own
// confirmation, so a confirmation on the phone means the row exists. A
// scheduler goroutine fires them through the same delivery path as jobs.

type reminder struct {
	ID        int64
	Recipient string
	DueAt     time.Time
	Body      string
}

var remindLine = regexp.MustCompile(`(?m)^\s*REMIND:\s*(.+?)\s*\|\s*(.+?)\s*$`)

var relativeSpec = regexp.MustCompile(`^in\s+(\d+)\s*(m|min|mins|minutes?|h|hr|hrs|hours?|d|days?)$`)

// extractReminders pulls REMIND lines out of a reply. It returns the reply
// with those lines removed, the reminders that parsed, and one message per
// line that did not, phrased for the phone.
func extractReminders(reply string, now time.Time, loc *time.Location) (string, []reminder, []string) {
	var out []reminder
	var problems []string
	for _, m := range remindLine.FindAllStringSubmatch(reply, -1) {
		when, body := m[1], m[2]
		due, err := parseWhen(when, now, loc)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("Could not set a reminder for %q: %v", when, err))
		case !due.After(now):
			problems = append(problems, fmt.Sprintf("Reminder time %s is already past; not set.", due.In(loc).Format("Mon Jan 2 15:04")))
		default:
			out = append(out, reminder{DueAt: due, Body: body})
		}
	}
	clean := remindLine.ReplaceAllString(reply, "")
	clean = regexp.MustCompile(`\n{3,}`).ReplaceAllString(clean, "\n\n")
	return strings.TrimSpace(clean), out, problems
}

// parseWhen accepts "YYYY-MM-DD HH:MM", "YYYY-MM-DDTHH:MM" (in loc) or a
// relative "in N m|h|d".
func parseWhen(s string, now time.Time, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if m := relativeSpec.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n <= 0 {
			return time.Time{}, fmt.Errorf("zero duration")
		}
		switch m[2][0] {
		case 'm':
			return now.Add(time.Duration(n) * time.Minute), nil
		case 'h':
			return now.Add(time.Duration(n) * time.Hour), nil
		default:
			return now.AddDate(0, 0, n), nil
		}
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02t15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			if layout == "2006-01-02" {
				t = t.Add(9 * time.Hour) // date only: 09:00 local
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD HH:MM or \"in 20m\"")
}

func formatDue(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("Mon Jan 2, 15:04")
}

// saveReminders stores parsed reminders and returns the confirmation lines
// to append to the reply.
func (w *worker) saveReminders(recipient string, rs []reminder) []string {
	var lines []string
	for _, r := range rs {
		id, err := w.store.AddReminder(recipient, r.DueAt, r.Body)
		if err != nil {
			log.Printf("reminder: store: %v", err)
			lines = append(lines, "Could not save the reminder: "+err.Error())
			continue
		}
		log.Printf("reminder: #%d set for %s", id, r.DueAt.UTC().Format(time.RFC3339))
		lines = append(lines, fmt.Sprintf("⏰ Reminder #%d set for %s: %s", id, formatDue(r.DueAt, w.cfg.JobTZ), r.Body))
	}
	return lines
}

func (w *worker) listReminders(recipient string) (string, error) {
	rs, err := w.store.PendingReminders(recipient)
	if err != nil {
		return "", err
	}
	if len(rs) == 0 {
		return "No pending reminders.", nil
	}
	var b strings.Builder
	b.WriteString("⏰ Pending reminders\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "• #%d — %s — %s\n", r.ID, formatDue(r.DueAt, w.cfg.JobTZ), r.Body)
	}
	b.WriteString("\n/cancel <n> removes one.")
	return b.String(), nil
}

func (w *worker) cancelReminder(recipient, arg string) (string, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(arg), "#"), 10, 64)
	if err != nil {
		return "Usage: /cancel <reminder number> — see /reminders.", nil
	}
	ok, err := w.store.CancelReminder(recipient, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("No pending reminder #%d.", id), nil
	}
	return fmt.Sprintf("Reminder #%d cancelled.", id), nil
}

// runReminders fires due reminders until ctx is done. It shares the store
// and WhatsApp client with the worker; both are safe for concurrent use.
func (w *worker) runReminders(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.fireDue(ctx, time.Now())
		}
	}
}

func (w *worker) fireDue(ctx context.Context, now time.Time) {
	due, err := w.store.DueReminders(now)
	if err != nil {
		log.Printf("reminder: query: %v", err)
		return
	}
	for _, r := range due {
		// Mark first so a delivery failure cannot re-fire it every tick.
		if err := w.store.MarkFired(r.ID, now); err != nil {
			log.Printf("reminder: mark #%d: %v", r.ID, err)
			continue
		}
		log.Printf("reminder: firing #%d", r.ID)
		w.deliver(ctx, r.Recipient, "⏰ "+r.Body)
	}
}
