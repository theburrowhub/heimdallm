package issues_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
)

// ── fakeSearcher ─────────────────────────────────────────────────────────────

// fakeSearcher records every query it receives so tests can assert the
// correct number and shape of queries.
type fakeSearcher struct {
	// issues is returned for every call unless perQueryIssues is set.
	issues []*github.Issue
	err    error
	calls  int
	// queries records the raw query strings passed to each SearchIssues call.
	queries []string
	// perQueryIssues maps query index (0-based) to a custom result. When set,
	// the corresponding call returns that slice instead of f.issues.
	perQueryIssues map[int][]*github.Issue
	// perQueryErr overrides err for a specific query index.
	perQueryErr map[int]error
	// errWithResults is returned ALONGSIDE the results rather than instead of
	// them, modelling the partial-success shape of ErrSearchTruncated. Takes
	// precedence over err.
	errWithResults error
}

func (s *fakeSearcher) SearchIssues(query string) ([]*github.Issue, error) {
	idx := s.calls
	s.calls++
	s.queries = append(s.queries, query)
	if err, ok := s.perQueryErr[idx]; ok {
		return nil, err
	}
	if s.errWithResults == nil && s.err != nil {
		return nil, s.err
	}
	if s.perQueryIssues != nil {
		if specific, ok := s.perQueryIssues[idx]; ok {
			return specific, s.errWithResults
		}
	}
	return s.issues, s.errWithResults
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

// simpleEligibleFn builds an issues.EligibleFn that returns the same cfg for
// every repo in the allowed set (autonomous=false, ok=true) and ok=false for
// any repo not in the set. Pass nil to allow all repos.
func simpleEligibleFn(cfg config.IssueTrackingConfig, allowed []string) issues.EligibleFn {
	set := make(map[string]bool, len(allowed))
	for _, r := range allowed {
		set[r] = true
	}
	return func(repo string) (config.IssueTrackingConfig, bool, bool) {
		if len(set) > 0 && !set[repo] {
			return config.IssueTrackingConfig{}, false, false
		}
		return cfg, false, true
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

	repos := []string{"org/repo-a", "org/repo-b"}
	byRepo, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), repos), "alice", repos)
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

	repos := []string{"org/repo"}
	byRepo, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), repos), "alice", repos)
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

	repos := []string{"org/repo"}
	_, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), repos), "alice", repos)
	if err == nil {
		t.Fatal("expected error to be propagated, got nil")
	}
}

func TestPrefetchIssues_EmptyReposListReturnsNil(t *testing.T) {
	searcher := &fakeSearcher{} // should not be called
	fetcher := issues.NewFetcher(&fakeClient{}, nil, &fakeDedup{}, nil)
	fetcher.SetSearcher(searcher)

	byRepo, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), nil), "alice", nil)
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

	repos := []string{"org/repo"}
	// Warm the prefetch.
	if _, err := fetcher.PrefetchIssues(simpleEligibleFn(cfg, repos), "alice", repos); err != nil {
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

// TestProcessRepo_FallsBackWhenPrefetchMapNonNilButKeyAbsent verifies that
// ProcessRepo falls back to per-repo FetchIssues when the prefetch map is
// non-nil but does not contain the repo. This covers the case where the search
// API returned 0 results for that repo (e.g., over the 1000-issue cap within
// its group).
func TestProcessRepo_FallsBackWhenPrefetchMapNonNilButKeyAbsent(t *testing.T) {
	// Prefetch is warm for org/other-repo but NOT for org/repo. A repo ends up
	// absent when it was never in the search scope, when its group's search
	// failed, or when its group's search was truncated — in all three cases
	// "no results" does not mean "no issues", so the REST fallback must fire.
	// Repos in a group that searched cleanly get a present-but-empty entry
	// instead, because there "no results" IS the answer.
	searcher := &fakeSearcher{
		issues: []*github.Issue{
			makeIssue(10, 10, "org/other-repo", []string{"bug"}, []string{"alice"}),
		},
	}
	restClient := &fakeClient{
		issues: []*github.Issue{
			makeIssue(7, 7, "org/repo", []string{"bug"}, nil),
		},
	}
	pipe := &fakePipeline{}
	fetcher := issues.NewFetcher(restClient, nil, &fakeDedup{}, pipe)
	fetcher.SetSearcher(searcher)

	cfg := searchCfg()
	cfg.Assignees = []string{"alice"}

	// Prefetch scope deliberately excludes org/repo.
	searched := []string{"org/other-repo"}
	if _, err := fetcher.PrefetchIssues(simpleEligibleFn(cfg, searched), "alice", searched); err != nil {
		t.Fatalf("PrefetchIssues failed: %v", err)
	}

	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }
	// ProcessRepo for org/repo — prefetch map exists but org/repo key is absent.
	if _, err := fetcher.ProcessRepo(context.Background(), "org/repo", cfg, "alice", optsFor); err != nil {
		t.Fatalf("ProcessRepo failed: %v", err)
	}
	if restClient.calls != 1 {
		t.Errorf("FetchIssues fallback should have fired for absent key; got %d calls", restClient.calls)
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

	repos := []string{"org/repo"}
	// PrefetchIssues returns an error — prefetch map stays nil.
	if _, err := fetcher.PrefetchIssues(simpleEligibleFn(cfg, repos), "alice", repos); err == nil {
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

	repos := []string{"org/repo"}
	// Cycle 1: prefetch active — FetchIssues must NOT be called.
	fetcher.PrefetchIssues(simpleEligibleFn(cfg, repos), "alice", repos)         //nolint:errcheck
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

	repos := []string{"org/repo"}
	if _, err := fetcher.PrefetchIssues(simpleEligibleFn(cfg, repos), "alice", repos); err != nil {
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

// ── Group-by-assignee-set tests ───────────────────────────────────────────────

// TestPrefetchIssues_TwoGroupsIssuesTwoSearchCalls verifies that when two repos
// have different effective assignee sets, PrefetchIssues issues TWO search
// queries (one per group) and merges both repos' results into the prefetch map.
func TestPrefetchIssues_TwoGroupsIssuesTwoSearchCalls(t *testing.T) {
	// Group 1: org/global-repo uses global assignees ["alice"]
	// Group 2: org/override-repo uses per-repo override assignees ["bob"]
	globalCfg := config.IssueTrackingConfig{
		Enabled:       true,
		Assignees:     []string{"alice"},
		DefaultAction: string(config.IssueModeIgnore),
		DevelopLabels: []string{"bug"},
	}
	overrideCfg := config.IssueTrackingConfig{
		Enabled:       true,
		Assignees:     []string{"bob"},
		DefaultAction: string(config.IssueModeIgnore),
		DevelopLabels: []string{"bug"},
	}

	searcher := &fakeSearcher{
		perQueryIssues: map[int][]*github.Issue{
			// First query (group: alice): returns issue for org/global-repo
			0: {makeIssue(1, 1, "org/global-repo", []string{"bug"}, []string{"alice"})},
			// Second query (group: bob): returns issue for org/override-repo
			1: {makeIssue(2, 2, "org/override-repo", []string{"bug"}, []string{"bob"})},
		},
	}
	fetcher := issues.NewFetcher(&fakeClient{}, nil, &fakeDedup{}, nil)
	fetcher.SetSearcher(searcher)

	repos := []string{"org/global-repo", "org/override-repo"}
	eligibleFn := func(repo string) (config.IssueTrackingConfig, bool, bool) {
		switch repo {
		case "org/global-repo":
			return globalCfg, false, true
		case "org/override-repo":
			return overrideCfg, false, true
		default:
			return config.IssueTrackingConfig{}, false, false
		}
	}

	byRepo, err := fetcher.PrefetchIssues(eligibleFn, "alice", repos)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Two distinct assignee sets → two search queries.
	if searcher.calls != 2 {
		t.Errorf("expected 2 SearchIssues calls (one per assignee group), got %d", searcher.calls)
	}

	// Both repos' issues must be in the prefetch map.
	if len(byRepo["org/global-repo"]) != 1 {
		t.Errorf("expected 1 issue for org/global-repo, got %d", len(byRepo["org/global-repo"]))
	}
	if len(byRepo["org/override-repo"]) != 1 {
		t.Errorf("expected 1 issue for org/override-repo, got %d", len(byRepo["org/override-repo"]))
	}

	// Each query must use the correct assignee scope.
	globalQuery := searcher.queries[0]
	overrideQuery := searcher.queries[1]
	// Determine which query is for which group (order is deterministic but
	// depends on group key sort — check both queries collectively).
	allQueries := globalQuery + " " + overrideQuery
	if !strings.Contains(allQueries, "assignee:alice") {
		t.Errorf("expected assignee:alice in one of the queries, got: %v", searcher.queries)
	}
	if !strings.Contains(allQueries, "assignee:bob") {
		t.Errorf("expected assignee:bob in one of the queries, got: %v", searcher.queries)
	}
}

// TestPrefetchIssues_PerRepoOverrideAssigneeIssueNotDropped verifies the core
// correctness fix: an issue assigned to a per-repo-override assignee is present
// in that repo's prefetched results even when the global assignees are different.
// Before the fix, the single-query path would miss such issues if the prefetch
// map already had an entry for that repo (from global-assignee issues), causing
// silently dropped issues.
func TestPrefetchIssues_PerRepoOverrideAssigneeIssueNotDropped(t *testing.T) {
	globalCfg := config.IssueTrackingConfig{
		Enabled:       true,
		Assignees:     []string{"alice"},
		DefaultAction: string(config.IssueModeIgnore),
		DevelopLabels: []string{"bug"},
	}
	overrideCfg := config.IssueTrackingConfig{
		Enabled:       true,
		Assignees:     []string{"bob"},
		DefaultAction: string(config.IssueModeIgnore),
		DevelopLabels: []string{"bug"},
	}

	// The searcher returns an issue assigned to "bob" for org/override-repo.
	searcher := &fakeSearcher{
		perQueryIssues: map[int][]*github.Issue{
			0: {makeIssue(1, 1, "org/global-repo", []string{"bug"}, []string{"alice"})},
			1: {makeIssue(2, 2, "org/override-repo", []string{"bug"}, []string{"bob"})},
		},
	}
	restClient := &fakeClient{}
	pipe := &fakePipeline{}
	fetcher := issues.NewFetcher(restClient, nil, &fakeDedup{}, pipe)
	fetcher.SetSearcher(searcher)

	repos := []string{"org/global-repo", "org/override-repo"}
	eligibleFn := func(repo string) (config.IssueTrackingConfig, bool, bool) {
		switch repo {
		case "org/global-repo":
			return globalCfg, false, true
		case "org/override-repo":
			return overrideCfg, false, true
		default:
			return config.IssueTrackingConfig{}, false, false
		}
	}

	if _, err := fetcher.PrefetchIssues(eligibleFn, "alice", repos); err != nil {
		t.Fatalf("PrefetchIssues failed: %v", err)
	}

	// ProcessRepo for org/override-repo must use prefetched results and
	// classify/filter them with the override config (assignee=bob).
	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }
	n, err := fetcher.ProcessRepo(context.Background(), "org/override-repo", overrideCfg, "alice", optsFor)
	if err != nil {
		t.Fatalf("ProcessRepo(org/override-repo) failed: %v", err)
	}
	// The bob-assigned issue must have been dispatched.
	if n != 1 {
		t.Errorf("expected 1 dispatched issue for org/override-repo (bob-assigned), got %d", n)
	}
	if restClient.calls != 0 {
		t.Errorf("FetchIssues should NOT have been called (prefetch was populated); got %d calls", restClient.calls)
	}
}

// TestPrefetchIssues_SharedAssigneeSetRunsOneQuery verifies that repos sharing
// the same effective assignees are batched into a single search query.
func TestPrefetchIssues_SharedAssigneeSetRunsOneQuery(t *testing.T) {
	sharedCfg := config.IssueTrackingConfig{
		Enabled:       true,
		Assignees:     []string{"alice"},
		DefaultAction: string(config.IssueModeIgnore),
		DevelopLabels: []string{"bug"},
	}

	searcher := &fakeSearcher{
		issues: []*github.Issue{
			makeIssue(1, 1, "org/repo-a", []string{"bug"}, []string{"alice"}),
			makeIssue(2, 2, "org/repo-b", []string{"bug"}, []string{"alice"}),
		},
	}
	fetcher := issues.NewFetcher(&fakeClient{}, nil, &fakeDedup{}, nil)
	fetcher.SetSearcher(searcher)

	repos := []string{"org/repo-a", "org/repo-b"}
	if _, err := fetcher.PrefetchIssues(simpleEligibleFn(sharedCfg, repos), "alice", repos); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Same assignees → one query covering both repos.
	if searcher.calls != 1 {
		t.Errorf("expected 1 SearchIssues call for shared assignee set, got %d", searcher.calls)
	}
	// Both repos must appear in the single query.
	if len(searcher.queries) == 0 {
		t.Fatal("no queries recorded")
	}
	q := searcher.queries[0]
	if !strings.Contains(q, "repo:org/repo-a") {
		t.Errorf("expected repo:org/repo-a in query, got %q", q)
	}
	if !strings.Contains(q, "repo:org/repo-b") {
		t.Errorf("expected repo:org/repo-b in query, got %q", q)
	}
}

// ── BuildAggregatedSearchQuery tests ─────────────────────────────────────────

// TestBuildAggregatedSearchQuery_NoLabelQualifiers verifies that the aggregated
// search query contains assignee and repo scope but NO label qualifiers.
func TestBuildAggregatedSearchQuery_NoLabelQualifiers(t *testing.T) {
	q := github.BuildAggregatedSearchQuery([]string{"alice"}, []string{"org/repo"})
	if strings.Contains(q, "label:") {
		t.Errorf("aggregated query must not contain label qualifiers, got %q", q)
	}
	if !strings.Contains(q, "is:issue") {
		t.Errorf("expected is:issue, got %q", q)
	}
	if !strings.Contains(q, "is:open") {
		t.Errorf("expected is:open, got %q", q)
	}
	if !strings.Contains(q, "assignee:alice") {
		t.Errorf("expected assignee:alice, got %q", q)
	}
	if !strings.Contains(q, "repo:org/repo") {
		t.Errorf("expected repo:org/repo, got %q", q)
	}
}

// TestBuildAggregatedSearchQuery_MultipleAssigneesAndRepos verifies that
// multiple assignees and repos are all included in the query.
func TestBuildAggregatedSearchQuery_MultipleAssigneesAndRepos(t *testing.T) {
	q := github.BuildAggregatedSearchQuery(
		[]string{"alice", "bob"},
		[]string{"org/repo-a", "org/repo-b"},
	)
	for _, expected := range []string{"assignee:alice", "assignee:bob", "repo:org/repo-a", "repo:org/repo-b"} {
		if !strings.Contains(q, expected) {
			t.Errorf("expected %q in query, got %q", expected, q)
		}
	}
	if strings.Contains(q, "label:") {
		t.Errorf("no label qualifiers expected, got %q", q)
	}
}

// TestBuildAggregatedSearchQuery_AssigneesAreOneUnionQuery pins the shape that
// depends on GitHub Search treating repeated assignee: qualifiers as a UNION.
// Verified against the live API: assignee:a → 36, assignee:b → 3, both
// together → 39. Reading them as an intersection invites "fixing" this into one
// query per assignee, which would multiply the search spend for the same rows.
// The client-side MatchesAssignees narrows the superset afterwards.
func TestBuildAggregatedSearchQuery_AssigneesAreOneUnionQuery(t *testing.T) {
	q := github.BuildAggregatedSearchQuery(
		[]string{"alice", "bob", "carol"},
		[]string{"org/repo-a"},
	)
	if got := strings.Count(q, "assignee:"); got != 3 {
		t.Errorf("expected all 3 assignees in a single query, got %d in %q", got, q)
	}
	if strings.Contains(q, " OR ") || strings.Contains(q, " AND ") {
		t.Errorf("qualifiers combine implicitly; no explicit operators expected, got %q", q)
	}
}
