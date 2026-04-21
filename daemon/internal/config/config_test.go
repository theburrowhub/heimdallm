package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// ── applyEnvOverrides ────────────────────────────────────────────────────────

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
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"
	cfg.GitHub.PollInterval = "2m"

	if err := cfg.Validate(); err == nil {
		t.Error("Validate() = nil, want error for invalid poll_interval")
	}
}

func TestValidate_AllValidIntervals(t *testing.T) {
	for _, interval := range []string{"1m", "5m", "30m", "1h"} {
		cfg := &Config{AI: AIConfig{Primary: "claude"}, GitHub: GitHubConfig{PollInterval: interval}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with interval %q = %v", interval, err)
		}
	}
}

// ── Topic-based discovery ────────────────────────────────────────────────────

func TestApplyDefaults_DiscoveryIntervalWhenTopicSet(t *testing.T) {
	cfg := &Config{}
	cfg.GitHub.DiscoveryTopic = "heimdallm-review"
	cfg.applyDefaults()

	if cfg.GitHub.DiscoveryInterval != "15m" {
		t.Errorf("DiscoveryInterval = %q, want default %q", cfg.GitHub.DiscoveryInterval, "15m")
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

func TestApplyEnvOverrides_IssueTracking(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	t.Setenv("HEIMDALLM_ISSUE_TRACKING_ENABLED", "true")
	t.Setenv("HEIMDALLM_ISSUE_FILTER_MODE", "inclusive")
	t.Setenv("HEIMDALLM_ISSUE_DEFAULT_ACTION", "review_only")
	t.Setenv("HEIMDALLM_ISSUE_ORGANIZATIONS", "freepik-company, theburrowhub")
	t.Setenv("HEIMDALLM_ISSUE_ASSIGNEES", "sergiotejon")
	t.Setenv("HEIMDALLM_ISSUE_DEVELOP_LABELS", "enhancement,feature, bug")
	t.Setenv("HEIMDALLM_ISSUE_REVIEW_ONLY_LABELS", "question,discussion")
	t.Setenv("HEIMDALLM_ISSUE_SKIP_LABELS", "wontfix")

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
	if len(it.ReviewOnlyLabels) != 2 {
		t.Errorf("ReviewOnlyLabels = %v", it.ReviewOnlyLabels)
	}
	if len(it.SkipLabels) != 1 {
		t.Errorf("SkipLabels = %v", it.SkipLabels)
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

// ── Issue classification ─────────────────────────────────────────────────────

func TestClassify_Precedence(t *testing.T) {
	cfg := IssueTrackingConfig{
		SkipLabels:       []string{"wontfix"},
		ReviewOnlyLabels: []string{"question", "discussion"},
		DevelopLabels:    []string{"bug", "enhancement"},
		DefaultAction:    string(IssueModeIgnore),
	}
	cases := []struct {
		name   string
		labels []string
		want   IssueMode
	}{
		{"skip wins over review_only + develop", []string{"wontfix", "question", "bug"}, IssueModeIgnore},
		{"review_only wins over develop", []string{"question", "bug"}, IssueModeReviewOnly},
		{"develop only", []string{"bug"}, IssueModeDevelop},
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
	// Precedence must be: skip > blocked > review_only > develop > default.
	// Blocked slots in between skip (don't touch it) and review_only (blocked
	// is cheaper than any processing — we haven't even confirmed we want to
	// run it yet).
	cfg := IssueTrackingConfig{
		SkipLabels:       []string{"wontfix"},
		BlockedLabels:    []string{"blocked"},
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
		{"blocked wins over develop", []string{"blocked", "bug"}, IssueModeBlocked},
		{"blocked alone", []string{"blocked"}, IssueModeBlocked},
		{"review_only still wins over develop without blocked", []string{"question", "bug"}, IssueModeReviewOnly},
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
		name          string
		cfg           IssueTrackingConfig
		wantLabel     string
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
			IssueTracking: &IssueTrackingConfig{
				Enabled:       true, // per-repo Enabled overrides global unconditionally
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
			IssueTracking: &IssueTrackingConfig{
				Enabled:       true,
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
			IssueTracking: &IssueTrackingConfig{
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

func TestIssueTrackingForRepo_NoLabelsNoOverride(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{Enabled: false}
	c.AI.Repos = map[string]RepoAI{
		"org/empty-override": {
			IssueTracking: &IssueTrackingConfig{},
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
