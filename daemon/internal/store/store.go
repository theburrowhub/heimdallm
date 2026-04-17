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
  fetched_at DATETIME NOT NULL,
  dismissed  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS reviews (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  pr_id            INTEGER NOT NULL REFERENCES prs(id),
  cli_used         TEXT NOT NULL,
  summary          TEXT NOT NULL,
  issues           TEXT NOT NULL,
  suggestions      TEXT NOT NULL,
  severity         TEXT NOT NULL,
  created_at       DATETIME NOT NULL,
  github_review_id INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS configs (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  cli          TEXT NOT NULL DEFAULT 'claude',
  prompt       TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL DEFAULT '',
  cli_flags    TEXT NOT NULL DEFAULT '',
  is_default   INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL
);

-- Issue tracking pipeline (#24). The assignees and labels columns hold JSON
-- arrays of strings so we do not have to create a separate join table just
-- for display; the issue_reviews downstream consumers treat the whole row
-- as one record.
CREATE TABLE IF NOT EXISTS issues (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  github_id   INTEGER UNIQUE NOT NULL,
  repo        TEXT NOT NULL,
  number      INTEGER NOT NULL,
  title       TEXT NOT NULL,
  body        TEXT NOT NULL DEFAULT '',
  author      TEXT NOT NULL,
  assignees   TEXT NOT NULL DEFAULT '[]',
  labels      TEXT NOT NULL DEFAULT '[]',
  state       TEXT NOT NULL,
  created_at  DATETIME NOT NULL,
  fetched_at  DATETIME NOT NULL,
  dismissed   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS issue_reviews (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id     INTEGER NOT NULL REFERENCES issues(id),
  cli_used     TEXT NOT NULL,
  summary      TEXT NOT NULL,
  triage       TEXT NOT NULL,
  suggestions  TEXT NOT NULL DEFAULT '[]',
  action_taken TEXT NOT NULL DEFAULT 'review_only',
  pr_created   INTEGER NOT NULL DEFAULT 0,
  created_at   DATETIME NOT NULL
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
	// Migrate existing DBs (ALTER TABLE ignores "duplicate column" errors silently)
	db.Exec("ALTER TABLE reviews ADD COLUMN github_review_id INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE agents ADD COLUMN instructions TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents ADD COLUMN cli_flags TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents RENAME COLUMN prompt TO prompt") // no-op, ensures column exists
	db.Exec("ALTER TABLE prs ADD COLUMN dismissed INTEGER NOT NULL DEFAULT 0")
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

// ReviewTimingStats contains metrics about how long reviews take.
// Duration is measured from prs.fetched_at (pipeline start) to reviews.created_at (AI done).
type ReviewTimingStats struct {
	SampleCount    int     `json:"sample_count"`
	AvgSeconds     float64 `json:"avg_seconds"`
	MedianSeconds  float64 `json:"median_seconds"`
	MinSeconds     float64 `json:"min_seconds"`
	MaxSeconds     float64 `json:"max_seconds"`
	BucketFast     int     `json:"bucket_fast"`      // < 30 s
	BucketMedium   int     `json:"bucket_medium"`    // 30–120 s
	BucketSlow     int     `json:"bucket_slow"`      // 120–300 s
	BucketVerySlow int     `json:"bucket_very_slow"` // > 300 s
}

// Stats is the data returned by GET /stats.
type Stats struct {
	TotalReviews       int               `json:"total_reviews"`
	BySeverity         map[string]int    `json:"by_severity"`
	ByCLI              map[string]int    `json:"by_cli"`
	TopRepos           []RepoCount       `json:"top_repos"`
	ReviewsLast7Days   []DayCount        `json:"reviews_last_7_days"`
	AvgIssuesPerReview float64           `json:"avg_issues_per_review"`
	ReviewTiming       ReviewTimingStats `json:"review_timing"`
}

type RepoCount struct {
	Repo  string `json:"repo"`
	Count int    `json:"count"`
}

type DayCount struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

// ComputeStats aggregates statistics from the reviews and prs tables.
func (s *Store) ComputeStats() (*Stats, error) {
	stats := &Stats{
		BySeverity: make(map[string]int),
		ByCLI:      make(map[string]int),
	}

	// Total reviews
	s.db.QueryRow("SELECT COUNT(*) FROM reviews").Scan(&stats.TotalReviews)

	// By severity
	rows, _ := s.db.Query("SELECT severity, COUNT(*) FROM reviews GROUP BY severity")
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var sev string
			var cnt int
			rows.Scan(&sev, &cnt)
			stats.BySeverity[sev] = cnt
		}
	}

	// By CLI
	rows2, _ := s.db.Query("SELECT cli_used, COUNT(*) FROM reviews GROUP BY cli_used")
	if rows2 != nil {
		defer rows2.Close()
		for rows2.Next() {
			var cli string
			var cnt int
			rows2.Scan(&cli, &cnt)
			stats.ByCLI[cli] = cnt
		}
	}

	// Top repos by review count
	rows3, _ := s.db.Query(`
		SELECT p.repo, COUNT(r.id) as cnt
		FROM reviews r JOIN prs p ON p.id = r.pr_id
		WHERE p.repo != ''
		GROUP BY p.repo ORDER BY cnt DESC LIMIT 8
	`)
	if rows3 != nil {
		defer rows3.Close()
		for rows3.Next() {
			var rc RepoCount
			rows3.Scan(&rc.Repo, &rc.Count)
			stats.TopRepos = append(stats.TopRepos, rc)
		}
	}

	// Reviews per day last 7 days
	rows4, _ := s.db.Query(`
		SELECT DATE(created_at) as day, COUNT(*) as cnt
		FROM reviews
		WHERE created_at >= datetime('now', '-7 days')
		GROUP BY day ORDER BY day ASC
	`)
	if rows4 != nil {
		defer rows4.Close()
		for rows4.Next() {
			var dc DayCount
			rows4.Scan(&dc.Day, &dc.Count)
			stats.ReviewsLast7Days = append(stats.ReviewsLast7Days, dc)
		}
	}

	// Avg issues per review (issues is a JSON array stored as text)
	var totalIssues, reviewsWithIssues int
	s.db.QueryRow(`SELECT COUNT(*) FROM reviews WHERE issues != '[]' AND issues != 'null'`).Scan(&reviewsWithIssues)
	if reviewsWithIssues > 0 {
		// Approximate: count total issue objects via json_array_length
		s.db.QueryRow(`SELECT COALESCE(SUM(json_array_length(issues)),0) FROM reviews WHERE issues IS NOT NULL`).Scan(&totalIssues)
		if stats.TotalReviews > 0 {
			stats.AvgIssuesPerReview = float64(totalIssues) / float64(stats.TotalReviews)
		}
	}

	// Review timing: duration from pipeline start (prs.fetched_at) to AI done (reviews.created_at).
	// Fetch last 200 published reviews and compute stats in Go for accuracy.
	timingRows, _ := s.db.Query(`
		SELECT (julianday(r.created_at) - julianday(p.fetched_at)) * 86400.0
		FROM reviews r
		JOIN prs p ON p.id = r.pr_id
		WHERE r.github_review_id > 0
		  AND p.fetched_at IS NOT NULL
		  AND p.fetched_at != ''
		ORDER BY r.created_at DESC
		LIMIT 200
	`)
	if timingRows != nil {
		var durations []float64
		for timingRows.Next() {
			var d float64
			timingRows.Scan(&d)
			// Sanity check: 5s–3600s (ignore implausible values)
			if d >= 5 && d <= 3600 {
				durations = append(durations, d)
			}
		}
		timingRows.Close()
		if n := len(durations); n > 0 {
			t := &stats.ReviewTiming
			t.SampleCount = n
			sum, minD, maxD := 0.0, durations[0], durations[0]
			for _, d := range durations {
				sum += d
				if d < minD { minD = d }
				if d > maxD { maxD = d }
				switch {
				case d < 30:   t.BucketFast++
				case d < 120:  t.BucketMedium++
				case d < 300:  t.BucketSlow++
				default:       t.BucketVerySlow++
				}
			}
			t.AvgSeconds = sum / float64(n)
			t.MinSeconds = minD
			t.MaxSeconds = maxD
			// Median (durations are already in insertion order, approximate)
			sorted := make([]float64, n)
			copy(sorted, durations)
			// Simple insertion sort for small N
			for i := 1; i < n; i++ {
				for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
					sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
				}
			}
			if n%2 == 0 {
				t.MedianSeconds = (sorted[n/2-1] + sorted[n/2]) / 2
			} else {
				t.MedianSeconds = sorted[n/2]
			}
		}
	}

	return stats, nil
}
