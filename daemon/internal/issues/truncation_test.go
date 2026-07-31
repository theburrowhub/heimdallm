package issues_test

import (
	"context"
	"testing"

	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
)

// Seeding every searched repo with an empty entry suppresses the per-repo REST
// fallback — that is the point of it. But past the Search API's 1000-item cap,
// "this repo had no results" stops meaning "this repo has no issues" and starts
// meaning "we stopped looking". Seeding a truncated group would convert issues
// we never saw into a confident zero, with no error and no per-repo log.
//
// So a truncated group keeps the pre-seeding behaviour: repos WITH results are
// still served from the prefetch, repos without stay absent and pay for a
// correct per-repo fetch.

func TestPrefetchIssues_TruncatedGroupIsNotSeeded(t *testing.T) {
	// The search covered three repos but was truncated; only org/a appeared
	// in the 1000 items we did get back.
	searcher := &fakeSearcher{
		issues: []*github.Issue{
			makeIssue(1, 1, "org/a", []string{"bug"}, nil),
		},
		errWithResults: github.ErrSearchTruncated,
	}
	rest := &countingFetcher{}
	fetcher := issues.NewFetcher(rest, nil, &fakeDedup{}, &fakePipeline{})
	fetcher.SetSearcher(searcher)

	repos := []string{"org/a", "org/b", "org/c"}
	byRepo, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), repos), "alice", repos)
	if err != nil {
		t.Fatalf("truncation is a partial success, not a failure: %v", err)
	}

	// org/a came back in the results, so it is prefetched.
	if _, ok := byRepo["org/a"]; !ok {
		t.Error("org/a had results and must still be served from the prefetch")
	}
	// org/b and org/c must NOT be seeded — we never saw past the cap.
	for _, r := range []string{"org/b", "org/c"} {
		if _, ok := byRepo[r]; ok {
			t.Errorf("%s was seeded from a truncated search; its issues would be dropped silently", r)
		}
	}

	// Drive the consumer: the unseeded repos must fall back to REST.
	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }
	for _, r := range repos {
		if _, err := fetcher.ProcessRepo(context.Background(), r, searchCfg(), "alice", optsFor); err != nil {
			t.Fatalf("ProcessRepo(%s): %v", r, err)
		}
	}
	if rest.calls != 2 {
		t.Errorf("expected the 2 repos absent from a truncated result set to fall back to REST, got %d calls (%v)",
			rest.calls, rest.repos)
	}
}

// The counterpart: a clean (untruncated) search still seeds everything, so the
// truncation guard must not undo the savings in the normal case.
func TestPrefetchIssues_UntruncatedGroupStillSeedsAll(t *testing.T) {
	searcher := &fakeSearcher{
		issues: []*github.Issue{
			makeIssue(1, 1, "org/a", []string{"bug"}, nil),
		},
	}
	rest := &countingFetcher{}
	fetcher := issues.NewFetcher(rest, nil, &fakeDedup{}, &fakePipeline{})
	fetcher.SetSearcher(searcher)

	repos := []string{"org/a", "org/b", "org/c"}
	if _, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), repos), "alice", repos); err != nil {
		t.Fatalf("PrefetchIssues: %v", err)
	}

	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }
	for _, r := range repos {
		if _, err := fetcher.ProcessRepo(context.Background(), r, searchCfg(), "alice", optsFor); err != nil {
			t.Fatalf("ProcessRepo(%s): %v", r, err)
		}
	}
	if rest.calls != 0 {
		t.Errorf("a clean search must cover every repo; got %d REST calls (%v)", rest.calls, rest.repos)
	}
}
