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
);`
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
