package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store keeps the sender -> Claude session mapping and the set of processed
// message ids (Meta redelivers webhooks it thinks failed).
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	schema := `
CREATE TABLE IF NOT EXISTS sessions (
  sender     TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS processed (
  message_id TEXT PRIMARY KEY,
  seen_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS parked (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  recipient  TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS reminders (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  recipient  TEXT NOT NULL,
  due_at     TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  fired_at   TEXT,
  cancelled  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS reminders_due ON reminders (due_at) WHERE fired_at IS NULL AND cancelled = 0;`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// MarkProcessed returns false if the message was already seen.
func (s *Store) MarkProcessed(messageID string) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO processed (message_id, seen_at) VALUES (?, ?)`,
		messageID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) Session(sender string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT session_id FROM sessions WHERE sender = ?`, sender).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Store) SetSession(sender, sessionID string) error {
	_, err := s.db.Exec(`INSERT INTO sessions (sender, session_id, updated_at) VALUES (?, ?, ?)
ON CONFLICT(sender) DO UPDATE SET session_id = excluded.session_id, updated_at = excluded.updated_at`,
		sender, sessionID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ClearSession(sender string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE sender = ?`, sender)
	return err
}

// Park keeps a proactive message that could not be sent free-form because
// the 24-hour window was closed. It is delivered by TakeParked once the
// recipient writes again.
func (s *Store) Park(recipient, body string) error {
	_, err := s.db.Exec(`INSERT INTO parked (recipient, body, created_at) VALUES (?, ?, ?)`,
		recipient, body, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) AddReminder(recipient string, dueAt time.Time, body string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO reminders (recipient, due_at, body, created_at) VALUES (?, ?, ?, ?)`,
		recipient, dueAt.UTC().Format(time.RFC3339), body, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// PendingReminders lists a recipient's unfired, uncancelled reminders,
// soonest first.
func (s *Store) PendingReminders(recipient string) ([]reminder, error) {
	return s.queryReminders(`SELECT id, recipient, due_at, body FROM reminders
WHERE recipient = ? AND fired_at IS NULL AND cancelled = 0 ORDER BY due_at`, recipient)
}

// DueReminders lists every unfired, uncancelled reminder due at or before now.
func (s *Store) DueReminders(now time.Time) ([]reminder, error) {
	return s.queryReminders(`SELECT id, recipient, due_at, body FROM reminders
WHERE fired_at IS NULL AND cancelled = 0 AND due_at <= ? ORDER BY due_at`, now.UTC().Format(time.RFC3339))
}

func (s *Store) queryReminders(query string, args ...any) ([]reminder, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []reminder
	for rows.Next() {
		var r reminder
		var due string
		if err := rows.Scan(&r.ID, &r.Recipient, &due, &r.Body); err != nil {
			return nil, err
		}
		r.DueAt, _ = time.Parse(time.RFC3339, due)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) MarkFired(id int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE reminders SET fired_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339), id)
	return err
}

// CancelReminder returns false if no pending reminder with that id belongs
// to the recipient.
func (s *Store) CancelReminder(recipient string, id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE reminders SET cancelled = 1
WHERE id = ? AND recipient = ? AND fired_at IS NULL AND cancelled = 0`, id, recipient)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// TakeParked returns and removes every parked message for the recipient,
// oldest first.
func (s *Store) TakeParked(recipient string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id, body FROM parked WHERE recipient = ? ORDER BY id`, recipient)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var bodies []string
	for rows.Next() {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		bodies = append(bodies, body)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.db.Exec(`DELETE FROM parked WHERE id = ?`, id); err != nil {
			return bodies, err
		}
	}
	return bodies, nil
}
