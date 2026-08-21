package main

import (
	"sync"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	issuepipeline "github.com/heimdallm/daemon/internal/issues"
)

type emptyIssueSearcher struct{ calls int }

func (s *emptyIssueSearcher) SearchIssues(string) ([]*gh.Issue, error) {
	s.calls++
	return nil, nil
}

func TestIssuePrefetchPlanIncludesAdaptivePromotionRepos(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{
		Enabled:       true,
		Assignees:     []string{"alice"},
		BlockedLabels: []string{"blocked"},
		DevelopLabels: []string{"ready"},
	}
	cfgRef := cfg
	var cfgMu sync.Mutex
	a := &tier2Adapter{cfg: &cfgRef, cfgMu: &cfgMu}

	got, promotionConfigured := a.IssuePrefetchPlan(
		[]string{"org/due", "org/backed-off"},
		[]string{"org/due"},
	)
	if !promotionConfigured {
		t.Fatal("promotionConfigured = false, want true")
	}
	if len(got) != 2 || got[0] != "org/due" || got[1] != "org/backed-off" {
		t.Fatalf("prefetch plan = %v, want [org/due org/backed-off]", got)
	}
}

func TestIssueRepoNeedsCoreFetchOnlyWithoutCompleteSnapshot(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{
		Enabled:   true,
		Assignees: []string{"alice"},
	}
	cfgRef := cfg
	var cfgMu, loginMu sync.Mutex
	login := "alice"
	searcher := &emptyIssueSearcher{}
	fetcher := issuepipeline.NewFetcher(nil, nil, nil, nil)
	fetcher.SetSearcher(searcher)
	a := &tier2Adapter{
		fetcher: fetcher,
		cfg:     &cfgRef,
		cfgMu:   &cfgMu,
		login:   &login,
		loginMu: &loginMu,
	}

	a.PrefetchIssuesForCycle([]string{"org/a", "org/b"})
	if searcher.calls != 1 {
		t.Fatalf("search calls = %d, want 1", searcher.calls)
	}
	for _, repo := range []string{"org/a", "org/b"} {
		if a.IssueRepoNeedsCoreFetch(repo) {
			t.Fatalf("%s charged a core fallback despite complete empty snapshot", repo)
		}
	}

	a.ClearIssuePrefetch()
	if !a.IssueRepoNeedsCoreFetch("org/a") {
		t.Fatal("org/a should need a core fallback after snapshot clear")
	}
}
