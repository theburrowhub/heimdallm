package issues_test

import (
	"fmt"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
)

// countingFetcher records every per-repo REST FetchIssues call, so the test can
// contrast the aggregated Search path against the per-repo baseline.
type countingFetcher struct {
	calls int
}

func (c *countingFetcher) FetchIssues(repo string, cfg config.IssueTrackingConfig, user string) ([]*github.Issue, error) {
	c.calls++
	return nil, nil
}

// TestAPICallCount_PrefetchCollapsesPerRepoCalls measures the point of the
// gh-api-efficiency work: with the aggregated Search prefetch enabled, a poll
// cycle over N repos sharing one assignee set issues ONE API call instead of N.
//
// 47 repos mirrors the real deployment (discovery over freepik-company +
// theburrowhub), so the numbers map onto the production rate-limit budget:
// at poll_interval=1m that is 47*60=2820 calls/hour vs 60 calls/hour.
func TestAPICallCount_PrefetchCollapsesPerRepoCalls(t *testing.T) {
	const numRepos = 47

	repos := make([]string, numRepos)
	for i := range repos {
		repos[i] = fmt.Sprintf("org/repo-%02d", i)
	}

	// Give the searcher one issue per repo so the prefetch map is populated
	// for every repo — otherwise ProcessRepo would fall back to REST.
	all := make([]*github.Issue, 0, numRepos)
	for i, r := range repos {
		all = append(all, makeIssue(int64(i+1), i+1, r, []string{"bug"}, nil))
	}

	searcher := &fakeSearcher{issues: all}
	rest := &countingFetcher{}

	fetcher := issues.NewFetcher(rest, nil, &fakeDedup{}, nil)
	fetcher.SetSearcher(searcher)

	byRepo, err := fetcher.PrefetchIssues(simpleEligibleFn(searchCfg(), repos), "alice", repos)
	if err != nil {
		t.Fatalf("PrefetchIssues: %v", err)
	}

	t.Logf("prefetch ON : %d aggregated search call(s) covering %d repos", searcher.calls, numRepos)
	t.Logf("prefetch OFF: %d per-repo REST call(s) would be needed", numRepos)
	t.Logf("reduction   : %dx fewer calls per poll cycle", numRepos/max(searcher.calls, 1))

	if searcher.calls != 1 {
		t.Errorf("expected 1 aggregated search call, got %d", searcher.calls)
	}
	if rest.calls != 0 {
		t.Errorf("prefetch path must not touch the per-repo REST endpoint, got %d calls", rest.calls)
	}
	if len(byRepo) != numRepos {
		t.Errorf("prefetch map covers %d repos, want %d — uncovered repos fall back to REST",
			len(byRepo), numRepos)
	}
}
