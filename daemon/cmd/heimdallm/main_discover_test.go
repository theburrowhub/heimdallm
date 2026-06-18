package main

import (
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

// A repo with an explicit [ai.repos.*] entry must NEVER be added to
// NonMonitored — even when AutoEnablePRForDiscovery is off. Otherwise the next
// MergeRepos call would blacklist a repo the operator just configured, which
// is exactly the regression described in theburrowhub/heimdallm#281.
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

// theburrowhub/heimdallm#527 item 1: a repo blacklisted in NonMonitored on a
// prior tick that later gains explicit [ai.repos.*] config must be promoted —
// stripped from NonMonitored and added to Repositories — so config state stays
// consistent for code that reads NonMonitored directly (not just MergeRepos,
// which already exempts it). The promotion is reported in `added` so the
// updated lists are persisted.
func TestUpsertDiscoveredRepos_PromotesExplicitConfigOutOfNonMonitored(t *testing.T) {
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
	if len(added) != 1 || added[0] != "a/wired-up" {
		t.Fatalf("promoted repo must be reported in added (so lists persist), got %v", added)
	}

	// Must now be in Repositories.
	foundWired := false
	for _, r := range cfg.GitHub.Repositories {
		if r == "a/wired-up" {
			foundWired = true
		}
	}
	if !foundWired {
		t.Fatalf("a/wired-up must be promoted to Repositories, got %v", cfg.GitHub.Repositories)
	}

	// Must be stripped from NonMonitored, while the unrelated blacklist row stays.
	wantNonMon := []string{"a/keep-blacklisted"}
	if len(cfg.GitHub.NonMonitored) != len(wantNonMon) || cfg.GitHub.NonMonitored[0] != wantNonMon[0] {
		t.Fatalf("NonMonitored = %v, want %v (wired-up stripped, other kept)", cfg.GitHub.NonMonitored, wantNonMon)
	}
}

// Edge case (review of #527): a repo that is SIMULTANEOUSLY in Repositories and
// NonMonitored — the inconsistent prior-tick state this fix targets — must still
// have its NonMonitored strip reported in `added`, even though it is already
// monitored. Otherwise, if it is the only change in the tick, the empty `added`
// set makes processDiscoveredRepos early-return and the strip is never persisted.
func TestUpsertDiscoveredRepos_PromotionPersistsWhenAlreadyMonitored(t *testing.T) {
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

	// The strip MUST be reported so the updated lists get persisted.
	if len(added) != 1 || added[0] != "a/wired-up" {
		t.Fatalf("strip-only promotion must be reported in added (else it is not persisted), got %v", added)
	}
	// Stripped from NonMonitored.
	for _, r := range cfg.GitHub.NonMonitored {
		if r == "a/wired-up" {
			t.Fatalf("a/wired-up must be stripped from NonMonitored, got %v", cfg.GitHub.NonMonitored)
		}
	}
	// Still present in Repositories exactly once (no duplicate append).
	count := 0
	for _, r := range cfg.GitHub.Repositories {
		if r == "a/wired-up" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("a/wired-up must appear exactly once in Repositories, got %v", cfg.GitHub.Repositories)
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
