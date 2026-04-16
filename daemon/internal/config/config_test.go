package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[github]
repositories = ["org/repo1"]

[ai]
primary = "claude"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 7842 {
		t.Errorf("expected default port 7842, got %d", cfg.Server.Port)
	}
	if cfg.GitHub.PollInterval != "5m" {
		t.Errorf("expected default poll interval 5m, got %s", cfg.GitHub.PollInterval)
	}
	if cfg.Retention.MaxDays != 90 {
		t.Errorf("expected default retention 90, got %d", cfg.Retention.MaxDays)
	}
}

func TestLoad_PerRepoAI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[github]
repositories = ["org/repo1"]

[ai]
primary = "claude"
fallback = "gemini"

[ai.repos."org/repo1"]
primary = "codex"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ai := cfg.AIForRepo("org/repo1")
	if ai.Primary != "codex" {
		t.Errorf("expected codex for org/repo1, got %s", ai.Primary)
	}
}

func TestValidate_EmptyReposAllowed(t *testing.T) {
	// Empty repositories is valid — daemon polls all user PRs without a repo filter
	cfg := &config.Config{
		AI: config.AIConfig{Primary: "claude"},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty repositories should be valid, got: %v", err)
	}
}

func TestValidate_InvalidInterval(t *testing.T) {
	cfg := &config.Config{
		GitHub: config.GitHubConfig{
			Repositories: []string{"org/repo"},
			PollInterval: "invalid",
		},
		AI: config.AIConfig{Primary: "claude"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for invalid poll interval")
	}
}
