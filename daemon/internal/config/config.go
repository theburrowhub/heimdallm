package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

var validIntervals = map[string]bool{
	"1m": true, "5m": true, "30m": true, "1h": true,
}

type Config struct {
	Server    ServerConfig    `toml:"server"`
	GitHub    GitHubConfig    `toml:"github"`
	AI        AIConfig        `toml:"ai"`
	Retention RetentionConfig `toml:"retention"`
}

type ServerConfig struct {
	Port int `toml:"port"`
}

type GitHubConfig struct {
	PollInterval string   `toml:"poll_interval"`
	Repositories []string `toml:"repositories"`
}

type AIConfig struct {
	Primary  string            `toml:"primary"`
	Fallback string            `toml:"fallback"`
	Repos    map[string]RepoAI `toml:"repos"`
}

type RepoAI struct {
	Primary  string `toml:"primary"`
	Fallback string `toml:"fallback"`
}

type RetentionConfig struct {
	MaxDays int `toml:"max_days"`
}

// AIForRepo returns the AI config for a specific repo, falling back to global.
func (c *Config) AIForRepo(repo string) RepoAI {
	if c.AI.Repos != nil {
		if r, ok := c.AI.Repos[repo]; ok {
			if r.Primary == "" {
				r.Primary = c.AI.Primary
			}
			if r.Fallback == "" {
				r.Fallback = c.AI.Fallback
			}
			return r
		}
	}
	return RepoAI{Primary: c.AI.Primary, Fallback: c.AI.Fallback}
}

func (c *Config) applyDefaults() {
	if c.Server.Port == 0 {
		c.Server.Port = 7842
	}
	if c.GitHub.PollInterval == "" {
		c.GitHub.PollInterval = "5m"
	}
	if c.Retention.MaxDays == 0 {
		c.Retention.MaxDays = 90
	}
}

// Validate checks that required fields are present and values are valid.
func (c *Config) Validate() error {
	if len(c.GitHub.Repositories) == 0 {
		return fmt.Errorf("config: at least one repository is required")
	}
	if c.AI.Primary == "" {
		return fmt.Errorf("config: ai.primary is required")
	}
	if c.GitHub.PollInterval != "" && !validIntervals[c.GitHub.PollInterval] {
		return fmt.Errorf("config: invalid poll_interval %q (valid: 1m, 5m, 30m, 1h)", c.GitHub.PollInterval)
	}
	return nil
}

// Load reads the TOML config file, applies defaults, and validates.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// DefaultPath returns ~/.config/auto-pr/config.toml
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return home + "/.config/auto-pr/config.toml"
}
