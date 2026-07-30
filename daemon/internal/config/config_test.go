package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
}

func TestApplyDefaults_PreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.Server.Port = 9999
	cfg.Server.BindAddr = "0.0.0.0"
	cfg.GitHub.PollInterval = "1m"
	cfg.Retention.MaxDays = 30
	cfg.AI.ReviewMode = "multi"

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
}

func TestApplyDefaults_Tier2RepoConcurrency(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.AI.Tier2RepoConcurrency != 5 {
		t.Errorf("Tier2RepoConcurrency = %d, want 5", cfg.AI.Tier2RepoConcurrency)
	}
}

func TestApplyDefaults_Tier2RepoConcurrency_PreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.AI.Tier2RepoConcurrency = 12
	cfg.applyDefaults()
	if cfg.AI.Tier2RepoConcurrency != 12 {
		t.Errorf("Tier2RepoConcurrency overwritten: %d", cfg.AI.Tier2RepoConcurrency)
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

// TestApplyDefaults_ReviewResponseDisabledByDefault locks in the
// safety-first stance of #482 phase 2: a fresh config with no
// review_response section keeps the feature disabled. The cap and
// cooldown axes still get sane defaults so an operator who only sets
// Enabled = true does not accidentally lift the cap.
func TestApplyDefaults_ReviewResponseDisabledByDefault(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.AI.ReviewResponse.Enabled {
		t.Error("ReviewResponse.Enabled defaulted to true; must be opt-in")
	}
	if cfg.AI.ReviewResponse.PerPRLifetime != DefaultReviewResponsePerPRLifetime {
		t.Errorf("PerPRLifetime = %d, want %d", cfg.AI.ReviewResponse.PerPRLifetime, DefaultReviewResponsePerPRLifetime)
	}
	if cfg.AI.ReviewResponse.CooldownSecs != DefaultReviewResponseCooldownSecs {
		t.Errorf("CooldownSecs = %d, want %d", cfg.AI.ReviewResponse.CooldownSecs, DefaultReviewResponseCooldownSecs)
	}
}

func TestApplyDefaults_ReviewResponse_PreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.AI.ReviewResponse.Enabled = true
	cfg.AI.ReviewResponse.PerPRLifetime = 2
	cfg.AI.ReviewResponse.CooldownSecs = 60
	cfg.applyDefaults()
	if !cfg.AI.ReviewResponse.Enabled {
		t.Error("Enabled flipped off by applyDefaults")
	}
	if cfg.AI.ReviewResponse.PerPRLifetime != 2 {
		t.Errorf("PerPRLifetime overwritten: %d", cfg.AI.ReviewResponse.PerPRLifetime)
	}
	if cfg.AI.ReviewResponse.CooldownSecs != 60 {
		t.Errorf("CooldownSecs overwritten: %d", cfg.AI.ReviewResponse.CooldownSecs)
	}
}

// TestApplyDefaults_ReviewResponse_NegativeFallsBack pins that a
// negative or zero cap is treated as "use the default" — never as
// "unlimited" — so a misconfigured TOML cannot uncap the feature.
func TestApplyDefaults_ReviewResponse_NegativeFallsBack(t *testing.T) {
	cfg := &Config{}
	cfg.AI.ReviewResponse.PerPRLifetime = -1
	cfg.AI.ReviewResponse.CooldownSecs = -1
	cfg.applyDefaults()
	if cfg.AI.ReviewResponse.PerPRLifetime != DefaultReviewResponsePerPRLifetime {
		t.Errorf("negative PerPRLifetime not reset, got %d", cfg.AI.ReviewResponse.PerPRLifetime)
	}
	if cfg.AI.ReviewResponse.CooldownSecs != DefaultReviewResponseCooldownSecs {
		t.Errorf("negative CooldownSecs not reset, got %d", cfg.AI.ReviewResponse.CooldownSecs)
	}
}

func TestApplyDefaults_ReviewFixDisabledByDefault(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.AI.ReviewFix.Enabled {
		t.Error("ReviewFix.Enabled defaulted to true; must be opt-in")
	}
	if cfg.AI.ReviewFix.PerPRLifetime != DefaultReviewFixPerPRLifetime {
		t.Errorf("PerPRLifetime = %d, want %d", cfg.AI.ReviewFix.PerPRLifetime, DefaultReviewFixPerPRLifetime)
	}
	if cfg.AI.ReviewFix.CooldownSecs != DefaultReviewFixCooldownSecs {
		t.Errorf("CooldownSecs = %d, want %d", cfg.AI.ReviewFix.CooldownSecs, DefaultReviewFixCooldownSecs)
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

func TestValidate_InvalidRefinementTimeout(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "global",
			cfg: Config{
				AI: AIConfig{Primary: "claude", RefinementTimeout: "30 m"},
			},
			want: "ai.refinement_timeout",
		},
		{
			name: "org",
			cfg: Config{
				AI: AIConfig{
					Primary: "claude",
					Orgs: map[string]OrgAI{
						"org": {RefinementTimeout: "-1m"},
					},
				},
			},
			want: `ai.orgs."org".refinement_timeout`,
		},
		{
			name: "repo",
			cfg: Config{
				AI: AIConfig{
					Primary: "claude",
					Repos: map[string]RepoAI{
						"org/repo": {RefinementTimeout: "0s"},
					},
				},
			},
			want: `ai.repos."org/repo".refinement_timeout`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want path %q", err, tc.want)
			}
		})
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

func TestApplyDefaults_IssueTracking(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	if cfg.GitHub.IssueTracking.FilterMode != FilterModeExclusive {
		t.Errorf("FilterMode = %q, want default %q", cfg.GitHub.IssueTracking.FilterMode, FilterModeExclusive)
	}
	if cfg.GitHub.IssueTracking.DefaultAction != string(IssueModeIgnore) {
		t.Errorf("DefaultAction = %q, want default %q", cfg.GitHub.IssueTracking.DefaultAction, IssueModeIgnore)
	}
	if cfg.GitHub.IssueTracking.Enabled {
		t.Error("Enabled should default to false")
	}
	if cfg.AI.RefinementTimeout != "30m" {
		t.Errorf("RefinementTimeout = %q, want 30m", cfg.AI.RefinementTimeout)
	}
}

func TestApplyDefaults_IssueTrackingPreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.GitHub.IssueTracking.FilterMode = FilterModeInclusive
	cfg.GitHub.IssueTracking.DefaultAction = string(IssueModeReviewOnly)
	cfg.applyDefaults()

	if cfg.GitHub.IssueTracking.FilterMode != FilterModeInclusive {
		t.Errorf("FilterMode overwritten: %q", cfg.GitHub.IssueTracking.FilterMode)
	}
	if cfg.GitHub.IssueTracking.DefaultAction != string(IssueModeReviewOnly) {
		t.Errorf("DefaultAction overwritten: %q", cfg.GitHub.IssueTracking.DefaultAction)
	}
}

func TestIssueTrackingWithDefaultAssignee(t *testing.T) {
	cfg := IssueTrackingConfig{Enabled: true}
	got := cfg.WithDefaultAssignee("@alice")
	if len(got.Assignees) != 1 || got.Assignees[0] != "alice" {
		t.Fatalf("Assignees = %v, want [alice]", got.Assignees)
	}
	if len(cfg.Assignees) != 0 {
		t.Fatalf("WithDefaultAssignee mutated receiver: %v", cfg.Assignees)
	}
}

func TestIssueTrackingWithDefaultAssigneePreservesExplicitList(t *testing.T) {
	cfg := IssueTrackingConfig{Enabled: true, Assignees: []string{"bob"}}
	got := cfg.WithDefaultAssignee("alice")
	if len(got.Assignees) != 1 || got.Assignees[0] != "bob" {
		t.Fatalf("Assignees = %v, want explicit [bob]", got.Assignees)
	}
}

func TestIssueTrackingMatchesAssignees(t *testing.T) {
	cfg := IssueTrackingConfig{Assignees: []string{"Alice"}}
	if !cfg.MatchesAssignees([]string{"alice"}) {
		t.Fatal("expected case-insensitive assignee match")
	}
	if cfg.MatchesAssignees([]string{"bob", "alice"}) {
		t.Fatal("multi-assignee issue must not match active assignee filter")
	}
	if cfg.MatchesAssignees([]string{"bob"}) {
		t.Fatal("unexpected assignee match")
	}
	if cfg.MatchesAssignees(nil) {
		t.Fatal("unassigned issue must not match active assignee filter")
	}
}

func TestApplyEnvOverrides_IssueTracking(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	t.Setenv("HEIMDALLM_ISSUE_TRACKING_ENABLED", "true")
	t.Setenv("HEIMDALLM_ISSUE_FILTER_MODE", "inclusive")
	t.Setenv("HEIMDALLM_ISSUE_DEFAULT_ACTION", "review_only")
	t.Setenv("HEIMDALLM_ISSUE_ORGANIZATIONS", "freepik-company, theburrowhub")
	t.Setenv("HEIMDALLM_ISSUE_ASSIGNEES", "sergiotejon")
	t.Setenv("HEIMDALLM_ISSUE_DEVELOP_LABELS", "enhancement,feature, bug")
	t.Setenv("HEIMDALLM_ISSUE_REFINEMENT_LABELS", "refine,needs-plan")
	t.Setenv("HEIMDALLM_ISSUE_REVIEW_ONLY_LABELS", "question,discussion")
	t.Setenv("HEIMDALLM_ISSUE_SKIP_LABELS", "wontfix")
	t.Setenv("HEIMDALLM_REFINEMENT_TIMEOUT", "45m")

	cfg.applyEnvOverrides()

	it := cfg.GitHub.IssueTracking
	if !it.Enabled {
		t.Error("Enabled should be true")
	}
	if it.FilterMode != FilterModeInclusive {
		t.Errorf("FilterMode = %q", it.FilterMode)
	}
	if it.DefaultAction != string(IssueModeReviewOnly) {
		t.Errorf("DefaultAction = %q", it.DefaultAction)
	}
	if len(it.Organizations) != 2 || it.Organizations[1] != "theburrowhub" {
		t.Errorf("Organizations = %v", it.Organizations)
	}
	if len(it.Assignees) != 1 || it.Assignees[0] != "sergiotejon" {
		t.Errorf("Assignees = %v", it.Assignees)
	}
	if len(it.DevelopLabels) != 3 || it.DevelopLabels[2] != "bug" {
		t.Errorf("DevelopLabels = %v", it.DevelopLabels)
	}
	if len(it.RefinementLabels) != 2 || it.RefinementLabels[1] != "needs-plan" {
		t.Errorf("RefinementLabels = %v", it.RefinementLabels)
	}
	if len(it.ReviewOnlyLabels) != 2 {
		t.Errorf("ReviewOnlyLabels = %v", it.ReviewOnlyLabels)
	}
	if len(it.SkipLabels) != 1 {
		t.Errorf("SkipLabels = %v", it.SkipLabels)
	}
	if cfg.AI.RefinementTimeout != "45m" {
		t.Errorf("RefinementTimeout = %q, want 45m", cfg.AI.RefinementTimeout)
	}
}

func TestApplyEnvOverrides_IssueTracking_BlankCSVPreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.GitHub.IssueTracking.DevelopLabels = []string{"existing"}

	t.Setenv("HEIMDALLM_ISSUE_DEVELOP_LABELS", "  ,  ,  ")
	cfg.applyEnvOverrides()

	if len(cfg.GitHub.IssueTracking.DevelopLabels) != 1 || cfg.GitHub.IssueTracking.DevelopLabels[0] != "existing" {
		t.Errorf("expected existing value to be preserved, got %v", cfg.GitHub.IssueTracking.DevelopLabels)
	}
}

func TestValidate_IssueTrackingDisabledSkipsChecks(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{Primary: "claude"},
		GitHub: GitHubConfig{
			IssueTracking: IssueTrackingConfig{
				Enabled:       false,
				FilterMode:    FilterMode("nonsense"),
				DefaultAction: "also nonsense",
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled issue_tracking should skip validation, got: %v", err)
	}
}

func TestValidate_IssueTrackingInvalidFilterMode(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{Primary: "claude"},
		GitHub: GitHubConfig{
			IssueTracking: IssueTrackingConfig{
				Enabled:       true,
				FilterMode:    FilterMode("excluive"),
				DefaultAction: string(IssueModeIgnore),
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid filter_mode")
	}
	if !strings.Contains(err.Error(), "filter_mode") {
		t.Errorf("error should mention filter_mode, got: %v", err)
	}
}

func TestValidate_IssueTrackingInvalidDefaultAction(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{Primary: "claude"},
		GitHub: GitHubConfig{
			IssueTracking: IssueTrackingConfig{
				Enabled:       true,
				FilterMode:    FilterModeExclusive,
				DefaultAction: "auto_implement",
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid default_action")
	}
	if !strings.Contains(err.Error(), "default_action") {
		t.Errorf("error should mention default_action, got: %v", err)
	}
}

func TestValidate_IssueTrackingEnabledDefaultsPassValidation(t *testing.T) {
	cfg := &Config{AI: AIConfig{Primary: "claude"}}
	cfg.GitHub.IssueTracking.Enabled = true
	cfg.applyDefaults() // fills FilterMode + DefaultAction

	if err := cfg.Validate(); err != nil {
		t.Errorf("applyDefaults + Enabled should pass validation, got: %v", err)
	}
}

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

func TestValidate_OrgIssueTrackingOverride(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary: "claude",
			Orgs: map[string]OrgAI{
				"org": {
					IssueTracking: &IssueTrackingOverride{
						Enabled:       testBoolPtr(true),
						FilterMode:    FilterMode("bad-mode"),
						DefaultAction: string(IssueModeIgnore),
					},
				},
			},
		},
	}
	cfg.applyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate with invalid org issue_tracking = nil, want error")
	}
	if !strings.Contains(err.Error(), `ai.orgs."org".issue_tracking.filter_mode`) {
		t.Fatalf("error should name org issue_tracking path, got: %v", err)
	}
}

// ── Issue classification ─────────────────────────────────────────────────────

func TestClassify_Precedence(t *testing.T) {
	cfg := IssueTrackingConfig{
		SkipLabels:       []string{"wontfix"},
		ReviewOnlyLabels: []string{"question", "discussion"},
		RefinementLabels: []string{"refine"},
		DevelopLabels:    []string{"bug", "enhancement"},
		DefaultAction:    string(IssueModeIgnore),
	}
	cases := []struct {
		name   string
		labels []string
		want   IssueMode
	}{
		{"skip wins over review_only + develop", []string{"wontfix", "question", "bug"}, IssueModeIgnore},
		{"review_only wins over refinement + develop", []string{"question", "refine", "bug"}, IssueModeReviewOnly},
		{"review_only wins over develop when both present", []string{"question", "bug"}, IssueModeReviewOnly},
		{"refinement wins over develop when both present", []string{"refine", "bug"}, IssueModeRefinement},
		{"develop only", []string{"bug"}, IssueModeDevelop},
		{"refinement only", []string{"refine"}, IssueModeRefinement},
		{"review_only only", []string{"question"}, IssueModeReviewOnly},
		{"unrelated labels fall back to default_action=ignore", []string{"help-wanted"}, IssueModeIgnore},
		{"no labels fall back to default_action=ignore", nil, IssueModeIgnore},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.Classify(tc.labels); got != tc.want {
				t.Errorf("Classify(%v) = %q, want %q", tc.labels, got, tc.want)
			}
		})
	}
}

func TestClassify_BlockedPrecedence(t *testing.T) {
	// Precedence must be: skip > blocked > review_only > refinement > develop > default.
	// Blocked slots in between skip (don't touch it) and develop/review_only
	// (blocked is cheaper than any processing — we haven't even confirmed we
	// want to run it yet). Stage labels then prefer the earliest state.
	cfg := IssueTrackingConfig{
		SkipLabels:       []string{"wontfix"},
		BlockedLabels:    []string{"blocked"},
		RefinementLabels: []string{"refine"},
		ReviewOnlyLabels: []string{"question"},
		DevelopLabels:    []string{"bug"},
		DefaultAction:    string(IssueModeIgnore),
	}
	cases := []struct {
		name   string
		labels []string
		want   IssueMode
	}{
		{"skip wins over blocked", []string{"wontfix", "blocked", "bug"}, IssueModeIgnore},
		{"blocked wins over review_only", []string{"blocked", "question"}, IssueModeBlocked},
		{"blocked wins over refinement", []string{"blocked", "refine"}, IssueModeBlocked},
		{"blocked wins over develop", []string{"blocked", "bug"}, IssueModeBlocked},
		{"blocked alone", []string{"blocked"}, IssueModeBlocked},
		{"review_only wins over develop without blocked", []string{"question", "bug"}, IssueModeReviewOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.Classify(tc.labels); got != tc.want {
				t.Errorf("Classify(%v) = %q, want %q", tc.labels, got, tc.want)
			}
		})
	}
}

func TestResolvePromoteToLabel(t *testing.T) {
	cases := []struct {
		name      string
		cfg       IssueTrackingConfig
		wantLabel string
	}{
		{
			name:      "explicit target wins",
			cfg:       IssueTrackingConfig{PromoteToLabel: "develop", DevelopLabels: []string{"feature"}},
			wantLabel: "develop",
		},
		{
			name:      "falls back to first develop label",
			cfg:       IssueTrackingConfig{DevelopLabels: []string{"feature", "bug"}},
			wantLabel: "feature",
		},
		{
			name:      "empty when neither is set",
			cfg:       IssueTrackingConfig{},
			wantLabel: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ResolvePromoteToLabel(); got != tc.wantLabel {
				t.Errorf("ResolvePromoteToLabel = %q, want %q", got, tc.wantLabel)
			}
		})
	}
}

func TestApplyEnvOverrides_IssueTracking_Blocked(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	t.Setenv("HEIMDALLM_ISSUE_BLOCKED_LABELS", "blocked, heimdallm-queued")
	t.Setenv("HEIMDALLM_ISSUE_PROMOTE_TO_LABEL", "ready")

	cfg.applyEnvOverrides()

	it := cfg.GitHub.IssueTracking
	if len(it.BlockedLabels) != 2 || it.BlockedLabels[0] != "blocked" || it.BlockedLabels[1] != "heimdallm-queued" {
		t.Errorf("BlockedLabels = %v, want [blocked heimdallm-queued]", it.BlockedLabels)
	}
	if it.PromoteToLabel != "ready" {
		t.Errorf("PromoteToLabel = %q, want ready", it.PromoteToLabel)
	}
}

func TestValidate_BlockedLabelsRequirePromoteTarget(t *testing.T) {
	// A blocked label dimension without any way to resolve a promote-to
	// label is a misconfiguration: issues would get stuck in blocked state
	// forever with no target to promote them to.
	cfg := &Config{AI: AIConfig{Primary: "claude"}}
	cfg.GitHub.IssueTracking.Enabled = true
	cfg.GitHub.IssueTracking.BlockedLabels = []string{"blocked"}
	cfg.GitHub.IssueTracking.FilterMode = FilterModeExclusive
	cfg.GitHub.IssueTracking.DefaultAction = string(IssueModeIgnore)
	// No PromoteToLabel, no DevelopLabels → can't resolve a target.

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate with BlockedLabels and no promote target: expected error, got nil")
	}
}

func TestValidate_BlockedLabelsOK_WhenDevelopLabelsSet(t *testing.T) {
	// BlockedLabels + DevelopLabels is a valid combo — the first develop
	// label is the implicit promote-to target.
	cfg := &Config{AI: AIConfig{Primary: "claude"}}
	cfg.GitHub.IssueTracking.Enabled = true
	cfg.GitHub.IssueTracking.BlockedLabels = []string{"blocked"}
	cfg.GitHub.IssueTracking.DevelopLabels = []string{"feature"}
	cfg.GitHub.IssueTracking.FilterMode = FilterModeExclusive
	cfg.GitHub.IssueTracking.DefaultAction = string(IssueModeIgnore)

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate(BlockedLabels + DevelopLabels) = %v, want nil", err)
	}
}

func TestClassify_CaseInsensitive(t *testing.T) {
	cfg := IssueTrackingConfig{
		ReviewOnlyLabels: []string{"Question"},
		DevelopLabels:    []string{"BUG"},
		DefaultAction:    string(IssueModeIgnore),
	}
	if got := cfg.Classify([]string{"bug"}); got != IssueModeDevelop {
		t.Errorf("Classify(bug) = %q, want develop (case-insensitive)", got)
	}
	if got := cfg.Classify([]string{"QUESTION"}); got != IssueModeReviewOnly {
		t.Errorf("Classify(QUESTION) = %q, want review_only", got)
	}
}

func TestClassify_DefaultActionReviewOnly(t *testing.T) {
	cfg := IssueTrackingConfig{
		DevelopLabels: []string{"bug"},
		DefaultAction: string(IssueModeReviewOnly),
	}
	if got := cfg.Classify([]string{"help-wanted"}); got != IssueModeReviewOnly {
		t.Errorf("Classify unrelated label = %q, want review_only (default_action)", got)
	}
}

func TestClassify_TrimsWhitespace(t *testing.T) {
	cfg := IssueTrackingConfig{
		DevelopLabels: []string{"  bug  "},
		DefaultAction: string(IssueModeIgnore),
	}
	if got := cfg.Classify([]string{"bug"}); got != IssueModeDevelop {
		t.Errorf("Classify should trim whitespace in configured labels, got %q", got)
	}
}

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

// ── AIForRepo 3-level PR metadata resolution ────────────────────────────────

func TestAIForRepo_GlobalPRMetadata(t *testing.T) {
	draft := true
	cfg := &Config{
		AI: AIConfig{
			Primary:     "claude",
			PRReviewers: []string{"alice", "bob"},
			PRLabels:    []string{"auto-generated"},
			PRAssignee:  "charlie",
			PRDraft:     &draft,
		},
	}

	r := cfg.AIForRepo("any/repo")
	if len(r.PRReviewers) != 2 || r.PRReviewers[0] != "alice" {
		t.Errorf("PRReviewers = %v, want [alice bob]", r.PRReviewers)
	}
	if len(r.PRLabels) != 1 || r.PRLabels[0] != "auto-generated" {
		t.Errorf("PRLabels = %v, want [auto-generated]", r.PRLabels)
	}
	if r.PRAssignee != "charlie" {
		t.Errorf("PRAssignee = %q, want charlie", r.PRAssignee)
	}
	if r.PRDraft == nil || !*r.PRDraft {
		t.Error("PRDraft should be true from global")
	}
}

func TestAIForRepo_GlobalPRMetadataFromNestedSection(t *testing.T) {
	draft := true
	cfg := &Config{
		AI: AIConfig{
			Primary: "claude",
			PRMetadata: PRMetadataConfig{
				Reviewers: []string{"alice"},
				Labels:    []string{"nested-label"},
				Assignee:  "nested-assignee",
				Draft:     &draft,
			},
		},
	}

	r := cfg.AIForRepo("any/repo")
	if len(r.PRReviewers) != 1 || r.PRReviewers[0] != "alice" {
		t.Errorf("PRReviewers = %v, want [alice]", r.PRReviewers)
	}
	if len(r.PRLabels) != 1 || r.PRLabels[0] != "nested-label" {
		t.Errorf("PRLabels = %v, want [nested-label]", r.PRLabels)
	}
	if r.PRAssignee != "nested-assignee" {
		t.Errorf("PRAssignee = %q, want nested-assignee", r.PRAssignee)
	}
	if r.PRDraft == nil || !*r.PRDraft {
		t.Error("PRDraft should be true from nested section")
	}
}

func TestAIForRepo_FlatFieldsWinOverNestedSection(t *testing.T) {
	nestedDraft := true
	flatDraft := false
	cfg := &Config{
		AI: AIConfig{
			Primary: "claude",
			PRMetadata: PRMetadataConfig{
				Reviewers: []string{"nested"},
				Labels:    []string{"nested-label"},
				Assignee:  "nested-assignee",
				Draft:     &nestedDraft,
			},
			PRReviewers: []string{"flat"},
			PRLabels:    []string{"flat-label"},
			PRAssignee:  "flat-assignee",
			PRDraft:     &flatDraft,
		},
	}

	r := cfg.AIForRepo("any/repo")
	if r.PRReviewers[0] != "flat" {
		t.Errorf("PRReviewers = %v, want [flat] (flat wins over nested)", r.PRReviewers)
	}
	if r.PRLabels[0] != "flat-label" {
		t.Errorf("PRLabels = %v, want [flat-label]", r.PRLabels)
	}
	if r.PRAssignee != "flat-assignee" {
		t.Errorf("PRAssignee = %q, want flat-assignee", r.PRAssignee)
	}
	if r.PRDraft != nil && *r.PRDraft {
		t.Error("PRDraft should be false (flat wins over nested)")
	}
}

func TestAIForRepo_OrgOverridesGlobal(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:     "claude",
			PRReviewers: []string{"global-r1"},
			PRLabels:    []string{"global-label"},
			PRAssignee:  "global-assignee",
			Orgs: map[string]OrgAI{
				"myorg": {
					PRReviewers: []string{"org-r1", "org-r2"},
					PRLabels:    []string{"org-label"},
					PRAssignee:  "org-assignee",
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/some-repo")
	if len(r.PRReviewers) != 2 || r.PRReviewers[0] != "org-r1" {
		t.Errorf("PRReviewers = %v, want [org-r1 org-r2]", r.PRReviewers)
	}
	if r.PRLabels[0] != "org-label" {
		t.Errorf("PRLabels = %v, want [org-label]", r.PRLabels)
	}
	if r.PRAssignee != "org-assignee" {
		t.Errorf("PRAssignee = %q, want org-assignee", r.PRAssignee)
	}
}

func TestAIForRepo_OrgInheritsGlobalForUnsetFields(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:     "claude",
			PRReviewers: []string{"global-r1"},
			PRLabels:    []string{"global-label"},
			PRAssignee:  "global-assignee",
			Orgs: map[string]OrgAI{
				"myorg": {
					PRReviewers: []string{"org-r1"},
					// PRLabels and PRAssignee not set → inherit global
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/repo")
	if r.PRReviewers[0] != "org-r1" {
		t.Errorf("PRReviewers = %v, want [org-r1] (org override)", r.PRReviewers)
	}
	if r.PRLabels[0] != "global-label" {
		t.Errorf("PRLabels = %v, want [global-label] (inherited from global)", r.PRLabels)
	}
	if r.PRAssignee != "global-assignee" {
		t.Errorf("PRAssignee = %q, want global-assignee (inherited)", r.PRAssignee)
	}
}

func TestAIForRepo_RepoOverridesOrg(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:     "claude",
			PRReviewers: []string{"global-r1"},
			PRLabels:    []string{"global-label"},
			PRAssignee:  "global-assignee",
			Orgs: map[string]OrgAI{
				"myorg": {
					PRReviewers: []string{"org-r1", "org-r2"},
					PRLabels:    []string{"org-label"},
					PRAssignee:  "org-assignee",
				},
			},
			Repos: map[string]RepoAI{
				"myorg/special": {
					PRReviewers: []string{"repo-r1"},
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/special")
	if len(r.PRReviewers) != 1 || r.PRReviewers[0] != "repo-r1" {
		t.Errorf("PRReviewers = %v, want [repo-r1] (repo override)", r.PRReviewers)
	}
	// PRLabels not set per-repo → inherits org
	if r.PRLabels[0] != "org-label" {
		t.Errorf("PRLabels = %v, want [org-label] (inherited from org)", r.PRLabels)
	}
	// PRAssignee not set per-repo → inherits org
	if r.PRAssignee != "org-assignee" {
		t.Errorf("PRAssignee = %q, want org-assignee (inherited from org)", r.PRAssignee)
	}
}

func TestAIForRepo_RepoOverridesOrgAndGlobal(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:     "claude",
			PRReviewers: []string{"global-r1"},
			PRLabels:    []string{"global-label"},
			PRAssignee:  "global-assignee",
			Orgs: map[string]OrgAI{
				"myorg": {
					PRReviewers: []string{"org-r1"},
					PRLabels:    []string{"org-label"},
					PRAssignee:  "org-assignee",
				},
			},
			Repos: map[string]RepoAI{
				"myorg/special": {
					PRReviewers: []string{"repo-r1"},
					PRLabels:    []string{"repo-label"},
					PRAssignee:  "repo-assignee",
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/special")
	if r.PRReviewers[0] != "repo-r1" {
		t.Errorf("PRReviewers = %v, want [repo-r1]", r.PRReviewers)
	}
	if r.PRLabels[0] != "repo-label" {
		t.Errorf("PRLabels = %v, want [repo-label]", r.PRLabels)
	}
	if r.PRAssignee != "repo-assignee" {
		t.Errorf("PRAssignee = %q, want repo-assignee", r.PRAssignee)
	}
}

func TestAIForRepo_OrgDraftOverride(t *testing.T) {
	globalDraft := false
	orgDraft := true
	cfg := &Config{
		AI: AIConfig{
			Primary: "claude",
			PRDraft: &globalDraft,
			Orgs: map[string]OrgAI{
				"myorg": {PRDraft: &orgDraft},
			},
		},
	}

	r := cfg.AIForRepo("myorg/repo")
	if r.PRDraft == nil || !*r.PRDraft {
		t.Error("PRDraft should be true (org override)")
	}

	r2 := cfg.AIForRepo("other/repo")
	if r2.PRDraft != nil && *r2.PRDraft {
		t.Error("PRDraft should be false for repo in different org (global)")
	}
}

func TestAIForRepo_OrgOverridesAgentSelectionAndPrompts(t *testing.T) {
	autoTriage := true
	autoRefine := false
	genDesc := true
	cfg := &Config{
		AI: AIConfig{
			Primary:               "claude",
			Fallback:              "gemini",
			ReviewMode:            "single",
			IssuePrompt:           "global-issue",
			ImplementPrompt:       "global-impl",
			TriageOwner:           "global-owner",
			CloneDir:              "/tmp/global-clones",
			GeneratePRDescription: false,
			Orgs: map[string]OrgAI{
				"myorg": {
					Primary:               "codex",
					Fallback:              "opencode",
					ReviewMode:            "multi",
					Prompt:                "org-pr",
					IssuePrompt:           "org-issue",
					ImplementPrompt:       "org-impl",
					TriageOwner:           "org-owner",
					CloneDir:              "/tmp/org-clones",
					AutoPromoteTriage:     &autoTriage,
					AutoPromoteRefinement: &autoRefine,
					GeneratePRDescription: &genDesc,
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/repo")
	if r.Primary != "codex" || r.Fallback != "opencode" || r.ReviewMode != "multi" {
		t.Fatalf("agent selection = (%q,%q,%q), want org values", r.Primary, r.Fallback, r.ReviewMode)
	}
	if r.Prompt != "org-pr" || r.IssuePrompt != "org-issue" || r.ImplementPrompt != "org-impl" {
		t.Fatalf("prompts = (%q,%q,%q), want org prompts", r.Prompt, r.IssuePrompt, r.ImplementPrompt)
	}
	if r.TriageOwner != "org-owner" || r.CloneDir != "/tmp/org-clones" {
		t.Fatalf("future fields = (%q,%q), want org values", r.TriageOwner, r.CloneDir)
	}
	if r.AutoPromoteTriage == nil || !*r.AutoPromoteTriage {
		t.Fatal("AutoPromoteTriage should inherit org true")
	}
	if r.AutoPromoteRefinement == nil || *r.AutoPromoteRefinement {
		t.Fatal("AutoPromoteRefinement should inherit org false")
	}
	if r.GeneratePRDescription == nil || !*r.GeneratePRDescription {
		t.Fatal("GeneratePRDescription should inherit org true")
	}
}

func TestAIForRepo_RepoOverridesOrgAgentSelectionAndPrompts(t *testing.T) {
	orgAuto := true
	repoAuto := false
	cfg := &Config{
		AI: AIConfig{
			Primary: "claude",
			Orgs: map[string]OrgAI{
				"myorg": {
					Primary:           "codex",
					Prompt:            "org-pr",
					IssuePrompt:       "org-issue",
					AutoPromoteTriage: &orgAuto,
				},
			},
			Repos: map[string]RepoAI{
				"myorg/repo": {
					Primary:           "gemini",
					Prompt:            "repo-pr",
					IssuePrompt:       "repo-issue",
					AutoPromoteTriage: &repoAuto,
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/repo")
	if r.Primary != "gemini" || r.Prompt != "repo-pr" || r.IssuePrompt != "repo-issue" {
		t.Fatalf("repo fields did not override org: %+v", r)
	}
	if r.AutoPromoteTriage == nil || *r.AutoPromoteTriage {
		t.Fatal("repo AutoPromoteTriage=false should override org true")
	}
}

func TestAIForRepo_IndependentFieldResolution(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:     "claude",
			PRReviewers: []string{"global-r1"},
			PRLabels:    []string{"global-label"},
			PRAssignee:  "global-assignee",
			Orgs: map[string]OrgAI{
				"myorg": {
					PRLabels: []string{"org-label"},
				},
			},
			Repos: map[string]RepoAI{
				"myorg/special": {
					PRAssignee: "repo-assignee",
				},
			},
		},
	}

	r := cfg.AIForRepo("myorg/special")
	if r.PRReviewers[0] != "global-r1" {
		t.Errorf("PRReviewers = %v, want [global-r1] (global, no org/repo override)", r.PRReviewers)
	}
	if r.PRLabels[0] != "org-label" {
		t.Errorf("PRLabels = %v, want [org-label] (org level, no repo override)", r.PRLabels)
	}
	if r.PRAssignee != "repo-assignee" {
		t.Errorf("PRAssignee = %q, want repo-assignee (repo level)", r.PRAssignee)
	}
}

func TestAIForRepo_NoOrgMatch_FallsToGlobal(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:     "claude",
			PRReviewers: []string{"global-r1"},
			Orgs: map[string]OrgAI{
				"differentorg": {PRReviewers: []string{"other-r1"}},
			},
		},
	}

	r := cfg.AIForRepo("myorg/repo")
	if r.PRReviewers[0] != "global-r1" {
		t.Errorf("PRReviewers = %v, want [global-r1] (no org match)", r.PRReviewers)
	}
}

func TestAIForRepo_GeneratePRDescriptionInheritsGlobal(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Primary:               "claude",
			GeneratePRDescription: true,
		},
	}

	r := cfg.AIForRepo("org/repo")
	if r.GeneratePRDescription == nil || !*r.GeneratePRDescription {
		t.Error("GeneratePRDescription should inherit true from global")
	}
}

func TestAIForRepo_GeneratePRDescriptionPerRepoOverride(t *testing.T) {
	genTrue := true
	genFalse := false
	cfg := &Config{
		AI: AIConfig{
			Primary:               "claude",
			GeneratePRDescription: true,
			Repos: map[string]RepoAI{
				"org/enabled":  {GeneratePRDescription: &genTrue},
				"org/disabled": {GeneratePRDescription: &genFalse},
			},
		},
	}

	r1 := cfg.AIForRepo("org/enabled")
	if r1.GeneratePRDescription == nil || !*r1.GeneratePRDescription {
		t.Error("per-repo enabled override should be true")
	}

	r2 := cfg.AIForRepo("org/disabled")
	if r2.GeneratePRDescription == nil || *r2.GeneratePRDescription {
		t.Error("per-repo disabled override should be false")
	}
}

func TestApplyEnvOverrides_GeneratePRDescription(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	t.Setenv("HEIMDALLM_GENERATE_PR_DESCRIPTION", "true")
	cfg.applyEnvOverrides()
	if !cfg.AI.GeneratePRDescription {
		t.Error("GeneratePRDescription should be true from env")
	}
}

func TestApplyEnvOverrides_PRAssigneeAndDraft(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	t.Setenv("HEIMDALLM_PR_REVIEWERS", "alice,bob")
	t.Setenv("HEIMDALLM_PR_LABELS", "auto-generated")
	t.Setenv("HEIMDALLM_PR_ASSIGNEE", "charlie")
	t.Setenv("HEIMDALLM_PR_DRAFT", "true")

	cfg.applyEnvOverrides()

	if len(cfg.AI.PRReviewers) != 2 || cfg.AI.PRReviewers[0] != "alice" {
		t.Errorf("PRReviewers = %v, want [alice bob]", cfg.AI.PRReviewers)
	}
	if len(cfg.AI.PRLabels) != 1 || cfg.AI.PRLabels[0] != "auto-generated" {
		t.Errorf("PRLabels = %v, want [auto-generated]", cfg.AI.PRLabels)
	}
	if cfg.AI.PRAssignee != "charlie" {
		t.Errorf("PRAssignee = %q, want charlie", cfg.AI.PRAssignee)
	}
	if cfg.AI.PRDraft == nil || !*cfg.AI.PRDraft {
		t.Error("PRDraft should be true from env")
	}
}

func TestAIForRepo_EnvPRMetadataFlowsToRepo(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"

	t.Setenv("HEIMDALLM_PR_REVIEWERS", "env-r1,env-r2")
	t.Setenv("HEIMDALLM_PR_LABELS", "env-label")
	t.Setenv("HEIMDALLM_PR_ASSIGNEE", "env-assignee")
	t.Setenv("HEIMDALLM_PR_DRAFT", "true")

	cfg.applyEnvOverrides()

	r := cfg.AIForRepo("any/repo")
	if len(r.PRReviewers) != 2 || r.PRReviewers[0] != "env-r1" {
		t.Errorf("PRReviewers = %v, want [env-r1 env-r2]", r.PRReviewers)
	}
	if r.PRLabels[0] != "env-label" {
		t.Errorf("PRLabels = %v, want [env-label]", r.PRLabels)
	}
	if r.PRAssignee != "env-assignee" {
		t.Errorf("PRAssignee = %q, want env-assignee", r.PRAssignee)
	}
	if r.PRDraft == nil || !*r.PRDraft {
		t.Error("PRDraft should be true from env")
	}
}

func TestAIForRepo_TOMLThreeLevelResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[ai]
primary = "claude"
pr_reviewers = ["global-r1", "global-r2"]
pr_labels = ["global-label"]
pr_assignee = "global-assignee"
pr_draft = false

[ai.orgs."freepik-company"]
pr_reviewers = ["org-r1"]
pr_labels = ["org-label", "ai-platform"]

[ai.orgs."theburrowhub"]
pr_reviewers = ["org-r1", "org-r2", "org-r3"]

[ai.repos."freepik-company/data_contracts"]
pr_reviewers = ["data-lead"]
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
	if len(r.PRReviewers) != 1 || r.PRReviewers[0] != "data-lead" {
		t.Errorf("PRReviewers = %v, want [data-lead] (per-repo)", r.PRReviewers)
	}
	if len(r.PRLabels) != 2 || r.PRLabels[0] != "org-label" {
		t.Errorf("PRLabels = %v, want [org-label ai-platform] (from org)", r.PRLabels)
	}
	if r.PRAssignee != "global-assignee" {
		t.Errorf("PRAssignee = %q, want global-assignee (inherited)", r.PRAssignee)
	}

	// Org-level (no per-repo)
	r2 := cfg.AIForRepo("freepik-company/other-repo")
	if len(r2.PRReviewers) != 1 || r2.PRReviewers[0] != "org-r1" {
		t.Errorf("PRReviewers = %v, want [org-r1] (org level)", r2.PRReviewers)
	}
	if r2.PRLabels[0] != "org-label" {
		t.Errorf("PRLabels = %v, want [org-label ai-platform]", r2.PRLabels)
	}

	// Different org
	r3 := cfg.AIForRepo("theburrowhub/heimdallm")
	if len(r3.PRReviewers) != 3 || r3.PRReviewers[0] != "org-r1" {
		t.Errorf("PRReviewers = %v, want [org-r1 org-r2 org-r3]", r3.PRReviewers)
	}
	if r3.PRLabels[0] != "global-label" {
		t.Errorf("PRLabels = %v, want [global-label] (no org override for labels)", r3.PRLabels)
	}

	// Unknown org → global
	r4 := cfg.AIForRepo("unknown/repo")
	if len(r4.PRReviewers) != 2 || r4.PRReviewers[0] != "global-r1" {
		t.Errorf("PRReviewers = %v, want [global-r1 global-r2] (global)", r4.PRReviewers)
	}
}

func TestAIForRepo_ThreeLevelAllScopedFields(t *testing.T) {
	globalTriage := false
	globalRefine := true
	globalDraft := false
	orgTriage := true
	orgRefine := false
	orgDraft := true
	orgGenDesc := true
	repoRefine := true
	repoDraft := false
	repoGenDesc := false

	cfg := &Config{
		AI: AIConfig{
			Primary:               "global-primary",
			Fallback:              "global-fallback",
			ReviewMode:            "single",
			IssuePrompt:           "global-issue",
			ImplementPrompt:       "global-impl",
			TriageOwner:           "global-owner",
			CloneDir:              "global-clone",
			AutoPromoteTriage:     &globalTriage,
			AutoPromoteRefinement: &globalRefine,
			GeneratePRDescription: false,
			PRReviewers:           []string{"global-reviewer"},
			PRLabels:              []string{"global-label"},
			PRAssignee:            "global-assignee",
			PRDraft:               &globalDraft,
			Orgs: map[string]OrgAI{
				"org": {
					Primary:               "org-primary",
					Fallback:              "org-fallback",
					ReviewMode:            "multi",
					Prompt:                "org-prompt",
					IssuePrompt:           "org-issue",
					ImplementPrompt:       "org-impl",
					LocalDir:              "/org/local",
					TriageOwner:           "org-owner",
					CloneDir:              "org-clone",
					AutoPromoteTriage:     &orgTriage,
					AutoPromoteRefinement: &orgRefine,
					PRReviewers:           []string{"org-reviewer"},
					PRAssignee:            "org-assignee",
					PRLabels:              []string{"org-label"},
					PRDraft:               &orgDraft,
					GeneratePRDescription: &orgGenDesc,
				},
			},
			Repos: map[string]RepoAI{
				"org/repo": {
					Primary:               "repo-primary",
					Prompt:                "repo-prompt",
					ImplementPrompt:       "repo-impl",
					LocalDir:              "/repo/local",
					CloneDir:              "repo-clone",
					AutoPromoteRefinement: &repoRefine,
					PRLabels:              []string{"repo-label"},
					PRDraft:               &repoDraft,
					GeneratePRDescription: &repoGenDesc,
				},
			},
		},
	}

	got := cfg.AIForRepo("org/repo")
	if got.Primary != "repo-primary" || got.Fallback != "org-fallback" || got.ReviewMode != "multi" {
		t.Fatalf("agent selection = (%q,%q,%q), want repo/org/org", got.Primary, got.Fallback, got.ReviewMode)
	}
	if got.Prompt != "repo-prompt" || got.IssuePrompt != "org-issue" || got.ImplementPrompt != "repo-impl" {
		t.Fatalf("prompts = (%q,%q,%q), want repo/org/repo", got.Prompt, got.IssuePrompt, got.ImplementPrompt)
	}
	if got.LocalDir != "/repo/local" || got.TriageOwner != "org-owner" || got.CloneDir != "repo-clone" {
		t.Fatalf("paths/owners = (%q,%q,%q), want repo/org/repo", got.LocalDir, got.TriageOwner, got.CloneDir)
	}
	if got.AutoPromoteTriage == nil || !*got.AutoPromoteTriage {
		t.Fatal("AutoPromoteTriage should come from org")
	}
	if got.AutoPromoteRefinement == nil || !*got.AutoPromoteRefinement {
		t.Fatal("AutoPromoteRefinement should come from repo")
	}
	if len(got.PRReviewers) != 1 || got.PRReviewers[0] != "org-reviewer" {
		t.Fatalf("PRReviewers = %v, want org reviewer", got.PRReviewers)
	}
	if got.PRAssignee != "org-assignee" || len(got.PRLabels) != 1 || got.PRLabels[0] != "repo-label" {
		t.Fatalf("PR metadata = assignee %q labels %v, want org/repo", got.PRAssignee, got.PRLabels)
	}
	if got.PRDraft == nil || *got.PRDraft {
		t.Fatal("PRDraft should come from repo false")
	}
	if got.GeneratePRDescription == nil || *got.GeneratePRDescription {
		t.Fatal("GeneratePRDescription should come from repo false")
	}
}

func TestAIForRepo_EmptyPRMetadataOverridesClearInheritedValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[ai]
primary = "claude"
pr_reviewers = ["global-r1"]
pr_labels = ["global-label"]

[ai.orgs."org"]
pr_reviewers = []
pr_labels = ["org-label"]

[ai.repos."org/repo"]
pr_labels = []
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	r := cfg.AIForRepo("org/repo")
	if len(r.PRReviewers) != 0 {
		t.Fatalf("PRReviewers = %v, want explicit org clear", r.PRReviewers)
	}
	if len(r.PRLabels) != 0 {
		t.Fatalf("PRLabels = %v, want explicit repo clear", r.PRLabels)
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

// ── IssueTrackingForRepo ─────────────────────────────────────────────────────

func TestIssueTrackingForRepo_GlobalOnly(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:       true,
		FilterMode:    FilterModeExclusive,
		DevelopLabels: []string{"bug", "feature"},
		SkipLabels:    []string{"wontfix"},
		DefaultAction: "ignore",
	}
	got := c.IssueTrackingForRepo("org/repo")
	if !got.Enabled || got.FilterMode != FilterModeExclusive {
		t.Errorf("expected global values, got %+v", got)
	}
	if len(got.DevelopLabels) != 2 || got.DevelopLabels[0] != "bug" {
		t.Errorf("develop_labels = %v, want [bug feature]", got.DevelopLabels)
	}
}

func TestIssueTrackingForRepo_OrgOverride(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:          true,
		FilterMode:       FilterModeExclusive,
		DevelopLabels:    []string{"global-dev"},
		RefinementLabels: []string{"global-refine"},
		ReviewOnlyLabels: []string{"global-review"},
		SkipLabels:       []string{"global-skip"},
		BlockedLabels:    []string{"global-blocked"},
		PromoteToLabel:   "global-ready",
		DefaultAction:    "ignore",
	}
	c.AI.Orgs = map[string]OrgAI{
		"org": {
			IssueTracking: &IssueTrackingOverride{
				FilterMode:       FilterModeInclusive,
				DevelopLabels:    []string{"org-dev"},
				RefinementLabels: []string{"org-refine"},
				ReviewOnlyLabels: []string{"org-review"},
				BlockedLabels:    []string{"org-blocked"},
				PromoteToLabel:   "org-ready",
			},
		},
	}

	got := c.IssueTrackingForRepo("org/repo")
	if !got.Enabled {
		t.Fatal("enabled should inherit true from global")
	}
	if got.FilterMode != FilterModeInclusive {
		t.Errorf("filter_mode = %q, want org override", got.FilterMode)
	}
	if len(got.DevelopLabels) != 1 || got.DevelopLabels[0] != "org-dev" {
		t.Errorf("develop_labels = %v, want org override", got.DevelopLabels)
	}
	if len(got.RefinementLabels) != 1 || got.RefinementLabels[0] != "org-refine" {
		t.Errorf("refinement_labels = %v, want org override", got.RefinementLabels)
	}
	if len(got.SkipLabels) != 1 || got.SkipLabels[0] != "global-skip" {
		t.Errorf("skip_labels = %v, want inherited global skip label", got.SkipLabels)
	}
	if len(got.BlockedLabels) != 1 || got.BlockedLabels[0] != "org-blocked" {
		t.Errorf("blocked_labels = %v, want org override", got.BlockedLabels)
	}
	if got.PromoteToLabel != "org-ready" {
		t.Errorf("promote_to_label = %q, want org-ready", got.PromoteToLabel)
	}
}

func TestIssueTrackingForRepo_RepoOverridesOrg(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:       true,
		FilterMode:    FilterModeExclusive,
		DevelopLabels: []string{"global-dev"},
		DefaultAction: "ignore",
	}
	c.AI.Orgs = map[string]OrgAI{
		"org": {
			IssueTracking: &IssueTrackingOverride{
				DevelopLabels: []string{"org-dev"},
				SkipLabels:    []string{"org-skip"},
			},
		},
	}
	c.AI.Repos = map[string]RepoAI{
		"org/repo": {
			IssueTracking: &IssueTrackingOverride{
				DevelopLabels: []string{"repo-dev"},
			},
		},
	}

	got := c.IssueTrackingForRepo("org/repo")
	if len(got.DevelopLabels) != 1 || got.DevelopLabels[0] != "repo-dev" {
		t.Errorf("develop_labels = %v, want repo override", got.DevelopLabels)
	}
	if len(got.SkipLabels) != 1 || got.SkipLabels[0] != "org-skip" {
		t.Errorf("skip_labels = %v, want inherited org override", got.SkipLabels)
	}
}

func TestIssueTrackingForRepo_ExplicitFalseDisablesInheritedEnabled(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: true, FilterMode: FilterModeExclusive, DefaultAction: "ignore"}
	c.AI.Orgs = map[string]OrgAI{
		"org": {
			IssueTracking: &IssueTrackingOverride{Enabled: testBoolPtr(false)},
		},
	}

	got := c.IssueTrackingForRepo("org/repo")
	if got.Enabled {
		t.Fatal("org enabled=false should disable global issue tracking for that org")
	}
}

func TestIssueTrackingForRepo_EmptyListClearsInheritedLabels(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:       true,
		FilterMode:    FilterModeExclusive,
		DevelopLabels: []string{"global-dev"},
		DefaultAction: "ignore",
	}
	c.AI.Orgs = map[string]OrgAI{
		"org": {
			IssueTracking: &IssueTrackingOverride{DevelopLabels: []string{}},
		},
	}

	got := c.IssueTrackingForRepo("org/repo")
	if got.DevelopLabels == nil || len(got.DevelopLabels) != 0 {
		t.Fatalf("develop_labels = %#v, want explicit empty override", got.DevelopLabels)
	}
}

func TestIssueTrackingForRepo_PerRepoOverride(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:       true,
		FilterMode:    FilterModeExclusive,
		DevelopLabels: []string{"bug", "feature"},
		SkipLabels:    []string{"wontfix"},
		DefaultAction: "ignore",
	}
	c.AI.Primary = "claude"
	c.AI.Repos = map[string]RepoAI{
		"org/secure-repo": {
			IssueTracking: &IssueTrackingOverride{
				Enabled:       testBoolPtr(true), // per-repo Enabled overrides global unconditionally
				DevelopLabels: []string{"security-fix"},
				SkipLabels:    []string{"wontfix", "stale"},
			},
		},
	}
	got := c.IssueTrackingForRepo("org/secure-repo")
	if len(got.DevelopLabels) != 1 || got.DevelopLabels[0] != "security-fix" {
		t.Errorf("develop_labels = %v, want [security-fix]", got.DevelopLabels)
	}
	if len(got.SkipLabels) != 2 {
		t.Errorf("skip_labels = %v, want [wontfix stale]", got.SkipLabels)
	}
	if got.FilterMode != FilterModeExclusive {
		t.Errorf("filter_mode = %v, want exclusive (inherited)", got.FilterMode)
	}
	if got.DefaultAction != "ignore" {
		t.Errorf("default_action = %v, want ignore (inherited)", got.DefaultAction)
	}
	if !got.Enabled {
		t.Error("enabled should be true (per-repo override)")
	}
}

func TestIssueTrackingForRepo_UnknownRepo(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:    true,
		SkipLabels: []string{"wontfix"},
	}
	got := c.IssueTrackingForRepo("org/unknown")
	if !got.Enabled || len(got.SkipLabels) != 1 {
		t.Errorf("unknown repo should return global, got %+v", got)
	}
}

func TestIssueTrackingForRepo_PerRepoEnablesWhenGlobalOff(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:       false,
		DevelopLabels: []string{"bug"},
	}
	c.AI.Repos = map[string]RepoAI{
		"org/active-repo": {
			IssueTracking: &IssueTrackingOverride{
				Enabled:       testBoolPtr(true),
				DevelopLabels: []string{"feature"},
			},
		},
	}
	got := c.IssueTrackingForRepo("org/active-repo")
	if !got.Enabled {
		t.Error("per-repo should enable issue tracking even when global is off")
	}
	if len(got.DevelopLabels) != 1 || got.DevelopLabels[0] != "feature" {
		t.Errorf("develop_labels = %v, want [feature]", got.DevelopLabels)
	}

	// Repo without override inherits global (disabled)
	got2 := c.IssueTrackingForRepo("org/other-repo")
	if got2.Enabled {
		t.Error("repo without override should inherit global disabled")
	}
}

func TestIssueTrackingForRepo_LabelsImplyEnabled(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: false}
	c.AI.Repos = map[string]RepoAI{
		"org/labels-only": {
			IssueTracking: &IssueTrackingOverride{
				// Enabled not set (false), but labels configured
				DevelopLabels:    []string{"heimdallm-auto-implement"},
				ReviewOnlyLabels: []string{"heimdallm-auto-refine"},
			},
		},
	}
	got := c.IssueTrackingForRepo("org/labels-only")
	if !got.Enabled {
		t.Error("repo with labels should be implicitly enabled")
	}
}

func TestIssueTrackingForRepo_TOMLLabelsWithoutEnabledImplicitlyEnable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[ai]
primary = "claude"

[ai.repos."org/repo".issue_tracking]
develop_labels = ["ready"]
review_only_labels = ["triage"]
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.IssueTrackingForRepo("org/repo")
	if !got.Enabled {
		t.Error("TOML labels without enabled should preserve implicit-enable behavior")
	}
}

func TestIssueTrackingForRepo_OrgLabelsImplyEnabled(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: false}
	c.AI.Orgs = map[string]OrgAI{
		"org": {
			IssueTracking: &IssueTrackingOverride{
				DevelopLabels:    []string{"heimdallm-auto-implement"},
				ReviewOnlyLabels: []string{"heimdallm-auto-refine"},
			},
		},
	}
	got := c.IssueTrackingForRepo("org/labels-only")
	if !got.Enabled {
		t.Error("org with labels should be implicitly enabled")
	}
}

func TestIssueTrackingForRepo_ExplicitFalseWinsOverOrgLabels(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: true}
	c.AI.Orgs = map[string]OrgAI{
		"org": {
			IssueTracking: &IssueTrackingOverride{
				Enabled:          testBoolPtr(false),
				DevelopLabels:    []string{"heimdallm-auto-implement"},
				ReviewOnlyLabels: []string{"heimdallm-auto-refine"},
			},
		},
	}
	got := c.IssueTrackingForRepo("org/labels-only")
	if got.Enabled {
		t.Error("explicit org enabled=false should win over labels")
	}
}

func TestIssueTrackingForRepo_RepoExplicitFalseWithLabelsDisables(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: true}
	c.AI.Repos = map[string]RepoAI{
		"org/repo": {
			IssueTracking: &IssueTrackingOverride{
				Enabled:          testBoolPtr(false),
				DevelopLabels:    []string{"heimdallm-auto-implement"},
				ReviewOnlyLabels: []string{"heimdallm-auto-refine"},
			},
		},
	}
	got := c.IssueTrackingForRepo("org/repo")
	if got.Enabled {
		t.Error("explicit repo enabled=false should win over labels")
	}
}

func TestIssueTrackingForRepo_OrgImplicitEnableSurvivesRepoMetadataOverride(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: false}
	c.AI.Orgs = map[string]OrgAI{
		"org": {
			IssueTracking: &IssueTrackingOverride{
				ReviewOnlyLabels: []string{"needs-triage"},
			},
		},
	}
	c.AI.Repos = map[string]RepoAI{
		"org/repo": {
			IssueTracking: &IssueTrackingOverride{
				PromoteToLabel: "ready",
			},
		},
	}
	got := c.IssueTrackingForRepo("org/repo")
	if !got.Enabled {
		t.Error("repo override without enabled or labels should not undo org implicit enable")
	}
}

func TestIssueTrackingForRepo_RepoLabelsReenableOrgDisabled(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: true}
	c.AI.Orgs = map[string]OrgAI{
		"org": {
			IssueTracking: &IssueTrackingOverride{Enabled: testBoolPtr(false)},
		},
	}
	c.AI.Repos = map[string]RepoAI{
		"org/repo": {
			IssueTracking: &IssueTrackingOverride{
				DevelopLabels: []string{"ready"},
			},
		},
	}
	got := c.IssueTrackingForRepo("org/repo")
	if !got.Enabled {
		t.Error("repo labels should re-enable tracking after org disabled it")
	}
}

func TestIssueTrackingForRepo_NoLabelsNoOverride(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: false}
	c.AI.Repos = map[string]RepoAI{
		"org/empty-override": {
			IssueTracking: &IssueTrackingOverride{},
		},
	}
	got := c.IssueTrackingForRepo("org/empty-override")
	if got.Enabled {
		t.Error("repo with empty override and global off should be disabled")
	}
}

func TestIssueTrackingForRepo_GlobalOnNoOverride(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: true, DevelopLabels: []string{"bug"}}
	got := c.IssueTrackingForRepo("org/no-override")
	if !got.Enabled {
		t.Error("repo without override should inherit global enabled")
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

[AI.REPOS."Org/Repo".ISSUE_TRACKING]
ENABLED = true
unknown_mixed = [4, "pointer"]

[AUTONOMOUS.REPOS."Org/Repo"]
ENABLED = true

[AUTONOMOUS.REPOS."org/repo"]
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
	if upperRepo.Primary != "codex" || upperRepo.IssueTracking == nil ||
		upperRepo.IssueTracking.Enabled == nil || !*upperRepo.IssueTracking.Enabled {
		t.Fatalf("upper-case repo fields/pointer were not decoded: %+v", upperRepo)
	}
	if lowerRepo.Primary != "gemini" {
		t.Fatalf("lower-case repo primary = %q, want gemini", lowerRepo.Primary)
	}
	upperAuto, upperAutoOK := cfg.Autonomous.Repos["Org/Repo"]
	lowerAuto, lowerAutoOK := cfg.Autonomous.Repos["org/repo"]
	if !upperAutoOK || !lowerAutoOK || len(cfg.Autonomous.Repos) != 2 {
		t.Fatalf("case-distinct autonomous repo keys collapsed: %#v", cfg.Autonomous.Repos)
	}
	if upperAuto.Enabled == nil || !*upperAuto.Enabled ||
		lowerAuto.Enabled == nil || *lowerAuto.Enabled {
		t.Fatalf("autonomous pointer aliases were not decoded: %#v", cfg.Autonomous.Repos)
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

func TestProjectKnownConfigMap_CurrentSchemaIsSupported(t *testing.T) {
	if _, err := projectKnownConfigMap(map[string]any{}); err != nil {
		t.Fatalf("Config schema is unsupported by canonical projection: %v", err)
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
