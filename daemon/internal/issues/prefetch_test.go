package issues_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
)

// ── fakeSearcher ─────────────────────────────────────────────────────────────

type fakeSearcher struct {
	issues []*github.Issue
	err    error
	calls  int
}

func (s *fakeSearcher) SearchIssues(_ string) ([]*github.Issue, error) {
	s.calls++
	return s.issues, s.err
}

// ── helpers ───────────────────────────────────────────────────────────────────

func makeIssue(id int64, number int, repo string, labels []string, assignees []string) *github.Issue {
	lbls := make([]github.Label, len(labels))
	for i, l := range labels {
		lbls[i] = github.Label{Name: l}
	}
	asgs := make([]github.User, len(assignees))
	for i, a := range assignees {
		asgs[i] = github.User{Login: a}
	}
	return &github.Issue{
		ID:        id,
		Number:    number,
		Title:     "issue",
		State:     "open",
		Repo:      repo,
		Labels:    lbls,
		Assignees: asgs,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func searchCfg() config.IssueTrackingConfig {
	return config.IssueTrackingConfig{
		Enabled:          true,
		FilterMode:       config.FilterModeExclusive,
		DevelopLabels:    []string{"bug"},
		ReviewOnlyLabels: []string{"question"},
		SkipLabels:       []string{"wontfix"},
		DefaultAction:    string(config.IssueModeIgnore),
	}
}

// ── PrefetchIssues tests ──────────────────────────────────────────────────────

func TestPrefetchIssues_PopulatesMapByRepo(t *testing.T) {
	searcher := &fakeSearcher{
		issues: []*github.Issue{
			makeIssue(1, 1, "org/repo-a", []string{"bug"}, nil),
			makeIssue(2, 2, "org/repo-b", []string{"bug"}, nil),
			makeIssue(3, 3, "org/repo-a", []string{"bug"}, nil),
		},
	}
	client := &fakeClient{}
	fetcher := issues.NewFetcher(client, nil, &fakeDedup{}, nil)
	fetcher.SetSearcher(searcher)

	byRepo, err := fetcher.PrefetchIssues(searchCfg(), "alice", []string{"org/repo-a", "org/repo-b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(byRepo["org/repo-a"]) != 2 {
		t.Errorf("expected 2 issues for org/repo-a, got %d", len(byRepo["org/repo-a"]))
	}
	if len(byRepo["org/repo-b"]) != 1 {
		t.Errorf("expected 1 issue for org/repo-b, got %d", len(byRepo["org/repo-b"]))
	}
	if searcher.calls != 1 {
		t.Errorf("expected exactly 1 SearchIssues call, got %d", searcher.calls)
	}
}

func TestPrefetchIssues_NilSearcherReturnsNil(t *testing.T) {
	fetcher := issues.NewFetcher(&fakeClient{}, nil, &fakeDedup{}, nil)
	// no SetSearcher call

	byRepo, err := fetcher.PrefetchIssues(searchCfg(), "alice", []string{"org/repo"})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if byRepo != nil {
		t.Errorf("expected nil map when no searcher, got %v", byRepo)
	}
}

func TestPrefetchIssues_SearchErrorPropagated(t *testing.T) {
	searchErr := errors.New("search rate limited")
	searcher := &fakeSearcher{err: searchErr}
	fetcher := issues.NewFetcher(&fakeClient{}, nil, &fakeDedup{}, nil)
	fetcher.SetSearcher(searcher)

	_, err := fetcher.PrefetchIssues(searchCfg(), "alice", []string{"org/repo"})
	if err == nil {
		t.Fatal("expected error to be propagated, got nil")
	}
}

func TestPrefetchIssues_EmptyReposListReturnsNil(t *testing.T) {
	searcher := &fakeSearcher{} // should not be called
	fetcher := issues.NewFetcher(&fakeClient{}, nil, &fakeDedup{}, nil)
	fetcher.SetSearcher(searcher)

	byRepo, err := fetcher.PrefetchIssues(searchCfg(), "alice", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if byRepo != nil {
		t.Errorf("expected nil for empty repos list, got %v", byRepo)
	}
	if searcher.calls != 0 {
		t.Errorf("expected 0 SearchIssues calls for empty repos, got %d", searcher.calls)
	}
}

// ── ProcessRepo prefetch path tests ──────────────────────────────────────────

// TestProcessRepo_UsesPrefetchedResults verifies that when a prefetch map is
// populated for a repo, ProcessRepo does NOT call FetchIssues for that repo.
func TestProcessRepo_UsesPrefetchedResults(t *testing.T) {
	// Searcher returns one issue for org/repo.
	searcher := &fakeSearcher{
		issues: []*github.Issue{
			makeIssue(1, 1, "org/repo", []string{"bug"}, []string{"alice"}),
		},
	}
	// REST client has a different issue — presence of its number in the
	// dispatched set would mean FetchIssues was incorrectly used.
	restClient := &fakeClient{
		issues: []*github.Issue{
			makeIssue(99, 99, "org/repo", []string{"bug"}, []string{"alice"}),
		},
	}
	pipe := &fakePipeline{}

	fetcher := issues.NewFetcher(restClient, nil, &fakeDedup{}, pipe)
	fetcher.SetSearcher(searcher)

	cfg := searchCfg()
	cfg.Assignees = []string{"alice"}

	// Warm the prefetch.
	if _, err := fetcher.PrefetchIssues(cfg, "alice", []string{"org/repo"}); err != nil {
		t.Fatalf("PrefetchIssues failed: %v", err)
	}

	// ProcessRepo should consume from prefetch, NOT call FetchIssues.
	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) {
		return issues.RunOptions{}, true
	}
	n, err := fetcher.ProcessRepo(context.Background(), "org/repo", cfg, "alice", optsFor)
	if err != nil {
		t.Fatalf("ProcessRepo failed: %v", err)
	}

	if restClient.calls != 0 {
		t.Errorf("FetchIssues should NOT have been called when prefetch map is warm; got %d calls", restClient.calls)
	}
	if n != 1 {
		t.Errorf("expected 1 dispatched issue from prefetch, got %d", n)
	}
	// The pipeline should have been called with the prefetched issue (number 1),
	// not the REST issue (number 99).
	if len(pipe.calls) != 1 || pipe.calls[0] != 1 {
		t.Errorf("pipeline should have received issue #1 from prefetch, got calls %v", pipe.calls)
	}
}

// TestProcessRepo_FallsBackToFetchIssuesWhenNoPrefetch verifies that when there
// is no prefetch entry for a repo, ProcessRepo falls back to FetchIssues.
func TestProcessRepo_FallsBackToFetchIssuesWhenNoPrefetch(t *testing.T) {
	restClient := &fakeClient{
		issues: []*github.Issue{
			makeIssue(5, 5, "org/repo", []string{"bug"}, nil),
		},
	}
	pipe := &fakePipeline{}
	fetcher := issues.NewFetcher(restClient, nil, &fakeDedup{}, pipe)
	// no SetSearcher / no PrefetchIssues — map is nil

	cfg := searchCfg()
	// ProcessRepo requires at least one assignee; provide the auth user as fallback.
	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) {
		return issues.RunOptions{}, true
	}
	if _, err := fetcher.ProcessRepo(context.Background(), "org/repo", cfg, "alice", optsFor); err != nil {
		t.Fatalf("ProcessRepo failed: %v", err)
	}
	if restClient.calls != 1 {
		t.Errorf("FetchIssues should have been called as fallback; got %d calls", restClient.calls)
	}
}

// TestProcessRepo_FallsBackWhenSearchErrors verifies that a failed PrefetchIssues
// leaves the prefetch map nil so ProcessRepo falls back to per-repo FetchIssues.
func TestProcessRepo_FallsBackWhenSearchErrors(t *testing.T) {
	searcher := &fakeSearcher{err: errors.New("search unavailable")}
	restClient := &fakeClient{
		issues: []*github.Issue{
			makeIssue(7, 7, "org/repo", []string{"bug"}, nil),
		},
	}
	pipe := &fakePipeline{}
	fetcher := issues.NewFetcher(restClient, nil, &fakeDedup{}, pipe)
	fetcher.SetSearcher(searcher)

	cfg := searchCfg()

	// PrefetchIssues returns an error — prefetch map stays nil.
	if _, err := fetcher.PrefetchIssues(cfg, "alice", []string{"org/repo"}); err == nil {
		t.Fatal("expected PrefetchIssues to return the search error")
	}

	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) {
		return issues.RunOptions{}, true
	}
	// Use "alice" so the assignee scope check passes.
	if _, err := fetcher.ProcessRepo(context.Background(), "org/repo", cfg, "alice", optsFor); err != nil {
		t.Fatalf("ProcessRepo failed: %v", err)
	}
	if restClient.calls != 1 {
		t.Errorf("FetchIssues fallback should have run; got %d calls", restClient.calls)
	}
}

// TestClearPrefetch_PreventsStaleReuseAcrossCycles verifies that ClearPrefetch
// removes the map so the next cycle's ProcessRepo falls back to FetchIssues.
func TestClearPrefetch_PreventsStaleReuseAcrossCycles(t *testing.T) {
	searcher := &fakeSearcher{
		issues: []*github.Issue{
			makeIssue(1, 1, "org/repo", []string{"bug"}, []string{"alice"}),
		},
	}
	restClient := &fakeClient{
		issues: []*github.Issue{
			makeIssue(2, 2, "org/repo", []string{"bug"}, []string{"alice"}),
		},
	}
	pipe := &fakePipeline{}
	fetcher := issues.NewFetcher(restClient, nil, &fakeDedup{}, pipe)
	fetcher.SetSearcher(searcher)

	cfg := searchCfg()
	cfg.Assignees = []string{"alice"}
	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }

	// Cycle 1: prefetch active — FetchIssues must NOT be called.
	fetcher.PrefetchIssues(cfg, "alice", []string{"org/repo"}) //nolint:errcheck
	fetcher.ProcessRepo(context.Background(), "org/repo", cfg, "alice", optsFor) //nolint:errcheck
	if restClient.calls != 0 {
		t.Fatalf("cycle 1: FetchIssues should not be called when prefetch warm; got %d", restClient.calls)
	}

	// Clear the prefetch.
	fetcher.ClearPrefetch()

	// Cycle 2: prefetch gone — FetchIssues must be called.
	fetcher.ProcessRepo(context.Background(), "org/repo", cfg, "alice", optsFor) //nolint:errcheck
	if restClient.calls != 1 {
		t.Errorf("cycle 2: FetchIssues should be called after ClearPrefetch; got %d calls", restClient.calls)
	}
}

// TestPrefetchIssues_ClassifyAndFilterApplied verifies that search results are
// classified and filtered before being stored, so ProcessRepo only sees eligible
// issues from the prefetch map.
func TestPrefetchIssues_ClassifyAndFilterApplied(t *testing.T) {
	searcher := &fakeSearcher{
		issues: []*github.Issue{
			// Should pass: label=bug, assignee=alice
			makeIssue(1, 1, "org/repo", []string{"bug"}, []string{"alice"}),
			// Should be dropped by skip label filter
			makeIssue(2, 2, "org/repo", []string{"wontfix"}, []string{"alice"}),
			// Should be dropped: assignee filter (only alice is allowed)
			makeIssue(3, 3, "org/repo", []string{"bug"}, []string{"bob"}),
		},
	}

	restClient := &fakeClient{}
	pipe := &fakePipeline{}

	fetcher := issues.NewFetcher(restClient, nil, &fakeDedup{}, pipe)
	fetcher.SetSearcher(searcher)

	cfg := searchCfg()
	cfg.Assignees = []string{"alice"}

	if _, err := fetcher.PrefetchIssues(cfg, "alice", []string{"org/repo"}); err != nil {
		t.Fatalf("PrefetchIssues failed: %v", err)
	}

	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) {
		return issues.RunOptions{}, true
	}
	n, err := fetcher.ProcessRepo(context.Background(), "org/repo", cfg, "alice", optsFor)
	if err != nil {
		t.Fatalf("ProcessRepo failed: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 dispatched issue after classify+filter, got %d", n)
	}
	if len(pipe.calls) != 1 || pipe.calls[0] != 1 {
		t.Errorf("expected only issue #1 dispatched, got %v", pipe.calls)
	}
}
