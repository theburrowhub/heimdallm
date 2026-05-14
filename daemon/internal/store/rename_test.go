package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

// seedForRename inserts one PR, one issue, one activity row, and one
// watch_state row under `repo`. Returns the store ready for a rename
// invocation. Used by every test in this file.
func seedForRename(t *testing.T, repo string, githubBase int64) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if _, err := s.UpsertPR(&store.PR{
		GithubID:  githubBase,
		Repo:      repo,
		Number:    1,
		Title:     "pr",
		Author:    "alice",
		URL:       "https://x/" + repo + "/pull/1",
		State:     "open",
		UpdatedAt: time.Now().UTC(),
		FetchedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed pr: %v", err)
	}

	if _, err := s.UpsertIssue(&store.Issue{
		GithubID:  githubBase + 100,
		Repo:      repo,
		Number:    7,
		Title:     "issue",
		Author:    "bob",
		State:     "open",
		CreatedAt: time.Now().UTC(),
		FetchedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	if _, err := s.InsertActivity(
		time.Now().UTC().Truncate(time.Second),
		"acme", repo, "pr", 1, "pr", "review", "", nil,
	); err != nil {
		t.Fatalf("seed activity: %v", err)
	}

	// watch_state row. Insert directly via DB() — RenameRepo's contract
	// is to update existing rows, not to know how callers created them.
	if _, err := s.DB().Exec(`
		INSERT INTO watch_state (key, type, repo, number, github_id, next_check, backoff_ns, last_seen)
		VALUES (?, 'pr', ?, 1, ?, ?, 0, ?)`,
		"pr."+itoa(githubBase), repo, githubBase,
		time.Now().Format(time.RFC3339Nano),
		time.Now().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("seed watch_state: %v", err)
	}

	return s
}

func itoa(n int64) string {
	// strconv.FormatInt would pull in another import for one call; the
	// seeded values are small positives so a hand-rolled formatter keeps
	// the test self-contained.
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func countWithRepo(t *testing.T, s *store.Store, table, repo string) int {
	t.Helper()
	var n int
	q := "SELECT COUNT(*) FROM " + table + " WHERE repo = ?"
	if err := s.DB().QueryRow(q, repo).Scan(&n); err != nil {
		t.Fatalf("count %s where repo=%s: %v", table, repo, err)
	}
	return n
}

func TestStore_RenameRepo_UpdatesAllTables(t *testing.T) {
	s := seedForRename(t, "acme/old", 1000)

	if _, err := s.RenameRepo("acme/old", "acme/new"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	for _, table := range []string{"prs", "issues", "activity_log", "watch_state"} {
		if got := countWithRepo(t, s, table, "acme/new"); got != 1 {
			t.Errorf("%s: want 1 row with new slug, got %d", table, got)
		}
		if got := countWithRepo(t, s, table, "acme/old"); got != 0 {
			t.Errorf("%s: want 0 rows with old slug, got %d", table, got)
		}
	}
}

func TestStore_RenameRepo_LeavesUnrelatedRowsUntouched(t *testing.T) {
	s := seedForRename(t, "acme/old", 1000)

	// Add a second PR + activity row in a different repo. These must
	// stay put regardless of what RenameRepo does to the acme/old rows.
	if _, err := s.UpsertPR(&store.PR{
		GithubID:  2000,
		Repo:      "globex/other",
		Number:    99,
		Title:     "pr",
		Author:    "x",
		URL:       "https://x",
		State:     "open",
		UpdatedAt: time.Now().UTC(),
		FetchedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed unrelated pr: %v", err)
	}
	if _, err := s.InsertActivity(
		time.Now().UTC().Truncate(time.Second),
		"globex", "globex/other", "pr", 99, "x", "review", "", nil,
	); err != nil {
		t.Fatalf("seed unrelated activity: %v", err)
	}

	if _, err := s.RenameRepo("acme/old", "acme/new"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if got := countWithRepo(t, s, "prs", "globex/other"); got != 1 {
		t.Errorf("unrelated PR was touched: want 1 row for globex/other, got %d", got)
	}
	if got := countWithRepo(t, s, "activity_log", "globex/other"); got != 1 {
		t.Errorf("unrelated activity was touched: want 1, got %d", got)
	}
}

func TestStore_RenameRepo_InsertsAuditRow(t *testing.T) {
	s := seedForRename(t, "acme/old", 1000)

	if _, err := s.RenameRepo("acme/old", "acme/new"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	var (
		oldRepo, newRepo string
		ts               string
	)
	row := s.DB().QueryRow(
		"SELECT old_repo, new_repo, renamed_at FROM repo_renames ORDER BY id DESC LIMIT 1",
	)
	if err := row.Scan(&oldRepo, &newRepo, &ts); err != nil {
		t.Fatalf("scan audit: %v", err)
	}
	if oldRepo != "acme/old" || newRepo != "acme/new" {
		t.Errorf("audit pair = (%q, %q); want (acme/old, acme/new)", oldRepo, newRepo)
	}
	if ts == "" {
		t.Error("audit timestamp empty")
	}
}

func TestStore_RenameRepo_IsAtomic_OnError(t *testing.T) {
	s := seedForRename(t, "acme/old", 1000)

	// Force the UPDATE on activity_log to fail mid-TX by dropping the
	// table before the call. With RenameRepo running everything inside
	// a single SQLite transaction, the prs / issues UPDATEs that come
	// before activity_log must roll back when the missing-table error
	// fires.
	if _, err := s.DB().Exec("DROP TABLE activity_log"); err != nil {
		t.Fatalf("drop activity_log: %v", err)
	}

	_, err := s.RenameRepo("acme/old", "acme/new")
	if err == nil {
		t.Fatal("expected error from RenameRepo when activity_log is missing")
	}

	// prs and issues must NOT have been advanced to acme/new.
	for _, table := range []string{"prs", "issues", "watch_state"} {
		if got := countWithRepo(t, s, table, "acme/new"); got != 0 {
			t.Errorf("%s: TX leaked — want 0 new-slug rows after rollback, got %d", table, got)
		}
		if got := countWithRepo(t, s, table, "acme/old"); got != 1 {
			t.Errorf("%s: TX leaked — want 1 old-slug row preserved after rollback, got %d", table, got)
		}
	}

	// Audit row must also have rolled back.
	var n int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM repo_renames").Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 0 {
		t.Errorf("audit row leaked — want 0, got %d", n)
	}
}

// TestStore_RenameRepo_HandlesRenameBackChain pins the chain-convergence
// requirement from issue #489 ("A rename followed by a rename-back ...
// must converge correctly"). The previous audit-table-only idempotency
// check returned applied=false on the third leg (A→B after a B→A round
// trip), leaving DB rows on A while the reconciler advanced the config
// to B — silent drift.
func TestStore_RenameRepo_HandlesRenameBackChain(t *testing.T) {
	s := seedForRename(t, "acme/old", 1000)

	step := func(oldR, newR string, wantApplied bool) {
		t.Helper()
		applied, err := s.RenameRepo(oldR, newR)
		if err != nil {
			t.Fatalf("RenameRepo(%s, %s): %v", oldR, newR, err)
		}
		if applied != wantApplied {
			t.Errorf("RenameRepo(%s, %s) applied = %v, want %v", oldR, newR, applied, wantApplied)
		}
	}
	stateAt := func(repo string) int {
		t.Helper()
		return countWithRepo(t, s, "prs", repo) +
			countWithRepo(t, s, "issues", repo) +
			countWithRepo(t, s, "activity_log", repo) +
			countWithRepo(t, s, "watch_state", repo)
	}

	// Forward: rows move to acme/new.
	step("acme/old", "acme/new", true)
	if got := stateAt("acme/old"); got != 0 {
		t.Errorf("after A→B: %d rows still at acme/old, want 0", got)
	}
	if got := stateAt("acme/new"); got == 0 {
		t.Errorf("after A→B: 0 rows at acme/new, want > 0")
	}

	// Back: rows return to acme/old.
	step("acme/new", "acme/old", true)
	if got := stateAt("acme/new"); got != 0 {
		t.Errorf("after B→A: %d rows still at acme/new, want 0", got)
	}
	if got := stateAt("acme/old"); got == 0 {
		t.Errorf("after B→A: 0 rows at acme/old, want > 0")
	}

	// Forward again: this is the leg the old guard mishandled.
	// MUST return applied=true and actually move the rows.
	step("acme/old", "acme/new", true)
	if got := stateAt("acme/old"); got != 0 {
		t.Errorf("after A→B (round 2): %d rows still at acme/old — rename-back chain broken", got)
	}
	if got := stateAt("acme/new"); got == 0 {
		t.Errorf("after A→B (round 2): 0 rows at acme/new, want > 0")
	}

	// A fourth A→B with no rows to move is the natural no-op.
	step("acme/old", "acme/new", false)
}

// TestStore_RenameRepo_UpdatesActivityLogOrgOnOrgRename pins the org
// column maintenance for org-level renames (old-org/repo →
// new-org/repo). The ListActivity org filter (activity.go) keys on
// the org column, so leaving it stale silently hides historical
// rows from the post-rename org's view.
func TestStore_RenameRepo_UpdatesActivityLogOrgOnOrgRename(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	ts := time.Now().UTC().Truncate(time.Second)
	if _, err := s.InsertActivity(ts, "old-org", "old-org/api", "pr", 1, "x", "review", "", nil); err != nil {
		t.Fatalf("seed activity: %v", err)
	}

	if _, err := s.RenameRepo("old-org/api", "new-org/api"); err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}

	// repo column must reflect the new slug.
	if got := countWithRepo(t, s, "activity_log", "new-org/api"); got != 1 {
		t.Errorf("activity_log row not renamed: %d rows at new-org/api, want 1", got)
	}
	// org column must also reflect the new org — otherwise the
	// ListActivity Orgs filter loses this row.
	var org string
	if err := s.DB().QueryRow(
		"SELECT org FROM activity_log WHERE repo = ?", "new-org/api",
	).Scan(&org); err != nil {
		t.Fatalf("scan org: %v", err)
	}
	if org != "new-org" {
		t.Errorf("activity_log.org = %q, want new-org — UI filter by new-org would lose this row", org)
	}
}

// TestStore_RenameRepo_PreservesActivityOrgOnSameOrgRename pins the
// other half: a within-org rename keeps the org column untouched
// (newOrg == oldOrg), and other rows under that org are unaffected.
func TestStore_RenameRepo_PreservesActivityOrgOnSameOrgRename(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	ts := time.Now().UTC().Truncate(time.Second)
	for _, row := range []struct{ repo string }{
		{"acme/old"},
		{"acme/sibling"}, // must NOT be touched
	} {
		if _, err := s.InsertActivity(ts, "acme", row.repo, "pr", 1, "x", "review", "", nil); err != nil {
			t.Fatalf("seed activity for %s: %v", row.repo, err)
		}
	}

	if _, err := s.RenameRepo("acme/old", "acme/new"); err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}

	var org string
	if err := s.DB().QueryRow(
		"SELECT org FROM activity_log WHERE repo = ?", "acme/new",
	).Scan(&org); err != nil {
		t.Fatalf("scan org for renamed row: %v", err)
	}
	if org != "acme" {
		t.Errorf("renamed row org = %q, want acme", org)
	}
	// Sibling row in the same org must remain untouched.
	if got := countWithRepo(t, s, "activity_log", "acme/sibling"); got != 1 {
		t.Errorf("sibling row was touched: count = %d, want 1", got)
	}
}

// TestStore_RenameRepo_AuditsRenameOnEmptyState pins the edge case
// flagged in review: a repo configured on the daemon may be renamed
// on GitHub BEFORE any PRs/issues/activity rows have accumulated
// under it. The UPDATEs match zero rows in that case, but the
// rename still happened from the reconciler's point of view and the
// issue/PR contract requires the mapping to land in repo_renames so
// the historical record survives restarts.
func TestStore_RenameRepo_AuditsRenameOnEmptyState(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// First call on empty state: no SQLite rows for either slug.
	applied, err := s.RenameRepo("acme/empty", "acme/renamed")
	if err != nil {
		t.Fatalf("RenameRepo on empty state: %v", err)
	}
	if !applied {
		t.Error("applied=false on first rename of an empty-state repo — caller treats this as a recovery path, but it is a fresh rename that just lacks SQLite rows")
	}

	count := func() int {
		t.Helper()
		var n int
		if err := s.DB().QueryRow(
			"SELECT COUNT(*) FROM repo_renames WHERE old_repo = ? AND new_repo = ?",
			"acme/empty", "acme/renamed",
		).Scan(&n); err != nil {
			t.Fatalf("count audit: %v", err)
		}
		return n
	}
	if got := count(); got != 1 {
		t.Errorf("repo_renames row count = %d, want 1 — empty-state rename must persist the mapping per #489", got)
	}

	// Retry with the same pair: audit row already records the
	// mapping, so this is a true no-op. applied=false, no duplicate
	// audit row.
	applied, err = s.RenameRepo("acme/empty", "acme/renamed")
	if err != nil {
		t.Fatalf("RenameRepo retry: %v", err)
	}
	if applied {
		t.Error("applied=true on duplicate retry — the mapping is already audited and there is no new state to record")
	}
	if got := count(); got != 1 {
		t.Errorf("retry duplicated audit row: count = %d, want 1", got)
	}
}

func TestStore_RenameRepo_Idempotent(t *testing.T) {
	s := seedForRename(t, "acme/old", 1000)

	applied1, err := s.RenameRepo("acme/old", "acme/new")
	if err != nil {
		t.Fatalf("rename #1: %v", err)
	}
	if !applied1 {
		t.Error("first rename: applied=false, want true")
	}
	applied2, err := s.RenameRepo("acme/old", "acme/new")
	if err != nil {
		t.Fatalf("rename #2 (should be no-op): %v", err)
	}
	if applied2 {
		t.Error("second rename: applied=true, want false (idempotent no-op)")
	}

	var n int
	if err := s.DB().QueryRow(
		"SELECT COUNT(*) FROM repo_renames WHERE old_repo = ? AND new_repo = ?",
		"acme/old", "acme/new",
	).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 1 {
		t.Errorf("idempotency broken: want 1 audit row, got %d", n)
	}
}
