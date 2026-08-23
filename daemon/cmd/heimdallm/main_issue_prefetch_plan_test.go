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

func TestIssuePrefetchPlanHandlesNilFilteringAndUnlockedConfig(t *testing.T) {
	due := []string{"org/due"}
	got, promotionConfigured := (*tier2Adapter)(nil).IssuePrefetchPlan(nil, due)
	if promotionConfigured || len(got) != 1 || got[0] != due[0] {
		t.Fatalf("nil adapter plan = (%v, %v), want ([org/due], false)", got, promotionConfigured)
	}
	got[0] = "changed"
	if due[0] != "org/due" {
		t.Fatal("nil adapter returned the caller's backing slice")
	}

	cfg := &config.Config{}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{
		Enabled:       true,
		Assignees:     []string{"alice"},
		BlockedLabels: []string{"blocked"},
	}
	cfgRef := cfg
	a := &tier2Adapter{cfg: &cfgRef}

	got, promotionConfigured = a.IssuePrefetchPlan(
		[]string{"", "org/due", "org/promotion", "org/promotion"},
		[]string{"", "org/due", "org/due"},
	)
	if !promotionConfigured {
		t.Fatal("promotionConfigured = false, want true")
	}
	if len(got) != 2 || got[0] != "org/due" || got[1] != "org/promotion" {
		t.Fatalf("filtered plan = %v, want [org/due org/promotion]", got)
	}
}

func TestIssuePrefetchPlanSkipsDisabledAndAutonomousPromotion(t *testing.T) {
	cfg := &config.Config{Autonomous: config.AutonomousConfig{Enabled: true}}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{
		Enabled:       true,
		Assignees:     []string{"alice"},
		BlockedLabels: []string{"blocked"},
	}
	cfgRef := cfg
	a := &tier2Adapter{cfg: &cfgRef}
	if got, configured := a.IssuePrefetchPlan([]string{"org/auto"}, nil); configured || len(got) != 0 {
		t.Fatalf("autonomous plan = (%v, %v), want ([], false)", got, configured)
	}

	cfg.Autonomous.Enabled = false
	cfg.GitHub.IssueTracking.Enabled = false
	if got, configured := a.IssuePrefetchPlan([]string{"org/disabled"}, nil); configured || len(got) != 0 {
		t.Fatalf("disabled plan = (%v, %v), want ([], false)", got, configured)
	}

	cfg.GitHub.IssueTracking.Enabled = true
	cfg.GitHub.IssueTracking.BlockedLabels = nil
	if got, configured := a.IssuePrefetchPlan([]string{"org/unblocked"}, nil); configured || len(got) != 0 {
		t.Fatalf("unblocked plan = (%v, %v), want ([], false)", got, configured)
	}
}

func TestIssueRepoNeedsCoreFetchGuards(t *testing.T) {
	if (*tier2Adapter)(nil).IssueRepoNeedsCoreFetch("org/repo") {
		t.Fatal("nil adapter should not request a core permit")
	}

	cfg := &config.Config{}
	cfgRef := cfg
	a := &tier2Adapter{cfg: &cfgRef}
	if a.IssueRepoNeedsCoreFetch("org/disabled") {
		t.Fatal("disabled issue tracking should not request a core permit")
	}

	cfg.GitHub.IssueTracking.Enabled = true
	cfg.GitHub.IssueTracking.Assignees = []string{"alice"}
	cfg.Autonomous.Enabled = true
	if a.IssueRepoNeedsCoreFetch("org/auto") {
		t.Fatal("autonomous repo should not request a legacy core permit")
	}

	cfg.Autonomous.Enabled = false
	if !a.IssueRepoNeedsCoreFetch("org/fallback") {
		t.Fatal("eligible repo without a fetcher should request a core permit")
	}

	a.ClearIssuePrefetch() // nil fetcher is intentionally a no-op.
}

func TestPrefetchIssuesForCycleWithoutFetcherOrConfigMutex(t *testing.T) {
	(&tier2Adapter{}).PrefetchIssuesForCycle([]string{"org/no-fetcher"})

	cfg := &config.Config{}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{Enabled: true, Assignees: []string{"alice"}}
	cfgRef := cfg
	login := "alice"
	searcher := &emptyIssueSearcher{}
	fetcher := issuepipeline.NewFetcher(nil, nil, nil, nil)
	fetcher.SetSearcher(searcher)
	a := &tier2Adapter{fetcher: fetcher, cfg: &cfgRef, login: &login}

	a.PrefetchIssuesForCycle([]string{"org/repo"})
	if searcher.calls != 1 {
		t.Fatalf("search calls = %d, want 1", searcher.calls)
	}
}

func TestPromoteReadyReadsFetcherSnapshotWithNoGroups(t *testing.T) {
	cfg := &config.Config{}
	cfgRef := cfg
	var cfgMu sync.Mutex
	a := &tier2Adapter{
		cfg:     &cfgRef,
		cfgMu:   &cfgMu,
		fetcher: issuepipeline.NewFetcher(nil, nil, nil, nil),
	}

	if n, err := a.PromoteReady(t.Context(), nil); err != nil || n != 0 {
		t.Fatalf("PromoteReady() = (%d, %v), want (0, nil)", n, err)
	}
}
