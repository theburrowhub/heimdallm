package issues_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
)

// countingFetcher records every per-repo REST FetchIssues call, so the test can
// contrast the aggregated Search path against the per-repo baseline.
type countingFetcher struct {
	calls int
	repos []string
}

func (c *countingFetcher) FetchIssues(repo string, cfg config.IssueTrackingConfig, user string) ([]*github.Issue, error) {
	c.calls++
	c.repos = append(c.repos, repo)
	return nil, nil
}

// TestAPICallCount_PrefetchCoversIdleRepos is the load-bearing test for the
// aggregated-search work, and it deliberately models the shape of the real
// deployment that motivated it: 47 monitored repos of which only 16 actually
// have open assigned issues.
//
// The idle majority is the whole point. An earlier version of this test gave
// every repo an issue, which made it pass while hiding the defect that repos
// absent from the search results fell through to a per-repo REST call every
// cycle — i.e. 31 of 47 repos kept paying the cost the aggregation exists to
// remove. Keep the asymmetry here.
func TestAPICallCount_PrefetchCoversIdleRepos(t *testing.T) {
	const (
		numRepos        = 47
		reposWithIssues = 16
	)

	repos := make([]string, numRepos)
	for i := range repos {
		repos[i] = fmt.Sprintf("org/repo-%02d", i)
	}

	// Only the first 16 repos have issues; the other 31 are idle.
	var all []*github.Issue
	for i := 0; i < reposWithIssues; i++ {
		all = append(all, makeIssue(int64(i+1), i+1, repos[i], []string{"bug"}, nil))
	}

	searcher := &fakeSearcher{perQueryIssues: map[int][]*github.Issue{0: all, 1: nil}}
	rest := &countingFetcher{}

	fetcher := issues.NewFetcher(rest, nil, &fakeDedup{}, &fakePipeline{})
	fetcher.SetSearcher(searcher)

	byRepo, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), repos), "alice", repos)
	if err != nil {
		t.Fatalf("PrefetchIssues: %v", err)
	}

	if searcher.calls != 2 {
		t.Errorf("expected 2 bounded search chunks, got %d", searcher.calls)
	}
	for i, query := range searcher.queries {
		if reposInQuery := strings.Count(query, "repo:"); reposInQuery > 25 {
			t.Errorf("query %d contains %d repos, want <=25", i, reposInQuery)
		}
		if encoded := len(url.QueryEscape(query)); encoded > 1024 {
			t.Errorf("query %d encoded length = %d, want <=1024", i, encoded)
		}
	}

	// Every searched repo must be present — idle ones with an empty slice.
	// A missing key here is a per-repo REST call every poll cycle.
	if len(byRepo) != numRepos {
		var missing []string
		for _, r := range repos {
			if _, ok := byRepo[r]; !ok {
				missing = append(missing, r)
			}
		}
		t.Errorf("prefetch map covers %d/%d repos; %d missing would fall back to REST: %s",
			len(byRepo), numRepos, len(missing), strings.Join(missing, " "))
	}

	// Drive the real consumer: no repo, idle or not, may touch per-repo REST.
	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }
	for _, r := range repos {
		if _, err := fetcher.ProcessRepo(context.Background(), r, searchCfg(), "alice", optsFor); err != nil {
			t.Fatalf("ProcessRepo(%s): %v", r, err)
		}
	}

	t.Logf("prefetch ON : %d aggregated search call(s) covering %d repos (%d idle)",
		searcher.calls, numRepos, numRepos-reposWithIssues)
	t.Logf("prefetch OFF: %d per-repo REST call(s) would be needed", numRepos)

	if rest.calls != 0 {
		t.Errorf("per-repo REST must not be touched, got %d calls: %s",
			rest.calls, strings.Join(rest.repos, " "))
	}
}

func TestPrefetchIssues_FailedChunkFallsBackOnlyForThatChunk(t *testing.T) {
	const numRepos = 47
	repos := make([]string, numRepos)
	for i := range repos {
		repos[i] = fmt.Sprintf("org/repo-%02d", i)
	}

	searcher := &fakeSearcher{perQueryErr: map[int]error{1: fmt.Errorf("second chunk failed")}}
	rest := &countingFetcher{}
	fetcher := issues.NewFetcher(rest, nil, &fakeDedup{}, &fakePipeline{})
	fetcher.SetSearcher(searcher)

	byRepo, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), repos), "alice", repos)
	if err == nil {
		t.Fatal("expected partial prefetch error")
	}
	if len(byRepo) != 25 {
		t.Fatalf("successful chunk coverage = %d, want 25", len(byRepo))
	}

	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }
	for _, repo := range repos {
		if _, processErr := fetcher.ProcessRepo(context.Background(), repo, searchCfg(), "alice", optsFor); processErr != nil {
			t.Fatalf("ProcessRepo(%s): %v", repo, processErr)
		}
	}
	if rest.calls != numRepos-25 {
		t.Fatalf("REST fallbacks = %d, want %d for failed chunk only", rest.calls, numRepos-25)
	}
}

// TestPrefetchIssues_FailedGroupStillFallsBack guards the other side of the
// seeding change: repos are seeded only for groups whose search SUCCEEDED, so a
// failed search must still leave its repos absent and fall back to REST rather
// than silently reporting zero issues for them.
func TestPrefetchIssues_FailedGroupStillFallsBack(t *testing.T) {
	searcher := &fakeSearcher{err: fmt.Errorf("search unavailable")}
	rest := &countingFetcher{}

	fetcher := issues.NewFetcher(rest, nil, &fakeDedup{}, &fakePipeline{})
	fetcher.SetSearcher(searcher)

	repos := []string{"org/a", "org/b"}
	if _, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), repos), "alice", repos); err == nil {
		t.Fatal("expected the search error to propagate")
	}

	optsFor := func(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }
	for _, r := range repos {
		if _, err := fetcher.ProcessRepo(context.Background(), r, searchCfg(), "alice", optsFor); err != nil {
			t.Fatalf("ProcessRepo(%s): %v", r, err)
		}
	}
	if rest.calls != len(repos) {
		t.Errorf("failed search must fall back to REST for every repo; got %d of %d",
			rest.calls, len(repos))
	}
}
