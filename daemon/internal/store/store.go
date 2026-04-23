package store

import (
	"database/sql"
	"fmt"
	"strings"
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
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  pr_id               INTEGER NOT NULL REFERENCES prs(id),
  cli_used            TEXT NOT NULL,
  summary             TEXT NOT NULL,
  issues              TEXT NOT NULL,
  suggestions         TEXT NOT NULL,
  severity            TEXT NOT NULL,
  created_at          DATETIME NOT NULL,
  github_review_id    INTEGER NOT NULL DEFAULT 0,
  github_review_state TEXT NOT NULL DEFAULT '',
  head_sha            TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS configs (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  id                     TEXT PRIMARY KEY,
  name                   TEXT NOT NULL,
  cli                    TEXT NOT NULL DEFAULT 'claude',
  prompt                 TEXT NOT NULL DEFAULT '',
  instructions           TEXT NOT NULL DEFAULT '',
  cli_flags              TEXT NOT NULL DEFAULT '',
  -- Legacy column, kept so the migration seed below can read from it on
  -- existing DBs. No code writes to it after this release; the three
  -- per-category flags below are the source of truth.
  is_default             INTEGER NOT NULL DEFAULT 0,
  is_default_pr          INTEGER NOT NULL DEFAULT 0,
  is_default_issue       INTEGER NOT NULL DEFAULT 0,
  is_default_dev         INTEGER NOT NULL DEFAULT 0,
  created_at             DATETIME NOT NULL,
  issue_prompt           TEXT NOT NULL DEFAULT '',
  issue_instructions     TEXT NOT NULL DEFAULT '',
  implement_prompt       TEXT NOT NULL DEFAULT '',
  implement_instructions TEXT NOT NULL DEFAULT ''
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

CREATE TABLE IF NOT EXISTS activity_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          DATETIME NOT NULL,
  org         TEXT NOT NULL,
  repo        TEXT NOT NULL,
  item_type   TEXT NOT NULL,
  item_number INTEGER NOT NULL,
  item_title  TEXT NOT NULL,
  action      TEXT NOT NULL,
  outcome     TEXT NOT NULL DEFAULT '',
  details     TEXT NOT NULL DEFAULT '{}',
  created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_activity_ts      ON activity_log(ts DESC);
CREATE INDEX IF NOT EXISTS idx_activity_repo_ts ON activity_log(repo, ts DESC);
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
	db.Exec("ALTER TABLE reviews ADD COLUMN github_review_state TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE reviews ADD COLUMN head_sha TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents ADD COLUMN instructions TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents ADD COLUMN cli_flags TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents RENAME COLUMN prompt TO prompt") // no-op, ensures column exists
	db.Exec("ALTER TABLE prs ADD COLUMN dismissed INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE agents ADD COLUMN issue_prompt TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents ADD COLUMN issue_instructions TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents ADD COLUMN implement_prompt TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents ADD COLUMN implement_instructions TEXT NOT NULL DEFAULT ''")
	// Split the single global `is_default` flag into three per-category flags
	// so users can activate a different prompt for PR review, issue triage,
	// and auto-implement independently. On existing DBs, seed all three from
	// the legacy flag the first time the new columns appear — that preserves
	// current user-visible behaviour (whichever agent was active keeps driving
	// all three pipelines until the user re-activates per category).
	if _, err := db.Exec("ALTER TABLE agents ADD COLUMN is_default_pr INTEGER NOT NULL DEFAULT 0"); err == nil {
		db.Exec("UPDATE agents SET is_default_pr = is_default")
	}
	if _, err := db.Exec("ALTER TABLE agents ADD COLUMN is_default_issue INTEGER NOT NULL DEFAULT 0"); err == nil {
		db.Exec("UPDATE agents SET is_default_issue = is_default")
	}
	if _, err := db.Exec("ALTER TABLE agents ADD COLUMN is_default_dev INTEGER NOT NULL DEFAULT 0"); err == nil {
		db.Exec("UPDATE agents SET is_default_dev = is_default")
	}
	db.Exec("ALTER TABLE issue_reviews ADD COLUMN commented_at DATETIME NOT NULL DEFAULT ''")
	// Covering index for the circuit-breaker counters (see issue #243).
	// CREATE INDEX IF NOT EXISTS is idempotent; safe on every startup.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_reviews_pr_created ON reviews(pr_id, created_at)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_reviews_created ON reviews(created_at)")
	// Hot path for CountReviewsForRepo (see issue #243). Without this the
	// JOIN drives from prs.repo with no index and table-scans on every
	// poll-cycle breaker check.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_prs_repo ON prs(repo)")
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

// ListConfigs returns every row in the configs table as a key→value map.
// Consumed by config.ApplyStore during reload so user edits made via
// PUT /config actually reach the running Config struct.
func (s *Store) ListConfigs() (map[string]string, error) {
	rows, err := s.db.Query("SELECT key, value FROM configs")
	if err != nil {
		return nil, fmt.Errorf("store: list configs: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: scan config row: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate configs: %w", err)
	}
	return out, nil
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
	ActivityCount24h   int               `json:"activity_count_24h"`
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
// When repos is non-empty, results are scoped to PRs in those repos.
// When orgs is non-empty, results are scoped to PRs whose repo starts
// with "org/" (i.e. belongs to one of the given GitHub organizations).
// repos takes precedence over orgs; both empty = global stats.
func (s *Store) ComputeStats(repos []string, orgs []string) (*Stats, error) {
	stats := &Stats{
		BySeverity: make(map[string]int),
		ByCLI:      make(map[string]int),
	}

	// Build reusable filter clauses.
	// repoFilter: for queries on reviews only (uses subquery on prs table).
	// repoFilterJoined: for queries that already JOIN prs p.
	// Both use the same repoArgs since the placeholder count matches.
	var repoFilter, repoFilterJoined string
	var repoArgs []any
	if len(repos) > 0 {
		placeholders := make([]string, len(repos))
		for i, r := range repos {
			placeholders[i] = "?"
			repoArgs = append(repoArgs, r)
		}
		inClause := strings.Join(placeholders, ",")
		repoFilter = " AND r.pr_id IN (SELECT id FROM prs WHERE repo IN (" + inClause + "))"
		repoFilterJoined = " AND p.repo IN (" + inClause + ")"
	} else if len(orgs) > 0 {
		// Org filter: match repos starting with "org/" using LIKE.
		// Escape SQL LIKE wildcards in org names to prevent unintended matches
		// (e.g. org "my_team" matching "myXteam/repo" via unescaped _).
		likeEscaper := strings.NewReplacer("%", "\\%", "_", "\\_")
		var conditions, conditionsJ []string
		for _, org := range orgs {
			conditions = append(conditions, "repo LIKE ? ESCAPE '\\'")
			conditionsJ = append(conditionsJ, "p.repo LIKE ? ESCAPE '\\'")
			repoArgs = append(repoArgs, likeEscaper.Replace(org)+"/%")
		}
		repoFilter = " AND r.pr_id IN (SELECT id FROM prs WHERE " + strings.Join(conditions, " OR ") + ")"
		repoFilterJoined = " AND (" + strings.Join(conditionsJ, " OR ") + ")"
	}

	// Total reviews
	s.db.QueryRow("SELECT COUNT(*) FROM reviews r WHERE 1=1"+repoFilter, repoArgs...).Scan(&stats.TotalReviews)

	// By severity
	rows, _ := s.db.Query("SELECT severity, COUNT(*) FROM reviews r WHERE 1=1"+repoFilter+" GROUP BY severity", repoArgs...)
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
	rows2, _ := s.db.Query("SELECT cli_used, COUNT(*) FROM reviews r WHERE 1=1"+repoFilter+" GROUP BY cli_used", repoArgs...)
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
	topRepoQuery := `
		SELECT p.repo, COUNT(r.id) as cnt
		FROM reviews r JOIN prs p ON p.id = r.pr_id
		WHERE p.repo != ''` + repoFilterJoined + `
		GROUP BY p.repo ORDER BY cnt DESC LIMIT 8`
	rows3, _ := s.db.Query(topRepoQuery, repoArgs...)
	if rows3 != nil {
		defer rows3.Close()
		for rows3.Next() {
			var rc RepoCount
			rows3.Scan(&rc.Repo, &rc.Count)
			stats.TopRepos = append(stats.TopRepos, rc)
		}
	}

	// Reviews per day last 7 days
	last7Query := `
		SELECT DATE(r.created_at) as day, COUNT(*) as cnt
		FROM reviews r
		WHERE r.created_at >= datetime('now', '-7 days')` + repoFilter + `
		GROUP BY day ORDER BY day ASC`
	rows4, _ := s.db.Query(last7Query, repoArgs...)
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
	s.db.QueryRow("SELECT COUNT(*) FROM reviews r WHERE issues != '[]' AND issues != 'null'"+repoFilter, repoArgs...).Scan(&reviewsWithIssues)
	if reviewsWithIssues > 0 {
		s.db.QueryRow("SELECT COALESCE(SUM(json_array_length(issues)),0) FROM reviews r WHERE issues IS NOT NULL"+repoFilter, repoArgs...).Scan(&totalIssues)
		if stats.TotalReviews > 0 {
			stats.AvgIssuesPerReview = float64(totalIssues) / float64(stats.TotalReviews)
		}
	}

	// Review timing: duration from pipeline start (prs.fetched_at) to AI done (reviews.created_at).
	timingQuery := `
		SELECT (julianday(r.created_at) - julianday(p.fetched_at)) * 86400.0
		FROM reviews r
		JOIN prs p ON p.id = r.pr_id
		WHERE r.github_review_id > 0
		  AND p.fetched_at IS NOT NULL
		  AND p.fetched_at != ''` + repoFilterJoined + `
		ORDER BY r.created_at DESC
		LIMIT 200`
	timingRows, _ := s.db.Query(timingQuery, repoArgs...)
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
				if d < minD {
					minD = d
				}
				if d > maxD {
					maxD = d
				}
				switch {
				case d < 30:
					t.BucketFast++
				case d < 120:
					t.BucketMedium++
				case d < 300:
					t.BucketSlow++
				default:
					t.BucketVerySlow++
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

	// Activity log counter (last 24h). Non-fatal: a failing query leaves the
	// field zero rather than breaking /stats entirely.
	if n, err := s.CountActivitySince(time.Now().Add(-24 * time.Hour)); err == nil {
		stats.ActivityCount24h = n
	}

	return stats, nil
}
