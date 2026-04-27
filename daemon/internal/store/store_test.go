package store_test

import (
	"database/sql"
	"path/filepath"
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

func TestStore_AgentImplementFieldsRoundTrip(t *testing.T) {
	s := newTestStore(t)

	in := &store.Agent{
		ID:                    "go-impl",
		Name:                  "Go implementer",
		CLI:                   "claude",
		ImplementPrompt:       "custom full template for implementation",
		ImplementInstructions: "use go 1.22 generics where helpful",
	}
	if err := s.UpsertAgent(in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.ListAgents()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 agent, got %d", len(got))
	}
	if got[0].ImplementPrompt != in.ImplementPrompt {
		t.Errorf("ImplementPrompt = %q, want %q", got[0].ImplementPrompt, in.ImplementPrompt)
	}
	if got[0].ImplementInstructions != in.ImplementInstructions {
		t.Errorf("ImplementInstructions = %q, want %q", got[0].ImplementInstructions, in.ImplementInstructions)
	}
}

// Activating an agent for one category MUST NOT touch the active flags of
// the other two — this is the whole point of splitting the single is_default
// into three per-category flags.
func TestStore_UpsertAgent_ActivationIsPerCategory(t *testing.T) {
	s := newTestStore(t)

	// Seed a PR-review-active agent and an issue-triage-active agent.
	if err := s.UpsertAgent(&store.Agent{ID: "a", Name: "A", IsDefaultPR: true}); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	if err := s.UpsertAgent(&store.Agent{ID: "b", Name: "B", IsDefaultIssue: true}); err != nil {
		t.Fatalf("upsert b: %v", err)
	}

	// Activate a new dev-only agent. Neither A (PR) nor B (issue) should flip.
	if err := s.UpsertAgent(&store.Agent{ID: "c", Name: "C", IsDefaultDev: true}); err != nil {
		t.Fatalf("upsert c: %v", err)
	}

	byID := map[string]*store.Agent{}
	agents, err := s.ListAgents()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, a := range agents {
		byID[a.ID] = a
	}
	if !byID["a"].IsDefaultPR || byID["a"].IsDefaultIssue || byID["a"].IsDefaultDev {
		t.Errorf("agent a: got (pr=%v issue=%v dev=%v), want (true false false)",
			byID["a"].IsDefaultPR, byID["a"].IsDefaultIssue, byID["a"].IsDefaultDev)
	}
	if byID["b"].IsDefaultPR || !byID["b"].IsDefaultIssue || byID["b"].IsDefaultDev {
		t.Errorf("agent b: got (pr=%v issue=%v dev=%v), want (false true false)",
			byID["b"].IsDefaultPR, byID["b"].IsDefaultIssue, byID["b"].IsDefaultDev)
	}
	if byID["c"].IsDefaultPR || byID["c"].IsDefaultIssue || !byID["c"].IsDefaultDev {
		t.Errorf("agent c: got (pr=%v issue=%v dev=%v), want (false false true)",
			byID["c"].IsDefaultPR, byID["c"].IsDefaultIssue, byID["c"].IsDefaultDev)
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

// Legacy rows with the old single `is_default=1` flag must seed all three
// per-category flags the first time the new code opens the DB — otherwise
// an upgrade would silently deactivate the user's only active agent.
func TestStore_Migration_SeedsPerCategoryFlagsFromLegacyIsDefault(t *testing.T) {
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

	// Re-open with the current migration code — ALTER TABLE adds the three
	// new columns and seeds each from `is_default`.
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

	if !byID["legacy"].IsDefaultPR || !byID["legacy"].IsDefaultIssue || !byID["legacy"].IsDefaultDev {
		t.Errorf("legacy agent: got (pr=%v issue=%v dev=%v), want (true true true) after seed",
			byID["legacy"].IsDefaultPR, byID["legacy"].IsDefaultIssue, byID["legacy"].IsDefaultDev)
	}
	if byID["other"].IsDefaultPR || byID["other"].IsDefaultIssue || byID["other"].IsDefaultDev {
		t.Errorf("other agent: got (pr=%v issue=%v dev=%v), want all false (was legacy is_default=0)",
			byID["other"].IsDefaultPR, byID["other"].IsDefaultIssue, byID["other"].IsDefaultDev)
	}
}

// DefaultAgentFor returns the agent active for the requested category and
// ignores agents active in a different category.
func TestStore_DefaultAgentFor_ReturnsPerCategoryAgent(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertAgent(&store.Agent{ID: "pr-only", Name: "pr", IsDefaultPR: true}); err != nil {
		t.Fatalf("upsert pr-only: %v", err)
	}
	if err := s.UpsertAgent(&store.Agent{ID: "issue-only", Name: "issue", IsDefaultIssue: true}); err != nil {
		t.Fatalf("upsert issue-only: %v", err)
	}

	got, err := s.DefaultAgentFor(store.AgentCategoryPR)
	if err != nil || got == nil || got.ID != "pr-only" {
		t.Errorf("DefaultAgentFor(pr) = %+v, err=%v; want pr-only", got, err)
	}
	got, err = s.DefaultAgentFor(store.AgentCategoryIssue)
	if err != nil || got == nil || got.ID != "issue-only" {
		t.Errorf("DefaultAgentFor(issue) = %+v, err=%v; want issue-only", got, err)
	}
	// No agent is dev-default — should return an error (ErrNoRows), not one
	// of the other two.
	if got, err := s.DefaultAgentFor(store.AgentCategoryDev); err == nil {
		t.Errorf("DefaultAgentFor(dev) = %+v, want error for no-match", got)
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

func TestListIssues_StateFilter(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	openIssue := &store.Issue{GithubID: 401, Repo: "org/r", Number: 401, Title: "open issue", Author: "a", State: "open", CreatedAt: now, FetchedAt: now}
	closedIssue := &store.Issue{GithubID: 402, Repo: "org/r", Number: 402, Title: "closed issue", Author: "a", State: "closed", CreatedAt: now, FetchedAt: now}

	if _, err := s.UpsertIssue(openIssue); err != nil {
		t.Fatalf("upsert open: %v", err)
	}
	if _, err := s.UpsertIssue(closedIssue); err != nil {
		t.Fatalf("upsert closed: %v", err)
	}

	all, err := s.ListIssues()
	if err != nil {
		t.Fatalf("ListIssues() all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListIssues() = %d, want 2", len(all))
	}

	open, err := s.ListIssues("open")
	if err != nil {
		t.Fatalf("ListIssues(open): %v", err)
	}
	if len(open) != 1 {
		t.Errorf("ListIssues(open) = %d, want 1", len(open))
	}
	if open[0].State != "open" {
		t.Errorf("ListIssues(open)[0].State = %q, want open", open[0].State)
	}

	closed, err := s.ListIssues("closed")
	if err != nil {
		t.Fatalf("ListIssues(closed): %v", err)
	}
	if len(closed) != 1 {
		t.Errorf("ListIssues(closed) = %d, want 1", len(closed))
	}
	if closed[0].State != "closed" {
		t.Errorf("ListIssues(closed)[0].State = %q, want closed", closed[0].State)
	}
}

func TestUpdateIssueState(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	id, err := s.UpsertIssue(&store.Issue{GithubID: 403, Repo: "org/r", Number: 403, Title: "t", Author: "a", State: "open", CreatedAt: now, FetchedAt: now})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.UpdateIssueState(id, "closed"); err != nil {
		t.Fatalf("UpdateIssueState: %v", err)
	}

	closed, err := s.ListIssues("closed")
	if err != nil {
		t.Fatalf("ListIssues(closed): %v", err)
	}
	if len(closed) != 1 || closed[0].ID != id {
		t.Errorf("expected 1 closed issue with id=%d, got %v", id, closed)
	}
	if closed[0].State != "closed" {
		t.Errorf("State = %q, want closed", closed[0].State)
	}
}

func TestUpdateIssueStateByGithubID(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	id, err := s.UpsertIssue(&store.Issue{GithubID: 404, Repo: "org/r", Number: 404, Title: "t", Author: "a", State: "open", CreatedAt: now, FetchedAt: now})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.UpdateIssueStateByGithubID(404, "closed"); err != nil {
		t.Fatalf("UpdateIssueStateByGithubID: %v", err)
	}

	closed, err := s.ListIssues("closed")
	if err != nil {
		t.Fatalf("ListIssues(closed): %v", err)
	}
	if len(closed) != 1 || closed[0].ID != id {
		t.Errorf("expected 1 closed issue with id=%d, got %v", id, closed)
	}
	if closed[0].GithubID != 404 {
		t.Errorf("GithubID = %d, want 404", closed[0].GithubID)
	}
}
