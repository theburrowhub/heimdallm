package store_test

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPR_UpsertAndGet(t *testing.T) {
	s := newTestStore(t)
	pr := &store.PR{
		GithubID:  101,
		Repo:      "org/repo",
		Number:    42,
		Title:     "Fix bug",
		Author:    "alice",
		URL:       "https://github.com/org/repo/pull/42",
		State:     "open",
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
		FetchedAt: time.Now().UTC().Truncate(time.Second),
	}
	id, err := s.UpsertPR(pr)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}
	got, err := s.GetPR(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != pr.Title {
		t.Errorf("title mismatch: got %q want %q", got.Title, pr.Title)
	}
}

func TestPR_GetByRepoNumberPrefersReviewedRow(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	reviewedID, err := s.UpsertPR(&store.PR{
		GithubID: 101, Repo: "org/repo", Number: 42, Title: "reviewed",
		Author: "alice", URL: "https://github.com/org/repo/pull/42",
		State: "open", UpdatedAt: now.Add(-2 * time.Minute), FetchedAt: now.Add(-2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("upsert reviewed pr: %v", err)
	}
	if _, err := s.InsertReview(&store.Review{
		PRID: reviewedID, CLIUsed: "claude", Summary: "ok",
		Issues: "[]", Suggestions: "[]", Severity: "low",
		CreatedAt: now.Add(-time.Minute), PublishedAt: now.Add(-time.Minute),
		HeadSHA: "abc",
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}
	if _, err := s.UpsertPR(&store.PR{
		GithubID: 202, Repo: "org/repo", Number: 42, Title: "unreviewed duplicate",
		Author: "alice", URL: "https://github.com/org/repo/pull/42",
		State: "open", UpdatedAt: now, FetchedAt: now,
	}); err != nil {
		t.Fatalf("upsert duplicate pr: %v", err)
	}

	got, err := s.GetPRByRepoNumber("org/repo", 42)
	if err != nil {
		t.Fatalf("get by repo number: %v", err)
	}
	if got.ID != reviewedID {
		t.Errorf("GetPRByRepoNumber picked row %d, want reviewed row %d", got.ID, reviewedID)
	}
}

// TestPR_NotFoundReturnsErrPRNotFound guards the cluster dispatch fix: a peer
// instance that receives an id it does not recognise (a rowid or github_id
// minted by a different daemon) must get a stable, checkable error rather
// than a bare sql.ErrNoRows leaking through as "store: scan pr: sql: no rows
// in result set".
func TestPR_NotFoundReturnsErrPRNotFound(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.GetPR(999999); !errors.Is(err, store.ErrPRNotFound) {
		t.Errorf("GetPR: expected ErrPRNotFound, got %v", err)
	}
	if _, err := s.GetPRByGithubID(999999); !errors.Is(err, store.ErrPRNotFound) {
		t.Errorf("GetPRByGithubID: expected ErrPRNotFound, got %v", err)
	}
	if _, err := s.GetPRByRepoNumber("org/repo", 999); !errors.Is(err, store.ErrPRNotFound) {
		t.Errorf("GetPRByRepoNumber: expected ErrPRNotFound, got %v", err)
	}
}

func TestReview_InsertAndList(t *testing.T) {
	s := newTestStore(t)
	pr := &store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()}
	prID, _ := s.UpsertPR(pr)

	rev := &store.Review{
		PRID:        prID,
		CLIUsed:     "claude",
		Summary:     "Looks good",
		Issues:      `[{"file":"main.go","line":10,"description":"nil deref","severity":"high"}]`,
		Suggestions: `["add nil check"]`,
		Severity:    "high",
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}
	revID, err := s.InsertReview(rev)
	if err != nil {
		t.Fatalf("insert review: %v", err)
	}
	if revID == 0 {
		t.Fatal("expected non-zero review id")
	}

	reviews, err := s.ListReviewsForPR(prID)
	if err != nil {
		t.Fatalf("list reviews: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(reviews))
	}
	if reviews[0].Summary != "Looks good" {
		t.Errorf("summary mismatch: %q", reviews[0].Summary)
	}
}

func TestMarkReviewPublished_RoundTripsStateAndID(t *testing.T) {
	// Locks in the behaviour the web UI relies on for the review-decision
	// badge: after SubmitReview succeeds, the GitHub-returned state must
	// survive a store round-trip so PRTile can render "Approved" vs
	// "Changes requested" without re-deriving from severity.
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{
		GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a",
		URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
	})

	rev := &store.Review{
		PRID:        prID,
		CLIUsed:     "claude",
		Summary:     "ok",
		Issues:      "[]",
		Suggestions: "[]",
		Severity:    "low",
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}
	revID, err := s.InsertReview(rev)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Freshly inserted rows have no published state.
	latest, err := s.LatestReviewForPR(prID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.GitHubReviewID != 0 || latest.GitHubReviewState != "" {
		t.Fatalf("pre-publish got (id=%d, state=%q), want (0, \"\")",
			latest.GitHubReviewID, latest.GitHubReviewState)
	}

	publishedAt := time.Now().UTC().Truncate(time.Second)
	if err := s.MarkReviewPublished(revID, 98765, "APPROVED", publishedAt); err != nil {
		t.Fatalf("mark published: %v", err)
	}

	got, err := s.LatestReviewForPR(prID)
	if err != nil {
		t.Fatalf("latest after publish: %v", err)
	}
	if got.GitHubReviewID != 98765 {
		t.Errorf("GitHubReviewID = %d, want 98765", got.GitHubReviewID)
	}
	if got.GitHubReviewState != "APPROVED" {
		t.Errorf("GitHubReviewState = %q, want %q", got.GitHubReviewState, "APPROVED")
	}
	// PublishedAt should round-trip — it's the new anchor the dedup uses,
	// so a storage regression here would silently re-break #243.
	if !got.PublishedAt.Equal(publishedAt) {
		t.Errorf("PublishedAt = %v, want %v", got.PublishedAt, publishedAt)
	}
}

// TestReview_HeadSHARoundTrip covers the field added to deduplicate re-reviews
// by HEAD commit SHA instead of the PR's updated_at (which is bumped every time
// any reviewer — including a peer bot — submits a review, causing bot-feedback
// loops on the same commit).
func TestReview_HeadSHARoundTrip(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{
		GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a",
		URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
	})

	rev := &store.Review{
		PRID: prID, CLIUsed: "claude", Summary: "ok",
		Issues: "[]", Suggestions: "[]", Severity: "low",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		HeadSHA:   "deadbeefcafef00d",
	}
	if _, err := s.InsertReview(rev); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.LatestReviewForPR(prID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if got.HeadSHA != "deadbeefcafef00d" {
		t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, "deadbeefcafef00d")
	}
}

// TestReview_EventRoundTrip covers the event column added so the daemon's
// decided GitHub review event (APPROVE|COMMENT|REQUEST_CHANGES) is persisted
// and reproduced on retry rather than re-derived from severity, which could
// drift if config changes between the original decision and a later retry.
func TestReview_EventRoundTrip(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{
		GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a",
		URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
	})

	rev := &store.Review{
		PRID: prID, CLIUsed: "claude",
		Issues: "[]", Suggestions: "[]", Severity: "low",
		Event:     "COMMENT",
		CreatedAt: time.Now().UTC(),
		HeadSHA:   "abc123",
	}
	id, err := s.InsertReview(rev)
	if err != nil {
		t.Fatalf("InsertReview: %v", err)
	}
	got, err := s.GetReview(id)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if got.Event != "COMMENT" {
		t.Errorf("Event = %q, want %q", got.Event, "COMMENT")
	}
}

func TestPR_ListAll(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ {
		s.UpsertPR(&store.PR{GithubID: int64(i + 1), Repo: "org/r", Number: i + 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})
	}
	prs, err := s.ListPRs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prs) != 3 {
		t.Errorf("expected 3 prs, got %d", len(prs))
	}
}

func TestRetentionPurge(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})
	old := &store.Review{
		PRID: prID, CLIUsed: "claude", Summary: "s", Issues: "[]", Suggestions: "[]", Severity: "low",
		CreatedAt: time.Now().Add(-100 * 24 * time.Hour),
	}
	s.InsertReview(old)
	err := s.PurgeOldReviews(90)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	reviews, _ := s.ListReviewsForPR(prID)
	if len(reviews) != 0 {
		t.Errorf("expected 0 reviews after purge, got %d", len(reviews))
	}
}

// Regression for #551: a non-positive maxDays must not compute a future cutoff
// and delete the entire review history; both 0 and negatives are no-ops.
func TestPurgeOldReviews_NonPositiveIsNoOp(t *testing.T) {
	for _, maxDays := range []int{0, -1, -365} {
		t.Run(fmt.Sprintf("maxDays=%d", maxDays), func(t *testing.T) {
			s := newTestStore(t)
			prID, err := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})
			if err != nil {
				t.Fatalf("upsert pr: %v", err)
			}
			for _, age := range []time.Duration{-100 * 24 * time.Hour, -1 * time.Hour} {
				if _, err := s.InsertReview(&store.Review{
					PRID: prID, CLIUsed: "claude", Summary: "s", Issues: "[]", Suggestions: "[]", Severity: "low",
					CreatedAt: time.Now().Add(age),
				}); err != nil {
					t.Fatalf("insert review: %v", err)
				}
			}
			if err := s.PurgeOldReviews(maxDays); err != nil {
				t.Fatalf("purge: %v", err)
			}
			reviews, err := s.ListReviewsForPR(prID)
			if err != nil {
				t.Fatalf("list reviews: %v", err)
			}
			if len(reviews) != 2 {
				t.Errorf("maxDays=%d wiped reviews: got %d, want 2 (no-op expected)", maxDays, len(reviews))
			}
		})
	}
}

func TestComputeStats_CountsBasics(t *testing.T) {
	s := newTestStore(t)
	prID, err := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	for _, sev := range []string{"low", "low", "high"} {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Summary: "s", Issues: "[]", Suggestions: "[]", Severity: sev,
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("insert review: %v", err)
		}
	}

	stats, err := s.ComputeStats(nil, nil)
	if err != nil {
		t.Fatalf("ComputeStats: %v", err)
	}
	if stats.TotalReviews != 3 {
		t.Errorf("TotalReviews = %d, want 3", stats.TotalReviews)
	}
	if stats.BySeverity["low"] != 2 || stats.BySeverity["high"] != 1 {
		t.Errorf("BySeverity = %v, want low:2 high:1", stats.BySeverity)
	}
}

func TestComputeStats_AvgIssuesPerReview(t *testing.T) {
	s := newTestStore(t)
	prID, err := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	// Two reviews carrying 3 and 1 issues, plus one with none — avg over all 3
	// reviews is (3+1+0)/3 = 1.333…
	for _, issues := range []string{
		`[{"a":1},{"a":2},{"a":3}]`,
		`[{"a":1}]`,
		`[]`,
	} {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Summary: "s", Issues: issues, Suggestions: "[]", Severity: "low",
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("insert review: %v", err)
		}
	}

	stats, err := s.ComputeStats(nil, nil)
	if err != nil {
		t.Fatalf("ComputeStats: %v", err)
	}
	if want := 4.0 / 3.0; stats.AvgIssuesPerReview != want {
		t.Errorf("AvgIssuesPerReview = %v, want %v", stats.AvgIssuesPerReview, want)
	}
}

// Regression for #553: a DB error must surface through the error return rather
// than yielding zeroed/partial stats with a nil error. Closing the store makes
// every query fail, so ComputeStats must report it instead of returning ok.
func TestComputeStats_DBErrorPropagates(t *testing.T) {
	s := newTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	stats, err := s.ComputeStats(nil, nil)
	if err == nil {
		t.Fatalf("ComputeStats on closed DB = nil error (stats=%+v), want error", stats)
	}
	// The first query (total reviews) is what fails; assert the wrapped message
	// localizes it, so a future reorder of the queries is caught.
	if !strings.Contains(err.Error(), "total reviews") {
		t.Errorf("error = %q, want it to mention %q", err, "total reviews")
	}
}

func TestConfigs_ListReturnsAllRows(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.SetConfig("poll_interval", "30m"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := s.SetConfig("repositories", `["org/a","org/b"]`); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d: %v", len(got), got)
	}
	if got["poll_interval"] != "30m" {
		t.Errorf("poll_interval = %q, want 30m", got["poll_interval"])
	}
	if got["repositories"] != `["org/a","org/b"]` {
		t.Errorf("repositories = %q", got["repositories"])
	}
}

func TestConfigs_ListOnEmptyTableReturnsEmptyMap(t *testing.T) {
	s := newTestStore(t)

	got, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

// TestSetConfigs_PersistsAllKeysAtomically guards #565: a multi-key save must
// write every key in one transaction (all-or-nothing), so PUT /config can't
// leave the store in a partial state after a failure.
func TestSetConfigs_PersistsAllKeysAtomically(t *testing.T) {
	s := newTestStore(t)

	in := map[string]string{
		"poll_interval":  "30m",
		"review_mode":    "single",
		"retention_days": "90",
		"repositories":   `["org/a","org/b"]`,
	}
	if err := s.SetConfigs(in); err != nil {
		t.Fatalf("SetConfigs: %v", err)
	}

	got, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("expected %d rows, got %d: %v", len(in), len(got), got)
	}
	for k, want := range in {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

func TestSetConfigs_EmptyMapIsNoOp(t *testing.T) {
	s := newTestStore(t)

	if err := s.SetConfigs(nil); err != nil {
		t.Errorf("SetConfigs(nil): %v", err)
	}
	if err := s.SetConfigs(map[string]string{}); err != nil {
		t.Errorf("SetConfigs(empty): %v", err)
	}
	got, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty configs after no-op, got %v", got)
	}
}

// TestSetConfigs_SurfacesWriteFailure proves that when the batch cannot be
// committed, SetConfigs returns an error rather than silently succeeding —
// the signal handlePutConfig turns into a 500 instead of a misleading 200
// (#550). Atomicity itself (single BEGIN/COMMIT, rollback on any error) is
// structural in the implementation and exercised by the happy-path test above;
// a partial commit is impossible because every key shares one transaction.
func TestSetConfigs_SurfacesWriteFailure(t *testing.T) {
	s := newTestStore(t)

	// Close the DB so the transaction cannot begin/commit.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err := s.SetConfigs(map[string]string{
		"poll_interval":  "5m",
		"review_mode":    "multi",
		"retention_days": "7",
	})
	if err == nil {
		t.Fatal("expected error from SetConfigs on a closed store, got nil")
	}
}

// Activating a second agent for the SAME category must demote the first.
func TestStore_UpsertAgent_ActivationReplacesWithinCategory(t *testing.T) {
	s := newTestStore(t)

	if err := s.UpsertAgent(&store.Agent{ID: "old", Name: "old", IsDefaultPR: true}); err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	if err := s.UpsertAgent(&store.Agent{ID: "new", Name: "new", IsDefaultPR: true}); err != nil {
		t.Fatalf("upsert new: %v", err)
	}

	agents, err := s.ListAgents()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	active := 0
	for _, a := range agents {
		if a.IsDefaultPR {
			active++
			if a.ID != "new" {
				t.Errorf("expected `new` to be active, got %q", a.ID)
			}
		}
	}
	if active != 1 {
		t.Errorf("want exactly 1 IsDefaultPR agent, got %d", active)
	}
}

// Legacy rows with the old single `is_default=1` flag must seed the PR-review
// flag the first time the new code opens the DB — otherwise an upgrade would
// silently deactivate the user's only active agent.
func TestStore_Migration_SeedsPRFlagFromLegacyIsDefault(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// Simulate the old schema: CREATE TABLE without the per-category
	// columns, then INSERT a row where only the legacy `is_default` is on.
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE agents (
			id                     TEXT PRIMARY KEY,
			name                   TEXT NOT NULL,
			cli                    TEXT NOT NULL DEFAULT 'claude',
			prompt                 TEXT NOT NULL DEFAULT '',
			instructions           TEXT NOT NULL DEFAULT '',
			cli_flags              TEXT NOT NULL DEFAULT '',
			is_default             INTEGER NOT NULL DEFAULT 0,
			created_at             DATETIME NOT NULL,
			issue_prompt           TEXT NOT NULL DEFAULT '',
			issue_instructions     TEXT NOT NULL DEFAULT '',
			implement_prompt       TEXT NOT NULL DEFAULT '',
			implement_instructions TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO agents (id, name, is_default, created_at) VALUES ('legacy', 'L', 1, ?)`,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	// Also insert a non-active legacy row to verify it doesn't get activated.
	if _, err := legacy.Exec(
		`INSERT INTO agents (id, name, is_default, created_at) VALUES ('other', 'O', 0, ?)`,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert other row: %v", err)
	}
	legacy.Close()

	// Re-open with the current migration code — ALTER TABLE adds
	// is_default_pr and seeds it from `is_default`.
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	agents, err := s.ListAgents()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]*store.Agent{}
	for _, a := range agents {
		byID[a.ID] = a
	}

	if !byID["legacy"].IsDefaultPR {
		t.Error("legacy agent: IsDefaultPR = false, want true after seed")
	}
	if byID["other"].IsDefaultPR {
		t.Error("other agent: IsDefaultPR = true, want false (was legacy is_default=0)")
	}
}

// DefaultAgentFor returns the active review agent, and an error — not some
// other agent — when none is active.
func TestStore_DefaultAgentFor_ReturnsActiveAgent(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DefaultAgentFor(store.AgentCategoryPR); err == nil {
		t.Fatal("DefaultAgentFor(pr) with no agents: want error, got nil")
	}
	if err := s.UpsertAgent(&store.Agent{ID: "idle", Name: "idle"}); err != nil {
		t.Fatalf("upsert idle: %v", err)
	}
	if err := s.UpsertAgent(&store.Agent{ID: "pr-only", Name: "pr", IsDefaultPR: true}); err != nil {
		t.Fatalf("upsert pr-only: %v", err)
	}

	got, err := s.DefaultAgentFor(store.AgentCategoryPR)
	if err != nil || got == nil || got.ID != "pr-only" {
		t.Errorf("DefaultAgentFor(pr) = %+v, err=%v; want pr-only", got, err)
	}
	if _, err := s.DefaultAgentFor(store.AgentCategory("issue")); err == nil {
		t.Error("DefaultAgentFor(issue): want unknown-category error, got nil")
	}
}

// Databases created while Heimdallm still ran the issue pipelines must come
// out of Open with that data gone and everything else untouched.
func TestStore_Open_DropsLegacyIssuePipelineData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE issues (id INTEGER PRIMARY KEY, github_id INTEGER, repo TEXT)`,
		`CREATE TABLE issue_reviews (id INTEGER PRIMARY KEY, issue_id INTEGER)`,
		`CREATE TABLE issue_triage_in_flight (issue_id INTEGER, updated_at TEXT, started_at DATETIME)`,
		`INSERT INTO issues (id, github_id, repo) VALUES (1, 10, 'org/repo')`,
		`CREATE TABLE watch_state (key TEXT PRIMARY KEY, type TEXT NOT NULL, repo TEXT NOT NULL,
			number INTEGER NOT NULL, github_id INTEGER NOT NULL, next_check TEXT NOT NULL,
			backoff_ns INTEGER NOT NULL, last_seen TEXT NOT NULL)`,
		`INSERT INTO watch_state VALUES ('issue.10', 'issue', 'org/repo', 7, 10, '', 0, '')`,
		`INSERT INTO watch_state VALUES ('pr.20', 'pr', 'org/repo', 1, 20, '', 0, '')`,
		`CREATE TABLE configs (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO configs VALUES ('issue_tracking', '{"enabled":true}')`,
		`INSERT INTO configs VALUES ('poll_interval', '5m')`,
	} {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatalf("seed legacy %q: %v", stmt, err)
		}
	}
	legacy.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// The removal is permanent, so it must be visible in the log with what
	// it deleted — and only when something was actually there.
	got := logs.String()
	for _, want := range []string{
		"removed data of the retired issue pipeline",
		"issues=1", "issue_reviews=0", "watch_state_rows=1", "config_rows=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("upgrade log missing %q:\n%s", want, got)
		}
	}
	logs.Reset()
	again, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	again.Close()
	if strings.Contains(logs.String(), "retired issue pipeline") {
		t.Errorf("an already-clean database must not log a removal:\n%s", logs.String())
	}

	for _, table := range []string{"issues", "issue_reviews", "issue_triage_in_flight"} {
		var n int
		if err := s.DB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&n); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("legacy table %s still exists after Open", table)
		}
	}
	var watchTypes []string
	rows, err := s.DB().Query(`SELECT type FROM watch_state ORDER BY key`)
	if err != nil {
		t.Fatalf("list watch_state: %v", err)
	}
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatalf("scan watch_state: %v", err)
		}
		watchTypes = append(watchTypes, typ)
	}
	rows.Close()
	if len(watchTypes) != 1 || watchTypes[0] != "pr" {
		t.Errorf("watch_state types = %v, want only the PR row", watchTypes)
	}
	cfgRows, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("list configs: %v", err)
	}
	if _, ok := cfgRows["issue_tracking"]; ok {
		t.Error("stale issue_tracking config row survived Open")
	}
	if cfgRows["poll_interval"] != "5m" {
		t.Errorf("poll_interval config row = %q, want it untouched", cfgRows["poll_interval"])
	}
}

// ---------------------------------------------------------------------------
// State-filter tests for PRs
// ---------------------------------------------------------------------------

func TestListPRs_StateFilter(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	_, err := s.UpsertPR(&store.PR{GithubID: 301, Repo: "org/r", Number: 301, Title: "open PR", Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now})
	if err != nil {
		t.Fatalf("upsert open: %v", err)
	}
	_, err = s.UpsertPR(&store.PR{GithubID: 302, Repo: "org/r", Number: 302, Title: "closed PR", Author: "a", URL: "u", State: "closed", UpdatedAt: now, FetchedAt: now})
	if err != nil {
		t.Fatalf("upsert closed: %v", err)
	}

	all, err := s.ListPRs()
	if err != nil {
		t.Fatalf("ListPRs() all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListPRs() = %d, want 2", len(all))
	}

	open, err := s.ListPRs("open")
	if err != nil {
		t.Fatalf("ListPRs(open): %v", err)
	}
	if len(open) != 1 {
		t.Errorf("ListPRs(open) = %d, want 1", len(open))
	}
	if open[0].State != "open" {
		t.Errorf("ListPRs(open)[0].State = %q, want open", open[0].State)
	}

	closed, err := s.ListPRs("closed")
	if err != nil {
		t.Fatalf("ListPRs(closed): %v", err)
	}
	if len(closed) != 1 {
		t.Errorf("ListPRs(closed) = %d, want 1", len(closed))
	}
	if closed[0].State != "closed" {
		t.Errorf("ListPRs(closed)[0].State = %q, want closed", closed[0].State)
	}
}

func TestUpdatePRState(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	id, err := s.UpsertPR(&store.PR{GithubID: 303, Repo: "org/r", Number: 303, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.UpdatePRState(id, "closed"); err != nil {
		t.Fatalf("UpdatePRState: %v", err)
	}

	closed, err := s.ListPRs("closed")
	if err != nil {
		t.Fatalf("ListPRs(closed): %v", err)
	}
	if len(closed) != 1 || closed[0].ID != id {
		t.Errorf("expected 1 closed PR with id=%d, got %v", id, closed)
	}
	if closed[0].State != "closed" {
		t.Errorf("State = %q, want closed", closed[0].State)
	}
}

func TestUpdatePRStateByGithubID(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	id, err := s.UpsertPR(&store.PR{GithubID: 304, Repo: "org/r", Number: 304, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.UpdatePRStateByGithubID(304, "closed"); err != nil {
		t.Fatalf("UpdatePRStateByGithubID: %v", err)
	}

	closed, err := s.ListPRs("closed")
	if err != nil {
		t.Fatalf("ListPRs(closed): %v", err)
	}
	if len(closed) != 1 || closed[0].ID != id {
		t.Errorf("expected 1 closed PR with id=%d, got %v", id, closed)
	}
	if closed[0].GithubID != 304 {
		t.Errorf("GithubID = %d, want 304", closed[0].GithubID)
	}
}

// ---------------------------------------------------------------------------
// State-filter tests for Issues
// ---------------------------------------------------------------------------
