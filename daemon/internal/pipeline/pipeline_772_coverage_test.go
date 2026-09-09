package pipeline

import (
	"os"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

// These tests exercise two defensive branches added for #772 that are
// unreachable through the public Run()/PublishPending() API (Run's own
// gating already guarantees the inputs these branches guard against never
// occur when called from there), following the same internal-package,
// direct-call pattern already used elsewhere in this package for exactly
// this purpose — see feedback_format_coverage_test.go.

// TestReviewWasReanchoredToHead_NilPrevReview covers the early-return guard:
// a nil prevReview, a non-positive GitHubReviewID, or an empty pr.Head.SHA
// all mean there is nothing to check a reanchor against. Run's own gate
// (prevReview != nil && pr.Head.SHA != "") makes this unreachable via Run,
// but the function must still fail safely if ever called otherwise.
func TestReviewWasReanchoredToHead_NilPrevReview(t *testing.T) {
	p := &Pipeline{}
	pr := &github.PullRequest{Repo: "org/repo", Number: 1, Head: github.Branch{SHA: "deadbeef"}}
	if got := p.reviewWasReanchoredToHead(pr, nil); got {
		t.Error("reviewWasReanchoredToHead(nil prevReview) = true, want false")
	}
}

// TestPersistPeerCoveredReview_InsertFailureIsLoggedAndSwallowed covers the
// InsertReview failure branch: persistPeerCoveredReview must not panic when
// the placeholder write fails, since the pre-generation peer check has
// already decided to skip regardless — only the convergence bookkeeping is
// at stake. Forces the failure via a read-only database file rather than a
// store double, because store.Store's underlying *sql.DB is unexported and
// unreachable from this package's own tests.
func TestPersistPeerCoveredReview_InsertFailureIsLoggedAndSwallowed(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	prID, err := s.UpsertPR(&store.PR{
		GithubID: 5001, Repo: "org/repo", Number: 5001, Title: "t",
		Author: "alice", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}

	if err := os.Chmod(dbPath, 0o444); err != nil {
		t.Fatalf("chmod db: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	p := &Pipeline{store: s}

	// Must not panic; the only observable effect of a failed insert is a log
	// line, which this test cannot capture without a slog handler — the
	// non-panic itself, plus the read-only file actually rejecting the
	// write (proven directly below), is what this test guards.
	p.persistPeerCoveredReview(prID, "deadbeef", github.PRReview{ID: 999, State: "APPROVED", User: github.User{Login: "bot"}})

	if _, err := s.InsertReview(&store.Review{PRID: prID, Issues: "[]", Suggestions: "[]", CreatedAt: time.Now()}); err == nil {
		t.Fatal("test setup broken: InsertReview succeeded on a chmod'd read-only database file")
	}
}
