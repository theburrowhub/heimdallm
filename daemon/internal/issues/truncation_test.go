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
// Skipping only the repos with no results is not enough either: a repo can have
// some issues inside the first 1000 and the rest beyond it, so serving its
// partial list reports it as complete — the same loss, just narrower. A
// truncated group is therefore discarded entirely and every repo in it falls
// back to a per-repo fetch.

func TestPrefetchIssues_TruncatedGroupIsDiscardedEntirely(t *testing.T) {
	// The search covered three repos but was truncated; org/a appeared in the
	// 1000 items we did get back — its list may still be incomplete.
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

	// No repo from a truncated group may be served from the prefetch —
	// including the one that did return results.
	for _, r := range repos {
		if _, ok := byRepo[r]; ok {
			t.Errorf("%s came from a truncated search; serving it would report a possibly partial list as complete", r)
		}
	}

	// Drive the consumer: every repo in the group must fall back to REST.
	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }
	for _, r := range repos {
		if _, err := fetcher.ProcessRepo(context.Background(), r, searchCfg(), "alice", optsFor); err != nil {
			t.Fatalf("ProcessRepo(%s): %v", r, err)
		}
	}
	if rest.calls != len(repos) {
		t.Errorf("expected all %d repos of a truncated group to fall back to REST, got %d calls (%v)",
			len(repos), rest.calls, rest.repos)
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
