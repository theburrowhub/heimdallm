package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Activity is one row in the activity_log timeline.
type Activity struct {
	ID          int64     `json:"id"`
	Timestamp   time.Time `json:"ts"`
	Org         string    `json:"org"`
	Repo        string    `json:"repo"`
	ItemType    string    `json:"item_type"`
	ItemNumber  int       `json:"item_number"`
	ItemTitle   string    `json:"item_title"`
	Action      string    `json:"action"`
	Outcome     string    `json:"outcome"`
	DetailsJSON string    `json:"-"`
	CreatedAt   time.Time `json:"-"`
}

// ActivityQuery bounds a ListActivity call.
// Zero values for From/To mean "no lower/upper bound" but the handler always
// supplies a bounded window, so unbounded queries only happen in tests.
type ActivityQuery struct {
	From    time.Time
	To      time.Time
	Orgs    []string
	Repos   []string
	Actions []string
	Limit   int
}

const defaultActivityLimit = 500
const maxActivityLimit = 5000

// InsertActivity writes one event row. Pass nil or empty details for "{}".
func (s *Store) InsertActivity(
	ts time.Time, org, repo, itemType string, itemNumber int,
	itemTitle, action, outcome string, details map[string]any,
) (int64, error) {
	payload := "{}"
	if len(details) > 0 {
		b, err := json.Marshal(details)
		if err != nil {
			return 0, fmt.Errorf("store: marshal activity details: %w", err)
		}
		payload = string(b)
	}
	now := time.Now().UTC().Format(sqliteTimeFormat)
	res, err := s.db.Exec(`
		INSERT INTO activity_log (ts, org, repo, item_type, item_number, item_title, action, outcome, details, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ts.UTC().Format(sqliteTimeFormat), org, repo, itemType, itemNumber,
		itemTitle, action, outcome, payload, now)
	if err != nil {
		return 0, fmt.Errorf("store: insert activity: %w", err)
	}
	return res.LastInsertId()
}

// ListActivity returns entries matching the query, newest first.
// Second return value is truncated — true when the result hit the limit.
func (s *Store) ListActivity(q ActivityQuery) ([]*Activity, bool, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}

	var (
		where []string
		args  []any
	)
	if !q.From.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, q.From.UTC().Format(sqliteTimeFormat))
	}
	if !q.To.IsZero() {
		where = append(where, "ts <= ?")
		args = append(args, q.To.UTC().Format(sqliteTimeFormat))
	}
	if len(q.Orgs) > 0 {
		where = append(where, "org IN ("+placeholders(len(q.Orgs))+")")
		for _, o := range q.Orgs {
			args = append(args, o)
		}
	}
	if len(q.Repos) > 0 {
		where = append(where, "repo IN ("+placeholders(len(q.Repos))+")")
		for _, r := range q.Repos {
			args = append(args, r)
		}
	}
	if len(q.Actions) > 0 {
		where = append(where, "action IN ("+placeholders(len(q.Actions))+")")
		for _, a := range q.Actions {
			args = append(args, a)
		}
	}

	// Over-fetch by 1 to detect truncation without a second COUNT query.
	args = append(args, limit+1)
	query := `
		SELECT id, ts, org, repo, item_type, item_number, item_title, action, outcome, details, created_at
		FROM activity_log
	`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY ts DESC LIMIT ?"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("store: list activity: %w", err)
	}
	defer rows.Close()

	var out []*Activity
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("store: iterate activity: %w", err)
	}

	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, nil
}

// PurgeOldActivity deletes activity rows older than maxDays. No-op if maxDays == 0.
func (s *Store) PurgeOldActivity(maxDays int) error {
	if maxDays == 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(maxDays) * 24 * time.Hour).Format(sqliteTimeFormat)
	_, err := s.db.Exec("DELETE FROM activity_log WHERE ts < ?", cutoff)
	if err != nil {
		return fmt.Errorf("store: purge old activity: %w", err)
	}
	return nil
}

func scanActivity(s scanner) (*Activity, error) {
	var a Activity
	var tsStr, createdStr string
	if err := s.Scan(&a.ID, &tsStr, &a.Org, &a.Repo, &a.ItemType,
		&a.ItemNumber, &a.ItemTitle, &a.Action, &a.Outcome,
		&a.DetailsJSON, &createdStr); err != nil {
		return nil, fmt.Errorf("store: scan activity: %w", err)
	}
	var err error
	if a.Timestamp, err = time.Parse(sqliteTimeFormat, tsStr); err != nil {
		return nil, fmt.Errorf("store: parse ts %q: %w", tsStr, err)
	}
	if a.CreatedAt, err = time.Parse(sqliteTimeFormat, createdStr); err != nil {
		return nil, fmt.Errorf("store: parse created_at %q: %w", createdStr, err)
	}
	return &a, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
