package main

import (
	"slices"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
)

func TestUpsertDiscoveredRepos_DefaultEnabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"a/known"}

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/a/known", Number: 1},
		{RepositoryURL: "https://api.github.com/repos/a/new", Number: 2},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 1 || added[0] != "a/new" {
		t.Fatalf("expected a/new added, got %v", added)
	}
	found := false
	for _, r := range cfg.GitHub.Repositories {
		if r == "a/new" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a/new should be appended to Repositories, got %v", cfg.GitHub.Repositories)
	}
}

func TestUpsertDiscoveredRepos_RespectsDisabledFlag(t *testing.T) {
	f := false
	cfg := &config.Config{}
	cfg.GitHub.AutoEnablePROnDiscovery = &f

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/a/new", Number: 1},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 1 {
		t.Fatalf("expected 1 added, got %v", added)
	}
	for _, r := range cfg.GitHub.Repositories {
		if r == "a/new" {
			t.Fatal("a/new must not be in Repositories when disabled")
		}
	}
	found := false
	for _, r := range cfg.GitHub.NonMonitored {
		if r == "a/new" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a/new should be in NonMonitored, got %v", cfg.GitHub.NonMonitored)
	}
}

func TestUpsertDiscoveredRepos_SkipsAlreadyKnown(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"a/one"}
	cfg.GitHub.NonMonitored = []string{"a/two"}

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/a/one", Number: 1},
		{RepositoryURL: "https://api.github.com/repos/a/two", Number: 2},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 0 {
		t.Fatalf("known repos should not be added, got %v", added)
	}
}

func TestUpsertDiscoveredRepos_IgnoresPRsWithEmptyRepo(t *testing.T) {
	cfg := &config.Config{}
	prs := []*gh.PullRequest{{Number: 42}} // RepositoryURL empty → Repo stays ""
	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 0 {
		t.Fatalf("PRs with empty Repo must be ignored, got %v", added)
	}
}

func TestUpsertDiscoveredRepos_FiltersOutsideDiscoveryOrgs(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.DiscoveryOrgs = []string{"overmind-swarm"}

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/overmind-swarm/backend", Number: 1},
		{RepositoryURL: "https://api.github.com/repos/freepik-company/fc-py-cogito", Number: 2},
		{RepositoryURL: "https://api.github.com/repos/other-org/repo", Number: 3},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 1 || added[0] != "overmind-swarm/backend" {
		t.Fatalf("expected only overmind-swarm/backend, got %v", added)
	}
}

func TestUpsertDiscoveredRepos_NoOrgFilterWhenDiscoveryOrgsEmpty(t *testing.T) {
	cfg := &config.Config{}
	// DiscoveryOrgs empty → all orgs accepted (backward compat)

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/any-org/repo", Number: 1},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 1 || added[0] != "any-org/repo" {
		t.Fatalf("without DiscoveryOrgs all repos should be accepted, got %v", added)
	}
}

func TestUpsertDiscoveredRepos_OrgFilterCaseInsensitive(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.DiscoveryOrgs = []string{"Overmind-Swarm"}

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/overmind-swarm/repo", Number: 1},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 1 {
		t.Fatalf("org filter should be case-insensitive, got %v", added)
	}
}

func TestIntersectMonitoredRepos_AppliesLiveDisableToStaleTier1Snapshot(t *testing.T) {
	current := []string{"org/keep", "org/just-disabled", "org/archived-elsewhere"}
	got := intersectMonitoredRepos(current, func() []string {
		return []string{"org/keep", "org/not-in-tier1"}
	})

	if len(got) != 1 || got[0] != "org/keep" {
		t.Fatalf("intersectMonitoredRepos = %v, want [org/keep]", got)
	}
}

// On a repo's first topic-discovery cycle Tier 1 can publish it before Tier 2
// persists the repo into NonMonitored (when auto-enable is off). The live
// intersection must turn that now-stale publication into an empty work set.
func TestIntersectMonitoredRepos_BlocksFirstCycleTopicRepoRecordedNonMonitored(t *testing.T) {
	const repo = "org/new-disabled"
	got := intersectMonitoredRepos([]string{repo}, func() []string { return nil })
	if len(got) != 0 {
		t.Fatalf("first-cycle non-monitored repo survived live gate: %v", got)
	}
}

func TestRepoIsMonitored_NonMonitoredOverridesEveryOptInSource(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"org/repo"}
	cfg.GitHub.NonMonitored = []string{"org/repo"}
	cfg.AI.Repos = map[string]config.RepoAI{"org/repo": {}}

	if repoIsMonitored(cfg, "org/repo") {
		t.Fatal("repoIsMonitored returned true for explicitly disabled repo")
	}
}

func TestEffectiveRepoListsExposeImplicitAIOptInsUnlessDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"org/static", "org/static-disabled"}
	cfg.GitHub.NonMonitored = []string{"org/static-disabled", "org/ai-disabled"}
	cfg.AI.Repos = map[string]config.RepoAI{
		"org/ai-enabled":  {},
		"org/ai-disabled": {},
	}

	monitored, nonMonitored := effectiveRepoLists(cfg)
	if want := []string{"org/static", "org/ai-enabled"}; !slices.Equal(monitored, want) {
		t.Fatalf("monitored = %v, want %v", monitored, want)
	}
	if !slices.Equal(nonMonitored, cfg.GitHub.NonMonitored) {
		t.Fatalf("nonMonitored = %v, want %v", nonMonitored, cfg.GitHub.NonMonitored)
	}
}

// A previously unknown repo with an explicit [ai.repos.*] entry is an implicit
// opt-in even when auto-enable is off. This does not apply once the operator
// has explicitly put the repo in NonMonitored (covered below).
func TestUpsertDiscoveredRepos_ExplicitAIConfigOverridesAutoEnableOff(t *testing.T) {
	f := false
	cfg := &config.Config{}
	cfg.GitHub.AutoEnablePROnDiscovery = &f
	cfg.AI.Repos = map[string]config.RepoAI{
		"a/wired-up": {},
	}

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/a/wired-up", Number: 1},
		{RepositoryURL: "https://api.github.com/repos/a/no-config", Number: 2},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 2 {
		t.Fatalf("both repos should be added, got %v", added)
	}

	// The wired-up repo must land in Repositories despite AutoEnable being off.
	foundWired := false
	for _, r := range cfg.GitHub.Repositories {
		if r == "a/wired-up" {
			foundWired = true
		}
	}
	if !foundWired {
		t.Fatalf("a/wired-up must be in Repositories (explicit [ai.repos.*] config), got %v", cfg.GitHub.Repositories)
	}

	// The wired-up repo must NOT be in NonMonitored — that would let MergeRepos blacklist it.
	for _, r := range cfg.GitHub.NonMonitored {
		if r == "a/wired-up" {
			t.Fatalf("a/wired-up must NOT be in NonMonitored — explicit TOML config wins over auto-enable=false")
		}
	}

	// The repo without explicit AI config still follows AutoEnablePRForDiscovery.
	foundUnwired := false
	for _, r := range cfg.GitHub.NonMonitored {
		if r == "a/no-config" {
			foundUnwired = true
		}
	}
	if !foundUnwired {
		t.Fatalf("a/no-config should be in NonMonitored (no explicit config + auto-enable off), got %v", cfg.GitHub.NonMonitored)
	}
}

// NonMonitored is explicit operator intent and must win over an [ai.repos.*]
// override. Before this regression fix, seeing a review-requested PR silently
// stripped the disable and re-enabled the repository.
func TestUpsertDiscoveredRepos_NonMonitoredOverridesExplicitConfig(t *testing.T) {
	f := false
	cfg := &config.Config{}
	cfg.GitHub.AutoEnablePROnDiscovery = &f
	cfg.GitHub.NonMonitored = []string{"a/keep-blacklisted", "a/wired-up"}
	cfg.AI.Repos = map[string]config.RepoAI{
		"a/wired-up": {},
	}

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/a/wired-up", Number: 1},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 0 {
		t.Fatalf("explicitly disabled repo must not be auto-added, got %v", added)
	}

	for _, r := range cfg.GitHub.Repositories {
		if r == "a/wired-up" {
			t.Fatalf("a/wired-up must remain out of Repositories, got %v", cfg.GitHub.Repositories)
		}
	}

	wantNonMon := []string{"a/keep-blacklisted", "a/wired-up"}
	if len(cfg.GitHub.NonMonitored) != len(wantNonMon) {
		t.Fatalf("NonMonitored = %v, want %v", cfg.GitHub.NonMonitored, wantNonMon)
	}
	for i := range wantNonMon {
		if cfg.GitHub.NonMonitored[i] != wantNonMon[i] {
			t.Fatalf("NonMonitored = %v, want %v", cfg.GitHub.NonMonitored, wantNonMon)
		}
	}
}

// Even an inconsistent legacy row present in both lists must not have its
// disable erased by PR discovery. MergeRepos gives NonMonitored precedence;
// config normalization is deliberately left to the config writer.
func TestUpsertDiscoveredRepos_DoesNotEraseNonMonitoredFromLegacyConflict(t *testing.T) {
	f := false
	cfg := &config.Config{}
	cfg.GitHub.AutoEnablePROnDiscovery = &f
	cfg.GitHub.Repositories = []string{"a/wired-up"}
	cfg.GitHub.NonMonitored = []string{"a/wired-up"} // inconsistent: present in both
	cfg.AI.Repos = map[string]config.RepoAI{
		"a/wired-up": {},
	}

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/a/wired-up", Number: 1},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)

	if len(added) != 0 {
		t.Fatalf("legacy conflict must not trigger auto-promotion, got %v", added)
	}
	// The authoritative disable remains intact.
	foundDisabled := false
	for _, r := range cfg.GitHub.NonMonitored {
		if r == "a/wired-up" {
			foundDisabled = true
		}
	}
	if !foundDisabled {
		t.Fatalf("a/wired-up must remain in NonMonitored, got %v", cfg.GitHub.NonMonitored)
	}
}

// theburrowhub/heimdallm#527 item 2: explicit [ai.repos.*] config is operator
// intent and must bypass the discovery_orgs allowlist. A configured repo whose
// org is outside discovery_orgs must still land in Repositories via upsert,
// while a non-configured repo in the same out-of-list org is still filtered.
func TestUpsertDiscoveredRepos_ExplicitConfigBypassesOrgFilter(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.DiscoveryOrgs = []string{"allowed-org"}
	cfg.AI.Repos = map[string]config.RepoAI{
		"other-org/wired-up": {},
	}

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/other-org/wired-up", Number: 1},
		{RepositoryURL: "https://api.github.com/repos/other-org/not-configured", Number: 2},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	added := upsertDiscoveredRepos(cfg, prs)
	if len(added) != 1 || added[0] != "other-org/wired-up" {
		t.Fatalf("only the explicitly-configured out-of-org repo should be added, got %v", added)
	}

	foundWired := false
	for _, r := range cfg.GitHub.Repositories {
		if r == "other-org/wired-up" {
			foundWired = true
		}
	}
	if !foundWired {
		t.Fatalf("other-org/wired-up must be in Repositories (explicit config bypasses org filter), got %v", cfg.GitHub.Repositories)
	}

	// The non-configured out-of-org repo must not appear in either list.
	for _, r := range cfg.GitHub.Repositories {
		if r == "other-org/not-configured" {
			t.Fatal("other-org/not-configured must be filtered by discovery_orgs")
		}
	}
	for _, r := range cfg.GitHub.NonMonitored {
		if r == "other-org/not-configured" {
			t.Fatal("other-org/not-configured must not be added to NonMonitored either")
		}
	}
}
