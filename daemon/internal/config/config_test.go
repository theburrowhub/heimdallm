package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

func testBoolPtr(v bool) *bool { return &v }

// ── applyDefaults ────────────────────────────────────────────────────────────

func TestApplyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	if cfg.Server.Port != 7842 {
		t.Errorf("Port = %d, want 7842", cfg.Server.Port)
	}
	if cfg.Server.BindAddr != "127.0.0.1" {
		t.Errorf("BindAddr = %q, want %q", cfg.Server.BindAddr, "127.0.0.1")
	}
	if cfg.GitHub.PollInterval != "5m" {
		t.Errorf("PollInterval = %q, want %q", cfg.GitHub.PollInterval, "5m")
	}
	if cfg.Retention.MaxDays != 90 {
		t.Errorf("MaxDays = %d, want 90", cfg.Retention.MaxDays)
	}
	if cfg.AI.ReviewMode != "single" {
		t.Errorf("ReviewMode = %q, want %q", cfg.AI.ReviewMode, "single")
	}
	if cfg.AI.ExecutionTimeout != DefaultAIExecutionTimeout {
		t.Errorf("ExecutionTimeout = %q, want %q", cfg.AI.ExecutionTimeout, DefaultAIExecutionTimeout)
	}
	if parsed, err := time.ParseDuration(cfg.AI.ExecutionTimeout); err != nil {
		t.Fatalf("parse default ExecutionTimeout: %v", err)
	} else if parsed != executor.DefaultExecutionTimeout {
		t.Errorf("config ExecutionTimeout = %v, executor default = %v", parsed, executor.DefaultExecutionTimeout)
	}
	if cfg.CircuitBreaker.PerReviewFailureRepoHr != 20 {
		t.Errorf("PerReviewFailureRepoHr = %d, want 20", cfg.CircuitBreaker.PerReviewFailureRepoHr)
	}
}

func TestDefaultCircuitBreakerConfigIncludesReviewFailureLimit(t *testing.T) {
	if got := DefaultCircuitBreakerConfig().PerReviewFailureRepoHr; got != 20 {
		t.Fatalf("PerReviewFailureRepoHr = %d, want 20", got)
	}
}

func TestApplyDefaults_PreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.Server.Port = 9999
	cfg.Server.BindAddr = "0.0.0.0"
	cfg.GitHub.PollInterval = "1m"
	cfg.Retention.MaxDays = 30
	cfg.AI.ReviewMode = "multi"
	cfg.AI.ExecutionTimeout = "30m"
	cfg.CircuitBreaker.PerReviewFailureRepoHr = 37

	cfg.applyDefaults()

	if cfg.Server.Port != 9999 {
		t.Errorf("Port overwritten: %d", cfg.Server.Port)
	}
	if cfg.Server.BindAddr != "0.0.0.0" {
		t.Errorf("BindAddr overwritten: %q", cfg.Server.BindAddr)
	}
	if cfg.GitHub.PollInterval != "1m" {
		t.Errorf("PollInterval overwritten: %q", cfg.GitHub.PollInterval)
	}
	if cfg.Retention.MaxDays != 30 {
		t.Errorf("MaxDays overwritten: %d", cfg.Retention.MaxDays)
	}
	if cfg.AI.ReviewMode != "multi" {
		t.Errorf("ReviewMode overwritten: %q", cfg.AI.ReviewMode)
	}
	if cfg.AI.ExecutionTimeout != "30m" {
		t.Errorf("ExecutionTimeout overwritten: %q", cfg.AI.ExecutionTimeout)
	}
	if cfg.CircuitBreaker.PerReviewFailureRepoHr != 37 {
		t.Errorf("PerReviewFailureRepoHr overwritten: %d", cfg.CircuitBreaker.PerReviewFailureRepoHr)
	}
}

func TestApplyDefaults_MaxWorktreesPerRepo(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.AI.MaxWorktreesPerRepo != 5 {
		t.Errorf("MaxWorktreesPerRepo = %d, want 5", cfg.AI.MaxWorktreesPerRepo)
	}
}

func TestApplyDefaults_MaxWorktreesPerRepo_PreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.AI.MaxWorktreesPerRepo = 12
	cfg.applyDefaults()
	if cfg.AI.MaxWorktreesPerRepo != 12 {
		t.Errorf("MaxWorktreesPerRepo overwritten: %d", cfg.AI.MaxWorktreesPerRepo)
	}
}

// ── applyEnvOverrides ────────────────────────────────────────────────────────

func TestApplyDefaults_MaxConcurrentWorkers(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.Server.MaxConcurrentWorkers != 5 {
		t.Errorf("MaxConcurrentWorkers = %d, want 5", cfg.Server.MaxConcurrentWorkers)
	}
}

func TestApplyDefaults_MaxConcurrentWorkers_PreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.Server.MaxConcurrentWorkers = 10
	cfg.applyDefaults()
	if cfg.Server.MaxConcurrentWorkers != 10 {
		t.Errorf("MaxConcurrentWorkers overwritten: %d", cfg.Server.MaxConcurrentWorkers)
	}
}

func TestEnvOverride_MaxConcurrentWorkers(t *testing.T) {
	t.Setenv("HEIMDALLM_MAX_CONCURRENT_WORKERS", "8")
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.applyEnvOverrides()
	if cfg.Server.MaxConcurrentWorkers != 8 {
		t.Errorf("MaxConcurrentWorkers = %d, want 8", cfg.Server.MaxConcurrentWorkers)
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	t.Setenv("HEIMDALLM_PORT", "8080")
	t.Setenv("HEIMDALLM_BIND_ADDR", "0.0.0.0")
	t.Setenv("HEIMDALLM_POLL_INTERVAL", "1m")
	t.Setenv("HEIMDALLM_REPOSITORIES", "org/repo1, org/repo2, org/repo3")
	t.Setenv("HEIMDALLM_AI_PRIMARY", "gemini")
	t.Setenv("HEIMDALLM_AI_FALLBACK", "claude")
	t.Setenv("HEIMDALLM_REVIEW_MODE", "multi")
	t.Setenv("HEIMDALLM_RETENTION_DAYS", "30")

	cfg.applyEnvOverrides()

	if cfg.Server.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.BindAddr != "0.0.0.0" {
		t.Errorf("BindAddr = %q, want %q", cfg.Server.BindAddr, "0.0.0.0")
	}
	if cfg.GitHub.PollInterval != "1m" {
		t.Errorf("PollInterval = %q, want %q", cfg.GitHub.PollInterval, "1m")
	}
	if len(cfg.GitHub.Repositories) != 3 {
		t.Fatalf("Repositories = %v, want 3 items", cfg.GitHub.Repositories)
	}
	if cfg.GitHub.Repositories[1] != "org/repo2" {
		t.Errorf("Repositories[1] = %q, want %q", cfg.GitHub.Repositories[1], "org/repo2")
	}
	if cfg.AI.Primary != "gemini" {
		t.Errorf("Primary = %q, want %q", cfg.AI.Primary, "gemini")
	}
	if cfg.AI.Fallback != "claude" {
		t.Errorf("Fallback = %q, want %q", cfg.AI.Fallback, "claude")
	}
	if cfg.AI.ReviewMode != "multi" {
		t.Errorf("ReviewMode = %q, want %q", cfg.AI.ReviewMode, "multi")
	}
	if cfg.Retention.MaxDays != 30 {
		t.Errorf("MaxDays = %d, want 30", cfg.Retention.MaxDays)
	}
}

func TestApplyEnvOverrides_InvalidPort(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	t.Setenv("HEIMDALLM_PORT", "notanumber")
	cfg.applyEnvOverrides()

	if cfg.Server.Port != 7842 {
		t.Errorf("Port = %d, should stay default 7842 on invalid input", cfg.Server.Port)
	}
}

func TestApplyEnvOverrides_EmptyRepositories(t *testing.T) {
	cfg := &Config{}
	cfg.GitHub.Repositories = []string{"existing/repo"}

	t.Setenv("HEIMDALLM_REPOSITORIES", "  ,  ,  ")
	cfg.applyEnvOverrides()

	if len(cfg.GitHub.Repositories) != 1 {
		t.Errorf("Repositories = %v, expected original preserved", cfg.GitHub.Repositories)
	}
}

// ── Validate ─────────────────────────────────────────────────────────────────

func TestValidate_MissingPrimary(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() = nil, want error for missing ai.primary")
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidate_InvalidPollInterval(t *testing.T) {
	for _, interval := range []string{
		"nonsense", // unparseable
		"30s",      // below the 1m floor
		"0",        // zero
		"-5m",      // negative
		"48h",      // above the 24h ceiling
	} {
		cfg := &Config{}
		cfg.applyDefaults()
		cfg.AI.Primary = "claude"
		cfg.GitHub.PollInterval = interval

		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate() with interval %q = nil, want error", interval)
		}
	}
}

func TestValidate_AllValidIntervals(t *testing.T) {
	// Any time.ParseDuration value within [1m, 24h] is accepted, including
	// arbitrary values like 3m that the old discrete whitelist rejected.
	for _, interval := range []string{"1m", "3m", "5m", "10m", "30m", "90m", "1h", "12h", "24h"} {
		cfg := &Config{AI: AIConfig{Primary: "claude"}, GitHub: GitHubConfig{PollInterval: interval}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with interval %q = %v", interval, err)
		}
	}
}

func TestValidate_RetentionMaxDaysOutOfRange(t *testing.T) {
	for _, days := range []int{-1, -365, 3651} {
		t.Run(fmt.Sprintf("days=%d", days), func(t *testing.T) {
			cfg := &Config{}
			cfg.applyDefaults()
			cfg.AI.Primary = "claude"
			cfg.Retention.MaxDays = days // set after applyDefaults, which coerces 0 -> 90

			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() with retention.max_days=%d = nil, want error", days)
			}
		})
	}
}

func TestValidate_RetentionMaxDaysValid(t *testing.T) {
	for _, days := range []int{0, 1, 90, 3650} {
		t.Run(fmt.Sprintf("days=%d", days), func(t *testing.T) {
			cfg := &Config{}
			cfg.applyDefaults()
			cfg.AI.Primary = "claude"
			cfg.Retention.MaxDays = days

			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() with retention.max_days=%d = %v, want nil", days, err)
			}
		})
	}
}

// Regression for #551: a negative HEIMDALLM_RETENTION_DAYS must be rejected at
// load time rather than silently flowing into PurgeOldReviews (whose cutoff
// would land in the future and wipe the entire review history).
func TestApplyEnvOverrides_NegativeRetentionRejectedByValidate(t *testing.T) {
	t.Setenv("HEIMDALLM_RETENTION_DAYS", "-1")
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"
	cfg.applyEnvOverrides()

	if cfg.Retention.MaxDays != -1 {
		t.Fatalf("env override not applied: MaxDays = %d, want -1", cfg.Retention.MaxDays)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() = nil for HEIMDALLM_RETENTION_DAYS=-1, want error")
	}
}

// ── Topic-based discovery ────────────────────────────────────────────────────

func TestApplyDefaults_DiscoveryIntervalUnsetWhenTopicSet(t *testing.T) {
	cfg := &Config{}
	cfg.GitHub.DiscoveryTopic = "heimdallm-review"
	cfg.applyDefaults()

	if cfg.GitHub.DiscoveryInterval != "" {
		t.Errorf("DiscoveryInterval = %q, want empty runtime fallback", cfg.GitHub.DiscoveryInterval)
	}
}

func TestApplyDefaults_NoDiscoveryIntervalWhenTopicUnset(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	if cfg.GitHub.DiscoveryInterval != "" {
		t.Errorf("DiscoveryInterval = %q, want empty when topic unset", cfg.GitHub.DiscoveryInterval)
	}
}

func TestApplyDefaults_PreservesDiscoveryInterval(t *testing.T) {
	cfg := &Config{}
	cfg.GitHub.DiscoveryTopic = "heimdallm-review"
	cfg.GitHub.DiscoveryInterval = "30m"
	cfg.applyDefaults()

	if cfg.GitHub.DiscoveryInterval != "30m" {
		t.Errorf("DiscoveryInterval overwritten: %q", cfg.GitHub.DiscoveryInterval)
	}
}

func TestApplyEnvOverrides_Discovery(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	t.Setenv("HEIMDALLM_DISCOVERY_TOPIC", "heimdallm-review")
	t.Setenv("HEIMDALLM_DISCOVERY_ORGS", "freepik-company, theburrowhub ,  ")
	t.Setenv("HEIMDALLM_DISCOVERY_INTERVAL", "10m")

	cfg.applyEnvOverrides()

	if cfg.GitHub.DiscoveryTopic != "heimdallm-review" {
		t.Errorf("DiscoveryTopic = %q", cfg.GitHub.DiscoveryTopic)
	}
	if len(cfg.GitHub.DiscoveryOrgs) != 2 {
		t.Fatalf("DiscoveryOrgs = %v, want 2 entries", cfg.GitHub.DiscoveryOrgs)
	}
	if cfg.GitHub.DiscoveryOrgs[0] != "freepik-company" || cfg.GitHub.DiscoveryOrgs[1] != "theburrowhub" {
		t.Errorf("DiscoveryOrgs = %v", cfg.GitHub.DiscoveryOrgs)
	}
	if cfg.GitHub.DiscoveryInterval != "10m" {
		t.Errorf("DiscoveryInterval = %q", cfg.GitHub.DiscoveryInterval)
	}
}

func TestApplyEnvOverrides_DiscoveryOrgs_AllBlankPreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.GitHub.DiscoveryOrgs = []string{"existing-org"}

	t.Setenv("HEIMDALLM_DISCOVERY_ORGS", "  ,  ,  ")
	cfg.applyEnvOverrides()

	if len(cfg.GitHub.DiscoveryOrgs) != 1 || cfg.GitHub.DiscoveryOrgs[0] != "existing-org" {
		t.Errorf("DiscoveryOrgs should keep the existing value when env is all blank, got %v", cfg.GitHub.DiscoveryOrgs)
	}
}

func TestValidate_DiscoveryDisabled(t *testing.T) {
	cfg := &Config{AI: AIConfig{Primary: "claude"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with no discovery = %v", err)
	}
}

func TestValidate_DiscoveryTopicRequiresOrgs(t *testing.T) {
	cfg := &Config{
		AI:     AIConfig{Primary: "claude"},
		GitHub: GitHubConfig{DiscoveryTopic: "heimdallm-review"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() with discovery_topic but no orgs = nil, want error")
	}
	if !strings.Contains(err.Error(), "discovery_orgs") {
		t.Errorf("error should mention discovery_orgs, got: %v", err)
	}
}

func TestValidate_DiscoveryTopicInvalidFormat(t *testing.T) {
	cases := []struct {
		name  string
		topic string
	}{
		{"uppercase", "Heimdallm-Review"},
		{"starts with hyphen", "-heimdallm"},
		{"contains space", "heimdallm review"},
		{"too long", strings.Repeat("a", 51)},
		{"underscore", "heimdallm_review"},
		{"empty after hyphen", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.topic == "" {
				t.Skip("empty topic disables discovery; covered elsewhere")
			}
			cfg := &Config{
				AI: AIConfig{Primary: "claude"},
				GitHub: GitHubConfig{
					DiscoveryTopic: tc.topic,
					DiscoveryOrgs:  []string{"some-org"},
				},
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate(topic=%q) = nil, want error", tc.topic)
			}
		})
	}
}

func TestValidate_DiscoveryTopicValidFormats(t *testing.T) {
	cases := []string{
		"heimdallm-review",
		"a",
		"123",
		"a-b-c-d",
		strings.Repeat("a", 50),
	}
	for _, topic := range cases {
		cfg := &Config{
			AI: AIConfig{Primary: "claude"},
			GitHub: GitHubConfig{
				DiscoveryTopic: topic,
				DiscoveryOrgs:  []string{"some-org"},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(topic=%q) = %v, want nil", topic, err)
		}
	}
}

func TestValidate_DiscoveryIntervalInvalid(t *testing.T) {
	cases := []string{"not-a-duration", "-5m", "0"}
	for _, interval := range cases {
		cfg := &Config{
			AI: AIConfig{Primary: "claude"},
			GitHub: GitHubConfig{
				DiscoveryTopic:    "heimdallm-review",
				DiscoveryOrgs:     []string{"some-org"},
				DiscoveryInterval: interval,
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate(interval=%q) = nil, want error", interval)
		}
	}
}

func TestValidate_DiscoveryOrgsInvalid(t *testing.T) {
	cases := []struct {
		name string
		org  string
	}{
		{"contains space", "freepik company"},
		{"contains slash", "org/subpath"},
		{"search qualifier injection", "evil archived:false org:other"},
		{"starts with hyphen", "-freepik"},
		{"ends with hyphen", "freepik-"},
		{"contains underscore", "free_pik"},
		{"too long", strings.Repeat("a", 40)},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				AI: AIConfig{Primary: "claude"},
				GitHub: GitHubConfig{
					DiscoveryTopic: "heimdallm-review",
					DiscoveryOrgs:  []string{"valid-org", tc.org},
				},
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate(org=%q) = nil, want error", tc.org)
			}
			if !strings.Contains(err.Error(), "discovery_orgs") {
				t.Errorf("error should mention discovery_orgs, got: %v", err)
			}
		})
	}
}

func TestValidate_DiscoveryOrgsValid(t *testing.T) {
	cases := []string{
		"freepik-company",
		"theburrowhub",
		"a",
		"A1",
		"1a",
		strings.Repeat("a", 39),
	}
	for _, org := range cases {
		cfg := &Config{
			AI: AIConfig{Primary: "claude"},
			GitHub: GitHubConfig{
				DiscoveryTopic: "heimdallm-review",
				DiscoveryOrgs:  []string{org},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(org=%q) = %v, want nil", org, err)
		}
	}
}

// ── Issue tracking ───────────────────────────────────────────────────────────

func TestValidate_InvalidOrgOverrideKey(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary: "claude",
			Orgs: map[string]OrgAI{
				"bad org": {Primary: "gemini"},
			},
		},
	}
	cfg.applyDefaults()

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate with invalid org key = nil, want error")
	}
}

// ── Issue classification ─────────────────────────────────────────────────────

// ── AIForRepo ────────────────────────────────────────────────────────────────

func TestAIForRepo_GlobalFallback(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{Primary: "claude", Fallback: "gemini", ReviewMode: "single"},
	}

	r := cfg.AIForRepo("unknown/repo")
	if r.Primary != "claude" {
		t.Errorf("Primary = %q, want %q", r.Primary, "claude")
	}
	if r.Fallback != "gemini" {
		t.Errorf("Fallback = %q, want %q", r.Fallback, "gemini")
	}
	if r.ReviewMode != "single" {
		t.Errorf("ReviewMode = %q, want %q", r.ReviewMode, "single")
	}
}

func TestAIForRepo_PerRepo(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:  "claude",
			Fallback: "gemini",
			Repos: map[string]RepoAI{
				"org/special": {Primary: "codex", LocalDir: "/data/repos/special"},
			},
		},
	}

	r := cfg.AIForRepo("org/special")
	if r.Primary != "codex" {
		t.Errorf("Primary = %q, want %q", r.Primary, "codex")
	}
	if r.Fallback != "gemini" {
		t.Error("Fallback should inherit from global when not set per-repo")
	}
	if r.LocalDir != "/data/repos/special" {
		t.Errorf("LocalDir = %q", r.LocalDir)
	}
}

func TestAIForRepo_PerRepoInheritsGlobal(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:    "claude",
			Fallback:   "gemini",
			ReviewMode: "multi",
			Repos: map[string]RepoAI{
				"org/repo": {},
			},
		},
	}

	r := cfg.AIForRepo("org/repo")
	if r.Primary != "claude" {
		t.Errorf("Primary = %q, want global fallback %q", r.Primary, "claude")
	}
	if r.Fallback != "gemini" {
		t.Errorf("Fallback = %q, want global fallback %q", r.Fallback, "gemini")
	}
	if r.ReviewMode != "multi" {
		t.Errorf("ReviewMode = %q, want global fallback %q", r.ReviewMode, "multi")
	}
}

// ── AIForRepo 3-level resolution ────────────────────────────────────────────

func TestAIForRepo_OrgOverridesAgentSelectionAndPrompts(t *testing.T) {
	orgNever := true
	cfg := &Config{
		AI: AIConfig{
			Primary:    "claude",
			Fallback:   "gemini",
			ReviewMode: "single",
			CloneDir:   "/tmp/global-clones",
			Orgs: map[string]OrgAI{
				"myorg": {
					Primary:                 "codex",
					Fallback:                "opencode",
					ReviewMode:              "multi",
					Prompt:                  "org-pr",
					CloneDir:                "/tmp/org-clones",
					NeverApproveWithIssues:  &orgNever,
					NeverApproveMinSeverity: "high",
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/repo")
	if r.Primary != "codex" || r.Fallback != "opencode" || r.ReviewMode != "multi" {
		t.Fatalf("agent selection = (%q,%q,%q), want org values", r.Primary, r.Fallback, r.ReviewMode)
	}
	if r.Prompt != "org-pr" || r.CloneDir != "/tmp/org-clones" {
		t.Fatalf("prompt/clone = (%q,%q), want org values", r.Prompt, r.CloneDir)
	}
	if r.NeverApproveWithIssues == nil || !*r.NeverApproveWithIssues || r.NeverApproveMinSeverity != "high" {
		t.Fatalf("never-approve = (%v,%q), want org true/high", r.NeverApproveWithIssues, r.NeverApproveMinSeverity)
	}
}

func TestAIForRepo_RepoOverridesOrgAgentSelectionAndPrompts(t *testing.T) {
	orgNever := true
	repoNever := false
	cfg := &Config{
		AI: AIConfig{
			Primary: "claude",
			Orgs: map[string]OrgAI{
				"myorg": {
					Primary:                "codex",
					Prompt:                 "org-pr",
					NeverApproveWithIssues: &orgNever,
				},
			},
			Repos: map[string]RepoAI{
				"myorg/repo": {
					Primary:                "gemini",
					Prompt:                 "repo-pr",
					NeverApproveWithIssues: &repoNever,
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/repo")
	if r.Primary != "gemini" || r.Prompt != "repo-pr" {
		t.Fatalf("repo fields did not override org: %+v", r)
	}
	if r.NeverApproveWithIssues == nil || *r.NeverApproveWithIssues {
		t.Fatal("repo NeverApproveWithIssues=false should override org true")
	}
}

func TestAIForRepo_IndependentFieldResolution(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:            "claude",
			Fallback:           "gemini",
			InstructionAuthors: []string{"global-lead"},
			Orgs: map[string]OrgAI{
				"myorg": {
					Fallback: "codex",
				},
			},
			Repos: map[string]RepoAI{
				"myorg/special": {
					InstructionAuthors: []string{"repo-lead"},
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/special")
	if r.Primary != "claude" {
		t.Errorf("Primary = %q, want claude (global, no org/repo override)", r.Primary)
	}
	if r.Fallback != "codex" {
		t.Errorf("Fallback = %q, want codex (org level, no repo override)", r.Fallback)
	}
	if len(r.InstructionAuthors) != 1 || r.InstructionAuthors[0] != "repo-lead" {
		t.Errorf("InstructionAuthors = %v, want [repo-lead] (repo level)", r.InstructionAuthors)
	}
}

func TestAIForRepo_NoOrgMatch_FallsToGlobal(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary: "claude",
			Orgs: map[string]OrgAI{
				"differentorg": {Primary: "codex"},
			},
		},
	}

	r := cfg.AIForRepo("myorg/repo")
	if r.Primary != "claude" {
		t.Errorf("Primary = %q, want claude (no org match)", r.Primary)
	}
}

// An empty (non-nil) InstructionAuthors list at a narrower scope is an
// explicit override: it clears the inherited allowlist instead of inheriting.
func TestAIForRepo_EmptyInstructionAuthorsClearsInherited(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:            "claude",
			InstructionAuthors: []string{"global-lead"},
			Repos: map[string]RepoAI{
				"myorg/repo": {InstructionAuthors: []string{}},
			},
		},
	}
	if got := cfg.AIForRepo("myorg/repo").InstructionAuthors; len(got) != 0 {
		t.Errorf("InstructionAuthors = %v, want empty (explicit repo override)", got)
	}
}

func TestAIForRepo_TOMLThreeLevelResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[ai]
primary = "claude"
fallback = "gemini"
instruction_authors = ["global-lead"]

[ai.orgs."freepik-company"]
primary = "codex"
review_mode = "multi"

[ai.orgs."theburrowhub"]
fallback = "opencode"

[ai.repos."freepik-company/data_contracts"]
primary = "gemini"
instruction_authors = ["data-lead"]
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Per-repo override
	r := cfg.AIForRepo("freepik-company/data_contracts")
	if r.Primary != "gemini" {
		t.Errorf("Primary = %q, want gemini (per-repo)", r.Primary)
	}
	if r.ReviewMode != "multi" {
		t.Errorf("ReviewMode = %q, want multi (from org)", r.ReviewMode)
	}
	if len(r.InstructionAuthors) != 1 || r.InstructionAuthors[0] != "data-lead" {
		t.Errorf("InstructionAuthors = %v, want [data-lead] (per-repo)", r.InstructionAuthors)
	}

	// Org-level (no per-repo)
	r2 := cfg.AIForRepo("freepik-company/other-repo")
	if r2.Primary != "codex" || r2.Fallback != "gemini" {
		t.Errorf("(Primary, Fallback) = (%q, %q), want (codex, gemini)", r2.Primary, r2.Fallback)
	}

	// Different org
	r3 := cfg.AIForRepo("theburrowhub/heimdallm")
	if r3.Primary != "claude" || r3.Fallback != "opencode" {
		t.Errorf("(Primary, Fallback) = (%q, %q), want (claude, opencode)", r3.Primary, r3.Fallback)
	}

	// Unknown org → global
	r4 := cfg.AIForRepo("unknown/repo")
	if len(r4.InstructionAuthors) != 1 || r4.InstructionAuthors[0] != "global-lead" {
		t.Errorf("InstructionAuthors = %v, want [global-lead] (global)", r4.InstructionAuthors)
	}
}

// Config files written before the issue pipelines were removed must keep
// loading: the dropped sections and keys are ignored, not rejected.
func TestLoad_IgnoresRemovedIssuePipelineKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[github]
repositories = ["org/repo"]

[github.issue_tracking]
enabled = true
develop_labels = ["develop"]
blocked_labels = ["blocked"]

[ai]
primary = "claude"
issue_prompt = "triage"
implement_prompt = "dev"
refinement_timeout = "30m"
triage_owner = "someone"
generate_pr_description = true
pr_reviewers = ["lead"]
tier2_repo_concurrency = 4

[ai.review_response]
enabled = true

[ai.orgs."org"]
issue_prompt = "org-triage"

[ai.orgs."org".issue_tracking]
develop_enabled = true

[ai.repos."org/repo"]
implement_prompt = "repo-dev"
pr_draft = true

[circuit_breaker]
per_issue_24h = 9
per_impl_repo_hr = 9

[autonomous]
enabled = true
auto_merge = true

[polling]
adaptive = true
min_interval = "1m"
max_interval = "15m"
use_graphql = true
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load with legacy issue-pipeline keys: %v", err)
	}
	if got := cfg.AIForRepo("org/repo").Primary; got != "claude" {
		t.Errorf("Primary = %q, want claude", got)
	}
	if cfg.CircuitBreaker.PerPR24h != 3 {
		t.Errorf("PerPR24h = %d, want the default 3", cfg.CircuitBreaker.PerPR24h)
	}
}

func TestRepoOrg(t *testing.T) {
	cases := map[string]string{
		"org/repo": "org",
		"a/b/c":    "a",
		"noslash":  "",
		"":         "",
		"/leading": "",
	}
	for input, want := range cases {
		if got := repoOrg(input); got != want {
			t.Errorf("repoOrg(%q) = %q, want %q", input, got, want)
		}
	}
}

// ── AgentConfigFor ───────────────────────────────────────────────────────────

func TestAgentConfigFor_Found(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Agents: map[string]CLIAgentConfig{
				"claude": {Model: "claude-opus-4-6", MaxTurns: 5},
			},
		},
	}

	ac := cfg.AgentConfigFor("claude")
	if ac.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q", ac.Model, "claude-opus-4-6")
	}
	if ac.MaxTurns != 5 {
		t.Errorf("MaxTurns = %d, want 5", ac.MaxTurns)
	}
}

func TestAgentConfigFor_NotFound(t *testing.T) {
	cfg := &Config{}
	ac := cfg.AgentConfigFor("unknown")
	if ac.Model != "" {
		t.Errorf("Model = %q, want empty", ac.Model)
	}
}

// ── Load ─────────────────────────────────────────────────────────────────────

func TestLoad_ValidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[server]
port = 9000
bind_addr = "0.0.0.0"

[github]
poll_interval = "1m"
repositories = ["org/repo"]

[ai]
primary = "gemini"
fallback = "claude"

[retention]
max_days = 60
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9000 {
		t.Errorf("Port = %d, want 9000", cfg.Server.Port)
	}
	if cfg.AI.Primary != "gemini" {
		t.Errorf("Primary = %q, want %q", cfg.AI.Primary, "gemini")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.toml")
	if err == nil {
		t.Error("Load(missing) = nil, want error")
	}
}

func TestLoad_InvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("this is not { valid } toml [[["), 0644)

	_, err := Load(path)
	if err == nil {
		t.Error("Load(invalid TOML) = nil, want error")
	}
}

func TestLoad_EnvOverridesToml(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[ai]
primary = "claude"
`
	os.WriteFile(path, []byte(content), 0644)

	t.Setenv("HEIMDALLM_AI_PRIMARY", "gemini")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AI.Primary != "gemini" {
		t.Errorf("Primary = %q, want %q (env override)", cfg.AI.Primary, "gemini")
	}
}

func TestLoad_IgnoresUnknownMixedArraysOutsideTypedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
unknown_mixed = [1, "top-level"]

[ai]
primary = "claude"
unknown_mixed = [2, "ai"]

[ai.repos."Org/Repo"]
primary = "codex"
unknown_mixed = [3, "repo"]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load with unknown mixed arrays: %v", err)
	}
	if cfg.AI.Primary != "claude" {
		t.Fatalf("known sibling ai.primary = %q, want claude", cfg.AI.Primary)
	}
	if got := cfg.AI.Repos["Org/Repo"].Primary; got != "codex" {
		t.Fatalf("known nested repo primary = %q, want codex", got)
	}
}

func TestLoad_KnownMixedArrayReportsFieldPath(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		path     string
		expected string
	}{
		{
			name: "slice field",
			content: `
[github]
repositories = ["org/repo", 1]

[ai]
primary = "claude"
`,
			path:     "config.github.repositories",
			expected: "[]string",
		},
		{
			name: "scalar field",
			content: `
[ai]
primary = "claude"

[ai.agents.claude]
model = ["safe-model", 1]
`,
			path:     `config.ai.agents["claude"].model`,
			expected: "string",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}

			_, err := Load(path)
			if err == nil {
				t.Fatal("Load accepted a mixed array for a known field")
			}
			for _, marker := range []string{
				tc.path,
				"TOML array has mixed element types",
				"expected " + tc.expected,
			} {
				if !strings.Contains(err.Error(), marker) {
					t.Fatalf("Load error %q missing %q", err, marker)
				}
			}
		})
	}
}

func TestLoad_WarnsWhenCanonicalKeyDiscardsAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[ai]
primary = "claude"
PRIMARY = "codex"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load canonical plus alias: %v", err)
	}
	if cfg.AI.Primary != "claude" {
		t.Fatalf("canonical primary did not win: %q", cfg.AI.Primary)
	}
	for _, marker := range []string{
		"ignored case-variant aliases",
		"path=config.ai",
		"field=primary",
		"PRIMARY",
	} {
		if !strings.Contains(logs.String(), marker) {
			t.Fatalf("alias warning %q missing %q", logs.String(), marker)
		}
	}
}

func TestLoad_CanonicalizesSchemaFieldsWithoutFoldingDynamicMapKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[GITHUB]
POLL_INTERVAL = "10m"

[AI]
primary = "claude"
PRIMARY = "codex"

[AI.AGENTS.Claude]
MODEL = "preserve-inert"

[AI.AGENTS.claude]
MODEL = "active-model"

[AI.REPOS."Org/Repo"]
PRIMARY = "codex"

[AI.REPOS."org/repo"]
PRIMARY = "gemini"

[AI.REPOS."Org/Repo".CIRCUIT_BREAKER]
PER_PR_24H = 7
unknown_mixed = [4, "pointer"]

[MERGE_TRACKING.REPOS."Org/Repo"]
ENABLED = true

[MERGE_TRACKING.REPOS."org/repo"]
ENABLED = false
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load aliases: %v", err)
	}
	if cfg.AI.Primary != "claude" {
		t.Fatalf("exact canonical primary did not win its alias: %q", cfg.AI.Primary)
	}
	if cfg.GitHub.PollInterval != "10m" {
		t.Fatalf("generic GitHub alias was not canonicalized: %q", cfg.GitHub.PollInterval)
	}
	if got := cfg.AI.Agents["Claude"].Model; got != "preserve-inert" {
		t.Fatalf("case-sensitive inert agent key was changed: %q", got)
	}
	if got := cfg.AI.Agents["claude"].Model; got != "active-model" {
		t.Fatalf("supported agent key or aliased field was lost: %q", got)
	}
	if len(cfg.AI.Agents) != 2 {
		t.Fatalf("case-distinct agent keys collapsed: %#v", cfg.AI.Agents)
	}
	upperRepo, upperOK := cfg.AI.Repos["Org/Repo"]
	lowerRepo, lowerOK := cfg.AI.Repos["org/repo"]
	if !upperOK || !lowerOK || len(cfg.AI.Repos) != 2 {
		t.Fatalf("case-distinct repo keys collapsed: %#v", cfg.AI.Repos)
	}
	if upperRepo.Primary != "codex" || upperRepo.CircuitBreaker == nil ||
		upperRepo.CircuitBreaker.PerPR24h != 7 {
		t.Fatalf("upper-case repo fields/pointer were not decoded: %+v", upperRepo)
	}
	if lowerRepo.Primary != "gemini" {
		t.Fatalf("lower-case repo primary = %q, want gemini", lowerRepo.Primary)
	}
	upperMT, upperMTOK := cfg.MergeTracking.Repos["Org/Repo"]
	lowerMT, lowerMTOK := cfg.MergeTracking.Repos["org/repo"]
	if !upperMTOK || !lowerMTOK || len(cfg.MergeTracking.Repos) != 2 {
		t.Fatalf("case-distinct merge-tracking repo keys collapsed: %#v", cfg.MergeTracking.Repos)
	}
	if upperMT.Enabled == nil || !*upperMT.Enabled ||
		lowerMT.Enabled == nil || *lowerMT.Enabled {
		t.Fatalf("merge-tracking pointer aliases were not decoded: %#v", cfg.MergeTracking.Repos)
	}
}

func TestLoad_DangerousAliasesRemainFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[ai]
primary = "claude"

[ai.agents.claude]
dangerously_skip_perms = true
DANGEROUSLY_SKIP_PERMS = false
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load dangerous aliases: %v", err)
	}
	if cfg.AI.Agents["claude"].DangerouslySkipPerms {
		t.Fatal("false dangerous alias did not override canonical true")
	}
}

func TestLoad_SanitizesLegacyAgentFieldOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[ai]
primary = "claude"

[ai.agents.claude]
model = "--sandbox"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load unsafe legacy field: %v", err)
	}
	if cfg.AI.Agents["claude"].Model != "" {
		t.Fatalf("unsafe model survived: %+v", cfg.AI.Agents["claude"])
	}
	if got := strings.Count(logs.String(), "field=model"); got != 1 {
		t.Fatalf("model sanitation warnings = %d, want 1:\n%s", got, logs.String())
	}
}

func TestLoad_SanitizesLegacyAgentPolicyWithoutBlockingStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[github]
poll_interval = "10m"

[ai]
primary = "codex"

[ai.agents.codex]
approval_mode = "ON-REQUEST"
extra_flags = "--model gpt-5 --json"

[ai.agents.gemini]
approval_mode = "YOLO"
extra_flags = "--sandbox"

[ai.agents.claude]
effort = "HIGH"
permission_mode = "ACCEPTEDITS"

[ai.agents.future_cli]
model = "preserve-inert-profile"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load legacy config: %v", err)
	}
	if cfg.GitHub.PollInterval != "10m" {
		t.Fatalf("unrelated config lost: poll_interval = %q", cfg.GitHub.PollInterval)
	}
	codex := cfg.AI.Agents["codex"]
	if codex.Model != "gpt-5" || codex.ApprovalMode != "on-request" || codex.ExtraFlags != "--json" {
		t.Fatalf("Codex legacy policy was not migrated safely: %+v", codex)
	}
	gemini := cfg.AI.Agents["gemini"]
	if gemini.ApprovalMode != "" || gemini.ExtraFlags != "" {
		t.Fatalf("unsafe Gemini legacy policy survived: %+v", gemini)
	}
	claude := cfg.AI.Agents["claude"]
	if claude.Effort != "high" || claude.PermissionMode != "acceptEdits" {
		t.Fatalf("safe casing was not canonicalized: %+v", claude)
	}
	if got := cfg.AI.Agents["future_cli"].Model; got != "preserve-inert-profile" {
		t.Fatalf("unknown inert profile was not preserved: %q", got)
	}
}

func TestLoad_CanonicalizesAgentAliasesBeforeTypedDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[AI]
primary = "claude"

[AI.Agents.codex]
model = "canonical-model"
Model = "first-alias"
MODEL = "second-alias"
Prompt = "trusted-profile"
BARE = true
DANGEROUSLY_SKIP_PERMS = true
NO_SESSION_PERSISTENCE = true
EXECUTION_TIMEOUT = "20m"

[AI.Agents.Claude]
model = "--sandbox"
permission_mode = "bypassPermissions"
DANGEROUSLY_SKIP_PERMS = true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEIMDALLM_AI_PRIMARY", "gemini")

	for i := 0; i < 64; i++ {
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load iteration %d: %v", i, err)
		}
		if cfg.AI.Primary != "gemini" {
			t.Fatalf("iteration %d: env precedence changed, primary = %q", i, cfg.AI.Primary)
		}
		if cfg.GitHub.PollInterval != "5m" {
			t.Fatalf("iteration %d: default precedence changed, poll_interval = %q", i, cfg.GitHub.PollInterval)
		}
		got := cfg.AI.Agents["codex"]
		if got.Model != "canonical-model" ||
			got.PromptID != "trusted-profile" ||
			!got.Bare ||
			!got.DangerouslySkipPerms ||
			!got.NoSessionPersistence ||
			got.ExecutionTimeout != "20m" {
			t.Fatalf("iteration %d: aliases decoded inconsistently: %+v", i, got)
		}
		inert, ok := cfg.AI.Agents["Claude"]
		if !ok ||
			inert.Model != "--sandbox" ||
			inert.PermissionMode != "bypassPermissions" ||
			!inert.DangerouslySkipPerms {
			t.Fatalf("iteration %d: case-variant CLI profile was not preserved inert: %+v", i, inert)
		}
		if _, active := cfg.AI.Agents["claude"]; active {
			t.Fatalf("iteration %d: inert Claude profile was activated as canonical claude", i)
		}
	}
	gotContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotContent) != content {
		t.Fatalf("Load rewrote inert profile:\n%s", gotContent)
	}
}

func TestLoad_RejectsAmbiguousAliasesDeterministically(t *testing.T) {
	tests := []struct {
		name    string
		content string
		marker  string
	}{
		{
			name: "agent leaf aliases",
			content: `
[ai]
primary = "codex"
[ai.agents.codex]
Model = "first"
MODEL = "second"
`,
			marker: `ambiguous aliases for "model" (MODEL, Model)`,
		},
		{
			name: "generic leaf aliases",
			content: `
[ai]
Primary = "claude"
PRIMARY = "codex"
`,
			marker: `ambiguous aliases for "primary" (PRIMARY, Primary)`,
		},
		{
			name: "nested dynamic map leaf aliases",
			content: `
[ai]
primary = "claude"
[ai.repos."Org/Repo"]
Primary = "codex"
PRIMARY = "gemini"
`,
			marker: `config.ai.repos["Org/Repo"]: ambiguous aliases for "primary" (PRIMARY, Primary)`,
		},
		{
			name: "structural aliases",
			content: `
[ai]
primary = "codex"
[AI]
fallback = "gemini"
`,
			marker: `ambiguous structural aliases at "config" for "ai" (AI, ai)`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			var first string
			for i := 0; i < 64; i++ {
				_, err := Load(path)
				if err == nil {
					t.Fatalf("Load iteration %d unexpectedly accepted ambiguous aliases", i)
				}
				if !strings.Contains(err.Error(), tc.marker) {
					t.Fatalf("iteration %d: error %q missing %q", i, err, tc.marker)
				}
				if i == 0 {
					first = err.Error()
				} else if err.Error() != first {
					t.Fatalf("nondeterministic errors:\nfirst: %s\niteration %d: %s", first, i, err)
				}
			}
		})
	}
}

func TestSanitizeAgentExecutionFields_RevalidatesMigratedOutputs(t *testing.T) {
	got := sanitizeAgentExecutionFields("claude", CLIAgentConfig{
		Model:        " --sandbox ",
		MaxTurns:     -1,
		Effort:       "impossible",
		ApprovalMode: "default",
	}, "test migration", "migrated")

	if got.Model != "" || got.MaxTurns != 0 || got.Effort != "" {
		t.Fatalf("unsafe migrated typed outputs survived: %+v", got)
	}
	if got.ApprovalMode != "default" {
		t.Fatalf("unrelated typed field changed: %+v", got)
	}

	safe := sanitizeAgentExecutionFields("claude", CLIAgentConfig{
		Model:    " safe-model ",
		MaxTurns: 7,
		Effort:   "HIGH",
	}, "test migration", "migrated")
	if safe.Model != "safe-model" || safe.MaxTurns != 7 || safe.Effort != "high" {
		t.Fatalf("safe migrated typed outputs were not canonicalized: %+v", safe)
	}
}

func TestSanitizeLegacyAgentExecutionPolicyMap_CanonicalExactWinsAliases(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	agent := map[string]any{
		"model":                  "canonical-model",
		"Model":                  "first-alias",
		"MODEL":                  "second-alias",
		"prompt":                 "canonical-prompt",
		"Prompt":                 "alias-prompt",
		"bare":                   true,
		"Bare":                   false,
		"no_session_persistence": false,
		"No_Session_Persistence": true,
		"execution_timeout":      "20m",
		"Execution_Timeout":      "1m",
	}
	m := map[string]any{
		"ai": map[string]any{
			"agents": map[string]any{"codex": agent},
		},
	}

	if err := SanitizeLegacyAgentExecutionPolicyMap(m, "test TOML"); err != nil {
		t.Fatalf("sanitize canonical plus aliases: %v", err)
	}
	if got := agent["model"]; got != "canonical-model" {
		t.Fatalf("canonical exact key did not win: %v", agent)
	}
	if _, present := agent["Model"]; present {
		t.Fatalf("mixed-case alias was not removed: %v", agent)
	}
	if _, present := agent["MODEL"]; present {
		t.Fatalf("uppercase alias was not removed: %v", agent)
	}
	if agent["prompt"] != "canonical-prompt" ||
		agent["bare"] != true ||
		agent["no_session_persistence"] != false ||
		agent["execution_timeout"] != "20m" {
		t.Fatalf("pass-through canonical leaves did not win: %v", agent)
	}
	for _, alias := range []string{"Prompt", "Bare", "No_Session_Persistence", "Execution_Timeout"} {
		if _, present := agent[alias]; present {
			t.Fatalf("pass-through alias %q was not removed: %v", alias, agent)
		}
	}
	for _, marker := range []string{
		"ignored case-variant aliases",
		"config.ai.agents",
		"field=model",
		"MODEL",
		"Model",
	} {
		if !strings.Contains(logs.String(), marker) {
			t.Fatalf("legacy alias warning %q missing %q", logs.String(), marker)
		}
	}
}

func TestSanitizeLegacyAgentExecutionPolicyMap_DangerousAliasesFailClosed(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]any
		want  bool
	}{
		{
			name:  "single canonical true",
			flags: map[string]any{"dangerously_skip_perms": true},
			want:  true,
		},
		{
			name:  "single alias true",
			flags: map[string]any{"DANGEROUSLY_SKIP_PERMS": true},
			want:  true,
		},
		{
			name: "canonical true plus alias false",
			flags: map[string]any{
				"dangerously_skip_perms": true,
				"DANGEROUSLY_SKIP_PERMS": false,
			},
			want: false,
		},
		{
			name: "aliases all true",
			flags: map[string]any{
				"Dangerously_Skip_Perms": true,
				"DANGEROUSLY_SKIP_PERMS": true,
			},
			want: true,
		},
		{
			name: "aliases mixed",
			flags: map[string]any{
				"Dangerously_Skip_Perms": true,
				"DANGEROUSLY_SKIP_PERMS": false,
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := make(map[string]any, len(tc.flags))
			for key, value := range tc.flags {
				agent[key] = value
			}
			m := map[string]any{
				"ai": map[string]any{
					"agents": map[string]any{"claude": agent},
				},
			}
			if err := SanitizeLegacyAgentExecutionPolicyMap(m, "test TOML"); err != nil {
				t.Fatalf("sanitize dangerous aliases: %v", err)
			}
			if got, ok := agent["dangerously_skip_perms"].(bool); !ok || got != tc.want {
				t.Fatalf("canonical dangerous value = %v, want %v (map: %v)", got, tc.want, agent)
			}
			for key := range agent {
				if key != "dangerously_skip_perms" && strings.EqualFold(key, "dangerously_skip_perms") {
					t.Fatalf("dangerous alias %q survived: %v", key, agent)
				}
			}
		})
	}
}

func TestSanitizeLegacyAgentExecutionPolicyMap_RejectsAmbiguousAliases(t *testing.T) {
	agent := map[string]any{
		"Model": "first-alias",
		"MODEL": "second-alias",
	}
	m := map[string]any{
		"ai": map[string]any{
			"agents": map[string]any{"codex": agent},
		},
	}

	err := SanitizeLegacyAgentExecutionPolicyMap(m, "test TOML")
	if err == nil {
		t.Fatal("ambiguous aliases unexpectedly accepted")
	}
	got := err.Error()
	for _, want := range []string{`ambiguous aliases for "model"`, "MODEL, Model", `canonical "model"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not contain actionable detail %q", got, want)
		}
	}
}

// ── LoadOrCreate ─────────────────────────────────────────────────────────────

func TestLoadOrCreate_Creates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	t.Setenv("HEIMDALLM_AI_PRIMARY", "claude")
	t.Setenv("HEIMDALLM_REPOSITORIES", "org/repo")

	cfg, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if cfg.AI.Primary != "claude" {
		t.Errorf("Primary = %q, want %q", cfg.AI.Primary, "claude")
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("config file was not created")
	}
}

func TestLoadOrCreate_LoadsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[ai]
primary = "gemini"
`
	os.WriteFile(path, []byte(content), 0644)

	cfg, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if cfg.AI.Primary != "gemini" {
		t.Errorf("Primary = %q, want %q", cfg.AI.Primary, "gemini")
	}
}

func TestLoadOrCreate_FailsWithoutPrimary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	_, err := LoadOrCreate(path)
	if err == nil {
		t.Error("LoadOrCreate without ai.primary should fail")
	}
}

// ── ShortRepoName ────────────────────────────────────────────────────────────

func TestShortRepoName(t *testing.T) {
	cases := map[string]string{
		"org/name":        "name",
		"org/name-dash":   "name-dash",
		"simple":          "simple",
		"":                "",
		"a/b/c":           "c",
		"trailing-slash/": "",
	}
	for in, want := range cases {
		if got := ShortRepoName(in); got != want {
			t.Errorf("ShortRepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── ResolveLocalDir ──────────────────────────────────────────────────────────

func TestResolveLocalDir_PrefersConfigured(t *testing.T) {
	// A configured value is always returned verbatim, even when the
	// mount-root fallback would also match — the operator's explicit
	// choice wins.
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "name"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	old := DefaultReposMountPath
	DefaultReposMountPath = tmp
	t.Cleanup(func() { DefaultReposMountPath = old })

	if got := ResolveLocalDir("/explicit/path", "org/name", nil); got != "/explicit/path" {
		t.Errorf("got %q, want /explicit/path", got)
	}
}

func TestResolveLocalDir_AutoDetectFromMount(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "name")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	old := DefaultReposMountPath
	DefaultReposMountPath = tmp
	t.Cleanup(func() { DefaultReposMountPath = old })

	if got := ResolveLocalDir("", "org/name", nil); got != repoDir {
		t.Errorf("got %q, want %q", got, repoDir)
	}
}

func TestResolveLocalDir_NoFallbackWhenDirMissing(t *testing.T) {
	tmp := t.TempDir()
	// Intentionally do NOT create tmp/name — mount exists but this repo
	// hasn't been cloned under it, so we fall through to empty.
	old := DefaultReposMountPath
	DefaultReposMountPath = tmp
	t.Cleanup(func() { DefaultReposMountPath = old })

	if got := ResolveLocalDir("", "org/name", nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolveLocalDir_IgnoresFiles(t *testing.T) {
	// A regular file at /repos/name must not be treated as a repo dir.
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "name"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	old := DefaultReposMountPath
	DefaultReposMountPath = tmp
	t.Cleanup(func() { DefaultReposMountPath = old })

	if got := ResolveLocalDir("", "org/name", nil); got != "" {
		t.Errorf("got %q, want empty (file, not dir)", got)
	}
}

func TestResolveLocalDir_EmptyReposMountPath(t *testing.T) {
	old := DefaultReposMountPath
	DefaultReposMountPath = ""
	t.Cleanup(func() { DefaultReposMountPath = old })

	if got := ResolveLocalDir("", "org/name", nil); got != "" {
		t.Errorf("got %q, want empty (mount path disabled)", got)
	}
}

func TestResolveLocalDir_EmptyRepo(t *testing.T) {
	// Defensive: an empty repo string should not accidentally resolve
	// to DefaultReposMountPath itself (would point the agent at the
	// mount root, exposing every repo to a single review).
	tmp := t.TempDir()
	old := DefaultReposMountPath
	DefaultReposMountPath = tmp
	t.Cleanup(func() { DefaultReposMountPath = old })

	if got := ResolveLocalDir("", "", nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// ── ResolveLocalDir with LocalDirBase ────────────────────────────────────────

func TestResolveLocalDir_LocalDirBase(t *testing.T) {
	// Create temp dirs simulating workspace
	base := t.TempDir()
	repoDir := filepath.Join(base, "my-repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	got := ResolveLocalDir("", "org/my-repo", []string{base})
	if got != repoDir {
		t.Errorf("ResolveLocalDir = %q, want %q", got, repoDir)
	}
}

func TestResolveLocalDir_OverrideTakesPrecedence(t *testing.T) {
	got := ResolveLocalDir("/custom/path", "org/repo", []string{"/some/base"})
	if got != "/custom/path" {
		t.Errorf("ResolveLocalDir = %q, want /custom/path", got)
	}
}

func TestResolveLocalDir_BaseBeforeDefault(t *testing.T) {
	base := t.TempDir()
	defaultPath := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	defaultRepoDir := filepath.Join(defaultPath, "repo")
	if err := os.MkdirAll(defaultRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	old := DefaultReposMountPath
	DefaultReposMountPath = defaultPath
	defer func() { DefaultReposMountPath = old }()

	got := ResolveLocalDir("", "org/repo", []string{base})
	if got != repoDir {
		t.Errorf("ResolveLocalDir = %q, want base path %q (not default)", got, repoDir)
	}
}

func TestResolveLocalDir_FallbackToDefault(t *testing.T) {
	defaultPath := t.TempDir()
	repoDir := filepath.Join(defaultPath, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	old := DefaultReposMountPath
	DefaultReposMountPath = defaultPath
	defer func() { DefaultReposMountPath = old }()

	got := ResolveLocalDir("", "org/repo", nil) // empty base
	if got != repoDir {
		t.Errorf("ResolveLocalDir = %q, want default %q", got, repoDir)
	}
}

func TestApplyEnvOverrides_LocalDirBase(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	t.Setenv("HEIMDALLM_LOCAL_DIR_BASE", "/workspace/group1, /workspace/group2")
	cfg.applyEnvOverrides()
	if len(cfg.GitHub.LocalDirBase) != 2 {
		t.Fatalf("LocalDirBase = %v, want 2 items", cfg.GitHub.LocalDirBase)
	}
	if cfg.GitHub.LocalDirBase[0] != "/workspace/group1" {
		t.Errorf("LocalDirBase[0] = %q, want /workspace/group1", cfg.GitHub.LocalDirBase[0])
	}
	if cfg.GitHub.LocalDirBase[1] != "/workspace/group2" {
		t.Errorf("LocalDirBase[1] = %q, want /workspace/group2", cfg.GitHub.LocalDirBase[1])
	}
}

func TestResolveLocalDir_MultipleBases(t *testing.T) {
	group1 := t.TempDir()
	group2 := t.TempDir()
	// repo-a only in group1
	if err := os.MkdirAll(filepath.Join(group1, "repo-a"), 0755); err != nil {
		t.Fatal(err)
	}
	// repo-b only in group2
	if err := os.MkdirAll(filepath.Join(group2, "repo-b"), 0755); err != nil {
		t.Fatal(err)
	}

	bases := []string{group1, group2}

	gotA := ResolveLocalDir("", "org/repo-a", bases)
	if gotA != filepath.Join(group1, "repo-a") {
		t.Errorf("repo-a = %q, want %q", gotA, filepath.Join(group1, "repo-a"))
	}
	gotB := ResolveLocalDir("", "org/repo-b", bases)
	if gotB != filepath.Join(group2, "repo-b") {
		t.Errorf("repo-b = %q, want %q", gotB, filepath.Join(group2, "repo-b"))
	}
	gotC := ResolveLocalDir("", "org/repo-c", bases)
	if gotC != "" {
		t.Errorf("repo-c = %q, want empty (not in any base)", gotC)
	}
}

// ── ActivityLogConfig ────────────────────────────────────────────────────────

func TestActivityLogConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[ai]
primary = "claude"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.ActivityLog.Enabled == nil {
		t.Fatal("Enabled pointer should be set after applyDefaults")
	}
	if !*c.ActivityLog.Enabled {
		t.Error("Enabled should default to true")
	}
	if c.ActivityLog.RetentionDays == nil {
		t.Fatal("RetentionDays pointer should be set after applyDefaults")
	}
	if *c.ActivityLog.RetentionDays != 90 {
		t.Errorf("RetentionDays = %d, want 90", *c.ActivityLog.RetentionDays)
	}
}

func TestActivityLogConfig_ExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[ai]
primary = "claude"
[activity_log]
enabled = false
retention_days = 30
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.ActivityLog.Enabled == nil || *c.ActivityLog.Enabled {
		t.Error("Enabled should be false (explicitly set)")
	}
	if c.ActivityLog.RetentionDays == nil || *c.ActivityLog.RetentionDays != 30 {
		days := 0
		if c.ActivityLog.RetentionDays != nil {
			days = *c.ActivityLog.RetentionDays
		}
		t.Errorf("RetentionDays = %d, want 30", days)
	}
}

func TestActivityLogConfig_StoreLayer(t *testing.T) {
	c := &Config{}
	enabledTrue := true
	c.ActivityLog.Enabled = &enabledTrue
	v := 90
	c.ActivityLog.RetentionDays = &v
	c.AI.Primary = "claude" // prevent unrelated validation failure

	if err := c.ApplyStore(map[string]string{
		"activity_log_enabled":        "false",
		"activity_log_retention_days": "45",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.ActivityLog.Enabled == nil || *c.ActivityLog.Enabled {
		t.Error("Enabled should be false after store override")
	}
	if c.ActivityLog.RetentionDays == nil || *c.ActivityLog.RetentionDays != 45 {
		days := 0
		if c.ActivityLog.RetentionDays != nil {
			days = *c.ActivityLog.RetentionDays
		}
		t.Errorf("retention_days = %d, want 45", days)
	}
}

func TestActivityLogConfig_RetentionValidation(t *testing.T) {
	tests := []struct {
		days    int
		wantErr bool
	}{
		{0, false}, // 0 is no-op, valid
		{1, false},
		{90, false},
		{3650, false},
		{-1, true},
		{3651, true},
	}
	for _, tt := range tests {
		c := &Config{}
		c.AI.Primary = "claude" // avoid unrelated validation failures
		days := tt.days
		c.ActivityLog.RetentionDays = &days
		// Enabled=nil is fine; Validate should not require a pointer deref.
		err := c.Validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("days=%d: err=%v wantErr=%v", tt.days, err, tt.wantErr)
		}
	}
}

func TestActivityLogConfig_ExplicitZeroRetentionIsKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[ai]
primary = "claude"
[activity_log]
retention_days = 0
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.ActivityLog.RetentionDays == nil {
		t.Fatal("RetentionDays should be non-nil after applyDefaults")
	}
	if *c.ActivityLog.RetentionDays != 0 {
		t.Errorf("RetentionDays = %d, want 0 (explicit)", *c.ActivityLog.RetentionDays)
	}
}

// ── AutoEnablePRForDiscovery ────────────────────────────────────────────────

func TestApplyEnvOverrides_ExecutionTimeout(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	t.Setenv("HEIMDALLM_EXECUTION_TIMEOUT", "20m")
	cfg.applyEnvOverrides()
	if cfg.AI.ExecutionTimeout != "20m" {
		t.Errorf("ExecutionTimeout = %q, want 20m", cfg.AI.ExecutionTimeout)
	}
}

func TestAutoEnablePRForDiscovery_Default(t *testing.T) {
	cfg := &GitHubConfig{}
	if !cfg.AutoEnablePRForDiscovery() {
		t.Fatal("default should be true")
	}
}

func TestAutoEnablePRForDiscovery_Explicit(t *testing.T) {
	f := false
	cfg := &GitHubConfig{AutoEnablePROnDiscovery: &f}
	if cfg.AutoEnablePRForDiscovery() {
		t.Fatal("explicit false should return false")
	}
}

func TestReviewGuards_Defaults(t *testing.T) {
	c := &Config{} // zero config — all pointers nil
	g := c.ReviewGuards("heimdallm-bot")
	if !g.SkipDrafts {
		t.Errorf("SkipDrafts default = false, want true")
	}
	if !g.SkipSelfAuthor {
		t.Errorf("SkipSelfAuthor default = false, want true")
	}
	if g.BotLogin != "heimdallm-bot" {
		t.Errorf("BotLogin = %q, want heimdallm-bot", g.BotLogin)
	}
}

func TestReviewGuards_ExplicitFalse(t *testing.T) {
	f := false
	c := &Config{
		GitHub: GitHubConfig{
			ReviewGuards: ReviewGuardsConfig{
				SkipDrafts:     &f,
				SkipSelfAuthor: &f,
			},
		},
	}
	g := c.ReviewGuards("bot")
	if g.SkipDrafts {
		t.Errorf("SkipDrafts: explicit false not honoured")
	}
	if g.SkipSelfAuthor {
		t.Errorf("SkipSelfAuthor: explicit false not honoured")
	}
}

func TestMatchesInstructionAuthors(t *testing.T) {
	r := RepoAI{InstructionAuthors: []string{"Alice", "@bob"}}
	if !r.MatchesInstructionAuthors("alice") {
		t.Error("alice should match (case-insensitive)")
	}
	if !r.MatchesInstructionAuthors("@BOB") {
		t.Error("@BOB should match (leading @ + case-insensitive)")
	}
	if r.MatchesInstructionAuthors("mallory") {
		t.Error("mallory must not match")
	}
	if r.MatchesInstructionAuthors("") {
		t.Error("empty login must not match")
	}
	if (RepoAI{}).MatchesInstructionAuthors("alice") {
		t.Error("empty allowlist must deny everyone")
	}
}

func TestAIForRepo_InstructionAuthorsResolution(t *testing.T) {
	c := &Config{}
	c.AI.InstructionAuthors = []string{"global-user"}
	c.AI.Orgs = map[string]OrgAI{
		"org": {InstructionAuthors: []string{"org-user"}},
	}
	c.AI.Repos = map[string]RepoAI{
		"org/repo":  {InstructionAuthors: []string{"repo-user"}},
		"org/inhrt": {}, // nil → inherits org
	}
	if got := c.AIForRepo("org/repo").InstructionAuthors; len(got) != 1 || got[0] != "repo-user" {
		t.Errorf("repo override: got %v", got)
	}
	if got := c.AIForRepo("org/inhrt").InstructionAuthors; len(got) != 1 || got[0] != "org-user" {
		t.Errorf("org inherit: got %v", got)
	}
	if got := c.AIForRepo("other/x").InstructionAuthors; len(got) != 1 || got[0] != "global-user" {
		t.Errorf("global fallback: got %v", got)
	}
}

func TestNeverApproveWithIssues_Resolution(t *testing.T) {
	tru := true
	fal := false
	cases := []struct {
		name   string
		global bool
		org    *bool
		repo   *bool
		want   bool
	}{
		{"global off, no overrides", false, nil, nil, false},
		{"global on, no overrides", true, nil, nil, true},
		{"org on over global off", false, &tru, nil, true},
		{"repo off over org on", false, &tru, &fal, false},
		{"repo on over global off", false, nil, &tru, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{}
			c.AI.NeverApproveWithIssues = tc.global
			c.AI.Orgs = map[string]OrgAI{"acme": {NeverApproveWithIssues: tc.org}}
			c.AI.Repos = map[string]RepoAI{"acme/widget": {NeverApproveWithIssues: tc.repo}}
			got := c.AIForRepo("acme/widget").NeverApproveWithIssues
			if got == nil {
				t.Fatalf("NeverApproveWithIssues is nil, want non-nil")
			}
			if *got != tc.want {
				t.Errorf("NeverApproveWithIssues = %v, want %v", *got, tc.want)
			}
		})
	}
}

func TestNeverApproveMinSeverity_Resolution(t *testing.T) {
	cases := []struct {
		name   string
		global string
		org    string
		repo   string
		want   string
	}{
		{"all empty inherits empty (low)", "", "", "", ""},
		{"global only", "medium", "", "", "medium"},
		{"org over global", "medium", "high", "", "high"},
		{"repo over org", "medium", "high", "low", "low"},
		{"repo over global", "medium", "", "high", "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{}
			c.AI.NeverApproveMinSeverity = tc.global
			c.AI.Orgs = map[string]OrgAI{"acme": {NeverApproveMinSeverity: tc.org}}
			c.AI.Repos = map[string]RepoAI{"acme/widget": {NeverApproveMinSeverity: tc.repo}}
			got := c.AIForRepo("acme/widget").NeverApproveMinSeverity
			if got != tc.want {
				t.Errorf("NeverApproveMinSeverity = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNeverApproveMinSeverity_Validate(t *testing.T) {
	base := func() *Config {
		c := &Config{}
		c.AI.Primary = "claude"
		c.GitHub.PollInterval = "1m"
		return c
	}
	for _, v := range []string{"", "low", "medium", "high"} {
		c := base()
		c.AI.NeverApproveMinSeverity = v
		if err := c.Validate(); err != nil {
			t.Errorf("valid value %q rejected: %v", v, err)
		}
	}
	c := base()
	c.AI.NeverApproveMinSeverity = "critical"
	if err := c.Validate(); err == nil {
		t.Errorf("invalid global value accepted")
	}
	c = base()
	c.AI.Orgs = map[string]OrgAI{"acme": {NeverApproveMinSeverity: "urgent"}}
	if err := c.Validate(); err == nil {
		t.Errorf("invalid org value accepted")
	}
	c = base()
	c.AI.Repos = map[string]RepoAI{"acme/widget": {NeverApproveMinSeverity: "nit"}}
	if err := c.Validate(); err == nil {
		t.Errorf("invalid repo value accepted")
	}
}

func TestValidateAgentExecutionPolicy(t *testing.T) {
	base := func() *Config {
		c := &Config{}
		c.applyDefaults()
		c.AI.Primary = "claude"
		c.GitHub.PollInterval = "1m"
		return c
	}

	tests := []struct {
		name    string
		agents  map[string]CLIAgentConfig
		wantErr bool
	}{
		{
			name: "safe provider flags and typed modes",
			agents: map[string]CLIAgentConfig{
				"claude": {Model: "opus", MaxTurns: 5, ExtraFlags: "--verbose", PermissionMode: "ACCEPTEDITS", Effort: "HIGH"},
				"codex":  {ExtraFlags: "--json --color never", ApprovalMode: "on-request"},
				"gemini": {ExtraFlags: "--output-format json", ApprovalMode: "auto_edit"},
			},
		},
		{
			name: "legacy Codex sandbox override",
			agents: map[string]CLIAgentConfig{
				"codex": {ExtraFlags: "--sandbox danger-full-access"},
			},
			wantErr: true,
		},
		{
			name: "legacy Gemini approval override",
			agents: map[string]CLIAgentConfig{
				"gemini": {ExtraFlags: "--approval-mode=yolo"},
			},
			wantErr: true,
		},
		{
			name: "unsafe typed Gemini approval",
			agents: map[string]CLIAgentConfig{
				"gemini": {ApprovalMode: "yolo"},
			},
			wantErr: true,
		},
		{
			name: "option-shaped typed model",
			agents: map[string]CLIAgentConfig{
				"claude": {Model: "--dangerously-skip-permissions"},
			},
			wantErr: true,
		},
		{
			name: "invalid typed effort",
			agents: map[string]CLIAgentConfig{
				"claude": {Effort: "--dangerously-skip-permissions"},
			},
			wantErr: true,
		},
		{
			name: "unknown CLI config",
			agents: map[string]CLIAgentConfig{
				"other": {},
			},
		},
		{
			name: "negative max turns",
			agents: map[string]CLIAgentConfig{
				"claude": {MaxTurns: -1},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			c.AI.Agents = tc.agents
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected agent execution policy error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected agent execution policy error: %v", err)
			}
		})
	}
}
