package store

import (
	"database/sql"
	"fmt"
	"log/slog"
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
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  github_id                INTEGER UNIQUE NOT NULL,
  repo                     TEXT NOT NULL,
  number                   INTEGER NOT NULL,
  title                    TEXT NOT NULL,
  author                   TEXT NOT NULL,
  url                      TEXT NOT NULL,
  state                    TEXT NOT NULL,
  updated_at               DATETIME NOT NULL,
  fetched_at               DATETIME NOT NULL,
  dismissed                INTEGER NOT NULL DEFAULT 0,
  -- Review-state vigilance for auto_implement-created PRs (#482). The
  -- columns are managed by Tier 3 (external_*) and the response/fix
  -- modules (counters + last_responded_at), never by UpsertPR — see
  -- the explicit migration block below for idempotent ADD COLUMNs that
  -- cover existing DBs.
  external_review_state    TEXT NOT NULL DEFAULT '',
  external_reviewer        TEXT NOT NULL DEFAULT '',
  external_review_at       TEXT NOT NULL DEFAULT '',
  auto_implement_issue_id  INTEGER NOT NULL DEFAULT 0,
  review_response_count    INTEGER NOT NULL DEFAULT 0,
  review_fix_count         INTEGER NOT NULL DEFAULT 0,
  last_responded_at        TEXT NOT NULL DEFAULT ''
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
  published_at        TEXT NOT NULL DEFAULT '',
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
  fetched_at             DATETIME NOT NULL,
  dismissed              INTEGER NOT NULL DEFAULT 0,
  claimed_by_autonomous  INTEGER NOT NULL DEFAULT 0,
  autonomous_claim_until TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS issue_reviews (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id     INTEGER NOT NULL REFERENCES issues(id),
  cli_used     TEXT NOT NULL,
  summary      TEXT NOT NULL,
  triage       TEXT NOT NULL,
  refinement_data TEXT NOT NULL DEFAULT '',
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

CREATE TABLE IF NOT EXISTS reviews_in_flight (
  pr_id       INTEGER NOT NULL,
  head_sha    TEXT    NOT NULL,
  started_at  DATETIME NOT NULL,
  PRIMARY KEY (pr_id, head_sha)
);

-- Mirror of reviews_in_flight for the issue-triage pipeline. The updated_at
-- column stores the issue's UpdatedAt truncated to an ISO-seconds string so
-- two fetcher ticks observing the same snapshot collapse onto the same row.
-- See theburrowhub/heimdallm#292.
CREATE TABLE IF NOT EXISTS issue_triage_in_flight (
  issue_id    INTEGER NOT NULL,
  updated_at  TEXT    NOT NULL,
  started_at  DATETIME NOT NULL,
  PRIMARY KEY (issue_id, updated_at)
);

-- Persistent per-repo review instructions captured from authorized PR
-- comment directives (#383). Injected into every future review of the repo.
CREATE TABLE IF NOT EXISTS repo_instructions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  repo        TEXT NOT NULL,
  instruction TEXT NOT NULL,
  author      TEXT NOT NULL,
  comment_id  INTEGER NOT NULL,
  created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_repo_instructions_repo ON repo_instructions(repo);
CREATE UNIQUE INDEX IF NOT EXISTS idx_repo_instructions_comment ON repo_instructions(comment_id);

-- Dedup/audit guard so each directive comment is applied/acked exactly once
-- across poll cycles. GitHub comment ids are stable and effectively unique.
CREATE TABLE IF NOT EXISTS directive_marks (
  comment_id   INTEGER PRIMARY KEY,
  verb         TEXT NOT NULL,
  processed_at DATETIME NOT NULL
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
	db.Exec("ALTER TABLE reviews ADD COLUMN github_review_state TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE reviews ADD COLUMN head_sha TEXT NOT NULL DEFAULT ''")
	// published_at anchors the updated_at dedup window on the actual
	// post-to-GitHub time. Stored as TEXT (sqlite datetime format, see
	// sqliteTimeFormat) with empty-string default so legacy rows read as
	// time.Time{} and callers can fall back to created_at. See
	// theburrowhub/heimdallm#243 Fix 3.
	db.Exec("ALTER TABLE reviews ADD COLUMN published_at TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents ADD COLUMN instructions TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents ADD COLUMN cli_flags TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE agents RENAME COLUMN prompt TO prompt") // no-op, ensures column exists
	db.Exec("ALTER TABLE prs ADD COLUMN dismissed INTEGER NOT NULL DEFAULT 0")
	// Review-state vigilance (#482). Idempotent on existing DBs; the
	// schema constant above already includes these for fresh installs.
	db.Exec("ALTER TABLE prs ADD COLUMN external_review_state TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE prs ADD COLUMN external_reviewer TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE prs ADD COLUMN external_review_at TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE prs ADD COLUMN auto_implement_issue_id INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE prs ADD COLUMN review_response_count INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE prs ADD COLUMN review_fix_count INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE prs ADD COLUMN last_responded_at TEXT NOT NULL DEFAULT ''")
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
	db.Exec("ALTER TABLE issue_reviews ADD COLUMN refinement_data TEXT NOT NULL DEFAULT ''")
	// Covering index for the circuit-breaker counters (see issue #243).
	// CREATE INDEX IF NOT EXISTS is idempotent; safe on every startup.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_reviews_pr_created ON reviews(pr_id, created_at)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_reviews_pr_head_created ON reviews(pr_id, head_sha, created_at)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_reviews_created ON reviews(created_at)")
	// Hot path for CountReviewsForRepo (see issue #243). Without this the
	// JOIN drives from prs.repo with no index and table-scans on every
	// poll-cycle breaker check.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_prs_repo ON prs(repo)")
	// Hot path for PR identity fallback when GitHub's Search Issues API and
	// Pulls API disagree on github_id for the same repo/number (#351).
	db.Exec("CREATE INDEX IF NOT EXISTS idx_prs_repo_number ON prs(repo, number)")
	// Mirrors of the above for the issue-side circuit breaker added in
	// theburrowhub/heimdallm#292. Without these, CountIssueReviewsForIssue
	// and CountIssueTriagesForRepo table-scan issue_reviews on every
	// triage attempt.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_issue_reviews_issue_created ON issue_reviews(issue_id, created_at)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_issue_reviews_created ON issue_reviews(created_at)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_issues_repo ON issues(repo)")
	// Autonomous end-to-end pipeline (#spec). Idempotent on existing DBs;
	// the schema constant above already includes this column for fresh installs.
	db.Exec("ALTER TABLE issues ADD COLUMN claimed_by_autonomous INTEGER NOT NULL DEFAULT 0")
	// Time-based autonomous claim lease (#spec). Doubles as the failure/no-
	// progress cooldown and survives crashes (expires naturally). Idempotent
	// on existing DBs; the schema constant above includes it for fresh installs.
	db.Exec("ALTER TABLE issues ADD COLUMN autonomous_claim_until TEXT NOT NULL DEFAULT ''")
	// Idempotent migration for existing DBs — new installs get the table
	// from the schema constant above. Safe on every startup.
	db.Exec(`CREATE TABLE IF NOT EXISTS reviews_in_flight (
		pr_id       INTEGER NOT NULL,
		head_sha    TEXT    NOT NULL,
		started_at  DATETIME NOT NULL,
		PRIMARY KEY (pr_id, head_sha)
	)`)
	// Same pattern for the issue-triage claim table added in #292.
	db.Exec(`CREATE TABLE IF NOT EXISTS issue_triage_in_flight (
		issue_id    INTEGER NOT NULL,
		updated_at  TEXT    NOT NULL,
		started_at  DATETIME NOT NULL,
		PRIMARY KEY (issue_id, updated_at)
	)`)
	// Repo rename audit table (#489). RenameRepo writes a row here
	// in the same TX that bulk-renames prs/issues/activity_log/
	// watch_state. The audit table is informational — it is NOT
	// consulted to short-circuit idempotency of the UPDATEs (those
	// are naturally idempotent via `WHERE repo = oldRepo`), so the
	// log is safe across rename-back chains like A→B→A→B. The most
	// recent row per old_repo is read only on the empty-state edge
	// path to decide whether to insert a fresh audit row when the
	// UPDATEs matched zero rows.
	db.Exec(`CREATE TABLE IF NOT EXISTS repo_renames (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		old_repo    TEXT NOT NULL,
		new_repo    TEXT NOT NULL,
		renamed_at  DATETIME NOT NULL
	)`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_repo_renames_old ON repo_renames(old_repo)")
	db.Exec(`CREATE TABLE IF NOT EXISTS repo_instructions (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		repo        TEXT NOT NULL,
		instruction TEXT NOT NULL,
		author      TEXT NOT NULL,
		comment_id  INTEGER NOT NULL,
		created_at  DATETIME NOT NULL
	)`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_repo_instructions_repo ON repo_instructions(repo)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_repo_instructions_comment ON repo_instructions(comment_id)")
	db.Exec(`CREATE TABLE IF NOT EXISTS directive_marks (
		comment_id   INTEGER PRIMARY KEY,
		verb         TEXT NOT NULL,
		processed_at DATETIME NOT NULL
	)`)
	// watch_state is owned by bus.NewWatchStore at runtime, but RenameRepo
	// needs to UPDATE rows here in the same TX as the prs/issues moves.
	// Mirror the schema with IF NOT EXISTS so the rename can run from
	// tests and migration paths that have not yet constructed a WatchStore.
	db.Exec(`CREATE TABLE IF NOT EXISTS watch_state (
		key        TEXT PRIMARY KEY,
		type       TEXT NOT NULL,
		repo       TEXT NOT NULL,
		number     INTEGER NOT NULL,
		github_id  INTEGER NOT NULL,
		next_check TEXT NOT NULL,
		backoff_ns INTEGER NOT NULL,
		last_seen  TEXT NOT NULL
	)`)
	// Enforce single-flight per issue at the schema level (#458). The
	// claim SQL already uses INSERT ... WHERE NOT EXISTS, but a UNIQUE
	// index lifts the invariant from a query convention to a DB
	// guarantee so any future raw INSERT (test helpers, ad-hoc tooling)
	// cannot create a duplicate. The composite PK above is strictly
	// weaker than this index — it allows multiple rows per issue_id when
	// updated_at differs — so the index supersedes it as the contention
	// constraint; the PK remains as a row-identity convention.
	//
	// On daemons upgrading from pre-#458 the table may contain rows
	// like (42, T0), (42, T1) for the same issue. CREATE UNIQUE INDEX
	// returns "UNIQUE constraint failed" in that case (IF NOT EXISTS
	// only suppresses the "already exists" case, not constraint
	// failures). Dedupe first — keep the most recent claim per issue —
	// then create the index, then log if either step still errors so
	// silent failures are observable in operator logs.
	if _, err := db.Exec(`DELETE FROM issue_triage_in_flight
		WHERE rowid NOT IN (SELECT MAX(rowid) FROM issue_triage_in_flight GROUP BY issue_id)`); err != nil {
		slog.Warn("store: dedupe issue_triage_in_flight before unique index failed",
			"err", err)
	}
	if _, err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_triage_in_flight_issue ON issue_triage_in_flight(issue_id)"); err != nil {
		slog.Warn("store: create unique index on issue_triage_in_flight(issue_id) failed; "+
			"single-flight invariant rests on the INSERT … WHERE NOT EXISTS guard only",
			"err", err)
	}
	return &Store{db: db}, nil
}

// DB returns the underlying *sql.DB for shared use by subsystems that need
// direct database access (e.g. the watch_state table used by bus.WatchStore).
func (s *Store) DB() *sql.DB {
	return s.db
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

// SetConfigs upserts multiple key/value config entries atomically in a single
// transaction. If any write fails, the whole batch is rolled back, so the store
// is never left in a partial state (see #565: PUT /config must be all-or-nothing).
// An empty map is a no-op that returns nil.
func (s *Store) SetConfigs(kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: set configs: begin: %w", err)
	}
	// Rollback is a no-op once the tx has committed, so this defer safely
	// unwinds the transaction on any early return below.
	defer func() { _ = tx.Rollback() }()

	for key, value := range kv {
		if _, err := tx.Exec(
			"INSERT INTO configs (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
			key, value,
		); err != nil {
			return fmt.Errorf("store: set config %q: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set configs: commit: %w", err)
	}
	return nil
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
	if err := s.db.QueryRow("SELECT COUNT(*) FROM reviews r WHERE 1=1"+repoFilter, repoArgs...).Scan(&stats.TotalReviews); err != nil {
		return nil, fmt.Errorf("store: stats total reviews: %w", err)
	}

	// By severity
	if err := queryRows(s.db, "SELECT severity, COUNT(*) FROM reviews r WHERE 1=1"+repoFilter+" GROUP BY severity", repoArgs, func(rows *sql.Rows) error {
		var sev string
		var cnt int
		if err := rows.Scan(&sev, &cnt); err != nil {
			return err
		}
		stats.BySeverity[sev] = cnt
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: stats by severity: %w", err)
	}

	// By CLI
	if err := queryRows(s.db, "SELECT cli_used, COUNT(*) FROM reviews r WHERE 1=1"+repoFilter+" GROUP BY cli_used", repoArgs, func(rows *sql.Rows) error {
		var cli string
		var cnt int
		if err := rows.Scan(&cli, &cnt); err != nil {
			return err
		}
		stats.ByCLI[cli] = cnt
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: stats by cli: %w", err)
	}

	// Top repos by review count
	topRepoQuery := `
		SELECT p.repo, COUNT(r.id) as cnt
		FROM reviews r JOIN prs p ON p.id = r.pr_id
		WHERE p.repo != ''` + repoFilterJoined + `
		GROUP BY p.repo ORDER BY cnt DESC LIMIT 8`
	if err := queryRows(s.db, topRepoQuery, repoArgs, func(rows *sql.Rows) error {
		var rc RepoCount
		if err := rows.Scan(&rc.Repo, &rc.Count); err != nil {
			return err
		}
		stats.TopRepos = append(stats.TopRepos, rc)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: stats top repos: %w", err)
	}

	// Reviews per day last 7 days
	last7Query := `
		SELECT DATE(r.created_at) as day, COUNT(*) as cnt
		FROM reviews r
		WHERE r.created_at >= datetime('now', '-7 days')` + repoFilter + `
		GROUP BY day ORDER BY day ASC`
	if err := queryRows(s.db, last7Query, repoArgs, func(rows *sql.Rows) error {
		var dc DayCount
		if err := rows.Scan(&dc.Day, &dc.Count); err != nil {
			return err
		}
		stats.ReviewsLast7Days = append(stats.ReviewsLast7Days, dc)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: stats last 7 days: %w", err)
	}

	// Avg issues per review (issues is a JSON array stored as text)
	var totalIssues, reviewsWithIssues int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM reviews r WHERE issues != '[]' AND issues != 'null'"+repoFilter, repoArgs...).Scan(&reviewsWithIssues); err != nil {
		return nil, fmt.Errorf("store: stats reviews with issues: %w", err)
	}
	if reviewsWithIssues > 0 {
		if err := s.db.QueryRow("SELECT COALESCE(SUM(json_array_length(issues)),0) FROM reviews r WHERE issues IS NOT NULL"+repoFilter, repoArgs...).Scan(&totalIssues); err != nil {
			return nil, fmt.Errorf("store: stats total issues: %w", err)
		}
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
	var durations []float64
	if err := queryRows(s.db, timingQuery, repoArgs, func(rows *sql.Rows) error {
		var d float64
		if err := rows.Scan(&d); err != nil {
			return err
		}
		// Sanity check: 5s–3600s (ignore implausible values)
		if d >= 5 && d <= 3600 {
			durations = append(durations, d)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: stats timing: %w", err)
	}
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

	// Activity log counter (last 24h). Non-fatal: a failing query leaves the
	// field zero rather than breaking /stats entirely.
	if n, err := s.CountActivitySince(time.Now().Add(-24 * time.Hour)); err == nil {
		stats.ActivityCount24h = n
	}

	return stats, nil
}

// queryRows runs query and calls scan once per row, returning the first of any
// Query, scan, or iteration (rows.Err) error. The *sql.Rows is closed before
// queryRows returns, so each result set is released as soon as its caller block
// finishes rather than being held open until the enclosing function returns.
func queryRows(db *sql.DB, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
