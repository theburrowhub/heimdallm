package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// sqliteTimeFormat is the datetime layout used when reading time values back from SQLite.
// The modernc.org/sqlite driver auto-converts stored DATETIME text to RFC3339 on read,
// so we store and parse in RFC3339. For range comparisons (e.g. PurgeOldReviews) we
// compute the cutoff in Go rather than using SQLite's datetime() function, which would
// return a space-separated string that compares incorrectly against RFC3339 values.
const sqliteTimeFormat = time.RFC3339

// Store wraps a SQLite database connection.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS prs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  github_id  INTEGER UNIQUE NOT NULL,
  repo       TEXT NOT NULL,
  number     INTEGER NOT NULL,
  title      TEXT NOT NULL,
  author     TEXT NOT NULL,
  url        TEXT NOT NULL,
  state      TEXT NOT NULL,
  updated_at DATETIME NOT NULL,
  fetched_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS reviews (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  pr_id       INTEGER NOT NULL REFERENCES prs(id),
  cli_used    TEXT NOT NULL,
  summary     TEXT NOT NULL,
  issues      TEXT NOT NULL,
  suggestions TEXT NOT NULL,
  severity    TEXT NOT NULL,
  created_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS configs (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// Open opens (or creates) a SQLite database at dsn and applies the schema.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", dsn, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// SetConfig upserts a key/value config entry.
func (s *Store) SetConfig(key, value string) (int64, error) {
	res, err := s.db.Exec(
		"INSERT INTO configs (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, value,
	)
	if err != nil {
		return 0, fmt.Errorf("store: set config: %w", err)
	}
	return res.LastInsertId()
}

// GetConfig retrieves the value for a config key. Returns sql.ErrNoRows if not found.
func (s *Store) GetConfig(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM configs WHERE key = ?", key).Scan(&value)
	return value, err
}
