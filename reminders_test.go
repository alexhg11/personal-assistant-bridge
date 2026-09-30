package main

import (
	"strings"
	"testing"
	"time"
)

func mty(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Monterrey")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestExtractReminders(t *testing.T) {
	loc := mty(t)
	now := time.Date(2026, 9, 30, 11, 0, 0, 0, loc)
	reply := "Sure.\n\nREMIND: 2026-10-02 09:00 | Call Ricardo about Infonavit\n\nREMIND: in 20m | Check the oven\nAnything else?"
	clean, rs, problems := extractReminders(reply, now, loc)
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(rs) != 2 {
		t.Fatalf("want 2 reminders, got %d", len(rs))
	}
	want := time.Date(2026, 10, 2, 9, 0, 0, 0, loc)
	if !rs[0].DueAt.Equal(want) || rs[0].Body != "Call Ricardo about Infonavit" {
		t.Errorf("absolute: %+v", rs[0])
	}
	if !rs[1].DueAt.Equal(now.Add(20*time.Minute)) || rs[1].Body != "Check the oven" {
		t.Errorf("relative: %+v", rs[1])
	}
	if strings.Contains(clean, "REMIND") || clean != "Sure.\n\nAnything else?" {
		t.Errorf("clean reply: %q", clean)
	}
}

func TestExtractRemindersProblems(t *testing.T) {
	loc := mty(t)
	now := time.Date(2026, 9, 30, 11, 0, 0, 0, loc)
	reply := "REMIND: yesterday | x\nREMIND: 2026-09-30 10:00 | past\nREMIND: 2026-10-01 | date only"
	clean, rs, problems := extractReminders(reply, now, loc)
	if len(rs) != 1 || rs[0].DueAt.Hour() != 9 || rs[0].DueAt.Day() != 1 {
		t.Errorf("date-only reminder: %+v", rs)
	}
	if len(problems) != 2 {
		t.Errorf("want 2 problems, got %v", problems)
	}
	if clean != "" {
		t.Errorf("clean should be empty, got %q", clean)
	}
	if _, rs, problems := extractReminders("plain reply", now, loc); len(rs) != 0 || len(problems) != 0 {
		t.Error("plain reply produced reminders")
	}
}

func TestParseWhenRelative(t *testing.T) {
	loc := mty(t)
	now := time.Date(2026, 9, 30, 11, 0, 0, 0, loc)
	cases := map[string]time.Duration{
		"in 20m": 20 * time.Minute, "in 2 hours": 2 * time.Hour, "in 1h": time.Hour, "IN 3 days": 72 * time.Hour,
	}
	for in, d := range cases {
		got, err := parseWhen(in, now, loc)
		if err != nil || !got.Equal(now.Add(d)) {
			t.Errorf("%q: got %v err=%v", in, got, err)
		}
	}
	if _, err := parseWhen("in 0m", now, loc); err == nil {
		t.Error("zero duration accepted")
	}
}

func TestReminderStore(t *testing.T) {
	store, err := openStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	id1, _ := store.AddReminder("a", now.Add(time.Hour), "later")
	id2, _ := store.AddReminder("a", now.Add(-time.Minute), "due")
	_, _ = store.AddReminder("b", now.Add(-time.Minute), "someone else")

	pending, _ := store.PendingReminders("a")
	if len(pending) != 2 || pending[0].ID != id2 || pending[1].ID != id1 {
		t.Fatalf("pending order: %+v", pending)
	}
	due, _ := store.DueReminders(now)
	if len(due) != 2 {
		t.Fatalf("want 2 due across recipients, got %d", len(due))
	}
	_ = store.MarkFired(id2, now)
	if due, _ := store.DueReminders(now); len(due) != 1 || due[0].Recipient != "b" {
		t.Errorf("fired reminder still due: %+v", due)
	}
	if ok, _ := store.CancelReminder("b", id1); ok {
		t.Error("cancelled another recipient's reminder")
	}
	if ok, _ := store.CancelReminder("a", id1); !ok {
		t.Error("could not cancel own reminder")
	}
	if ok, _ := store.CancelReminder("a", id1); ok {
		t.Error("cancelled twice")
	}
	if pending, _ := store.PendingReminders("a"); len(pending) != 0 {
		t.Errorf("pending after fire+cancel: %+v", pending)
	}
}

func TestReminderCommands(t *testing.T) {
	store, err := openStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	w := &worker{cfg: &Config{JobTZ: mty(t)}, store: store}
	if reply, _ := w.listReminders("a"); reply != "No pending reminders." {
		t.Errorf("empty list: %q", reply)
	}
	lines := w.saveReminders("a", []reminder{{DueAt: time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC), Body: "Call Ricardo"}})
	if len(lines) != 1 || !strings.Contains(lines[0], "#1") || !strings.Contains(lines[0], "Fri Oct 2, 09:00") {
		t.Errorf("confirmation: %v", lines)
	}
	reply, _ := w.listReminders("a")
	if !strings.Contains(reply, "#1") || !strings.Contains(reply, "Call Ricardo") {
		t.Errorf("list: %q", reply)
	}
	if reply, _ := w.cancelReminder("a", " #1"); reply != "Reminder #1 cancelled." {
		t.Errorf("cancel: %q", reply)
	}
	if reply, _ := w.cancelReminder("a", "x"); !strings.HasPrefix(reply, "Usage") {
		t.Errorf("bad arg: %q", reply)
	}
	if reply, _ := w.cancelReminder("a", "1"); reply != "No pending reminder #1." {
		t.Errorf("cancel twice: %q", reply)
	}
}

func TestApplyRemindersNoChange(t *testing.T) {
	store, err := openStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	w := &worker{cfg: &Config{JobTZ: mty(t)}, store: store}
	if got := w.applyReminders("a", "just a reply"); got != "just a reply" {
		t.Errorf("untouched reply changed: %q", got)
	}
	got := w.applyReminders("a", "Done.\nREMIND: in 5m | ping")
	if !strings.HasPrefix(got, "Done.\n\n⏰ Reminder #1 set for") || strings.Contains(got, "REMIND") {
		t.Errorf("applied: %q", got)
	}
}
