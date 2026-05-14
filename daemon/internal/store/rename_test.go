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
