package issues_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/store"
)

// Integration test that wires a real *store.Store to both the Fetcher and a
// real *Pipeline, with fakes only for the network-facing edges (GitHub and
// the CLI executor). Covers the end-to-end flow the reviewers wanted an
// integration-level guard on.
func TestIntegration_FetcherDrivesPipelineEndToEnd(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Two issues: one plain review_only, one classified develop but without a
	// working directory — the pipeline must downgrade it to review_only and
	// both should still end up triaged.
	now := time.Now().UTC().Truncate(time.Second)
	reviewOnly := &github.Issue{
		ID: 1001, Number: 1, Repo: "org/repo",
		Title: "Needs triage", Body: "Please look at this.",
		State: "open", User: github.User{Login: "reporter"},
		Labels:    []github.Label{{Name: "question"}},
		Assignees: []github.User{},
		CreatedAt: now, UpdatedAt: now,
		Mode: config.IssueModeReviewOnly,
	}
	developFallback := &github.Issue{
		ID: 1002, Number: 2, Repo: "org/repo",
		Title: "Fix crash", Body: "Null pointer in auth.",
		State: "open", User: github.User{Login: "reporter"},
		Labels:    []github.Label{{Name: "bug"}},
		Assignees: []github.User{},
		CreatedAt: now, UpdatedAt: now,
		Mode: config.IssueModeDevelop, // no WorkDir in RunOptions → fallback
	}

	client := &fakeClient{issues: []*github.Issue{reviewOnly, developFallback}}
	gh := &fakeGH{}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte(validResult)}
	broker := &fakeBroker{}

	pipe := issues.New(s, gh, exec, nil, broker, nil)
	fetcher := issues.NewFetcher(client, s, pipe)

	processed, err := fetcher.ProcessRepo(
		"org/repo",
		config.IssueTrackingConfig{Enabled: true},
		"reporter",
		func(_ *github.Issue) issues.RunOptions { return issues.RunOptions{Primary: "claude"} },
	)
	if err != nil {
		t.Fatalf("ProcessRepo: %v", err)
	}
	if processed != 2 {
		t.Fatalf("expected both issues processed (fallback preserves processing), got %d", processed)
	}

	// Store has both issues + both reviews.
	list, err := s.ListIssues()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 issues in store, got %d", len(list))
	}
	for _, row := range list {
		latest, err := s.LatestIssueReview(row.ID)
		if err != nil {
			t.Fatalf("latest review for issue %d: %v", row.ID, err)
		}
		if latest.ActionTaken != string(config.IssueModeReviewOnly) {
			t.Errorf("issue %d action_taken=%q, want review_only (incl. fallback)",
				row.Number, latest.ActionTaken)
		}
	}

	// Both comments landed on GitHub.
	if len(gh.postCalls) != 2 {
		t.Errorf("expected 2 PostComment calls, got %d", len(gh.postCalls))
	}

	// Second pass immediately after: the grace window should skip both.
	processed2, err := fetcher.ProcessRepo(
		"org/repo",
		config.IssueTrackingConfig{Enabled: true},
		"reporter",
		func(_ *github.Issue) issues.RunOptions { return issues.RunOptions{Primary: "claude"} },
	)
	if err != nil {
		t.Fatalf("second ProcessRepo: %v", err)
	}
	if processed2 != 0 {
		t.Errorf("dedup: expected 0 re-processed within grace, got %d", processed2)
	}
	if len(gh.postCalls) != 2 {
		t.Errorf("dedup: expected no new PostComment calls, got %d total", len(gh.postCalls))
	}
}

func TestIntegration_RecomputeGraceIsExportedForMainPipeline(t *testing.T) {
	// The constant is exported specifically so main.go in #28 can align the
	// PR pipeline's grace window with the issue fetcher's. Locking the value
	// (and its exported status) here prevents an accidental private rename
	// from silently drifting the two pipelines apart.
	if issues.RecomputeGrace != 30*time.Second {
		t.Errorf("RecomputeGrace changed to %v — confirm with the PR pipeline's grace value in main.go before updating",
			issues.RecomputeGrace)
	}
}
