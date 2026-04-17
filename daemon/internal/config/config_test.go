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
