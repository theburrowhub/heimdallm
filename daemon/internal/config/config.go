package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/heimdallm/daemon/internal/executor"
)

var validIntervals = map[string]bool{
	"1m": true, "5m": true, "30m": true, "1h": true,
}

// githubTopicPattern enforces GitHub's topic rules: lowercase letters, digits
// and hyphens, starting with a letter or digit, up to 50 characters total.
// See https://docs.github.com/repositories/classifying-your-repository-with-topics
var githubTopicPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,49}$`)

type Config struct {
	Server    ServerConfig    `toml:"server"`
	GitHub    GitHubConfig    `toml:"github"`
	AI        AIConfig        `toml:"ai"`
	Retention RetentionConfig `toml:"retention"`
}

type ServerConfig struct {
	Port     int    `toml:"port"`
	BindAddr string `toml:"bind_addr"`
}

type GitHubConfig struct {
	PollInterval string   `toml:"poll_interval"`
	Repositories []string `toml:"repositories"`
	// NonMonitored tracks repos the user knows about but has disabled auto-review for.
	// The daemon never polls these; they are stored here only so the Flutter UI can
	// remember and display them after a restart.
	NonMonitored []string `toml:"non_monitored"`

	// DiscoveryTopic, when set, enables automatic repository discovery based on
	// a GitHub topic tag (e.g. "heimdallm-review"). Discovered repos are merged
	// with Repositories at poll time. Empty = discovery disabled.
	DiscoveryTopic string `toml:"discovery_topic"`
	// DiscoveryOrgs limits topic-based discovery to specific organisations.
	// Required when DiscoveryTopic is set (prevents scanning all of GitHub).
	DiscoveryOrgs []string `toml:"discovery_orgs"`
	// DiscoveryInterval controls how often the discovery query is refreshed.
	// Independent from PollInterval because the Search API has a stricter
	// rate limit (30 req/min authenticated). Defaults to "15m" when discovery
	// is enabled. Accepts any Go time.ParseDuration value.
	DiscoveryInterval string `toml:"discovery_interval"`
}

// CLIAgentConfig holds per-CLI execution settings (model, flags, prompt override).
// Stored under [ai.agents.<cli-name>] in config.toml.
type CLIAgentConfig struct {
	Model        string `toml:"model"`          // e.g. "claude-opus-4-6"
	MaxTurns     int    `toml:"max_turns"`       // claude: --max-turns (0 = not set)
	ApprovalMode string `toml:"approval_mode"`  // codex: --approval-mode
	ExtraFlags   string `toml:"extra_flags"`     // free-form additional CLI flags
	PromptID     string `toml:"prompt"`          // agent-level prompt override

	// Claude-specific flags
	Effort               string `toml:"effort"`                  // low|medium|high|max
	PermissionMode       string `toml:"permission_mode"`         // default|auto|acceptEdits|dontAsk (bypassPermissions is explicitly forbidden)
	Bare                 bool   `toml:"bare"`                    // --bare
	DangerouslySkipPerms bool   `toml:"dangerously_skip_perms"` // --dangerously-skip-permissions (cannot be set via HTTP API, see M-5)
	NoSessionPersistence bool   `toml:"no_session_persistence"` // --no-session-persistence
}

type AIConfig struct {
	Primary    string                      `toml:"primary"`
	Fallback   string                      `toml:"fallback"`
	ReviewMode string                      `toml:"review_mode"` // "single" | "multi"
	Agents     map[string]CLIAgentConfig   `toml:"agents"`      // keyed by CLI name
	Repos      map[string]RepoAI           `toml:"repos"`
}

type RepoAI struct {
	Primary    string `toml:"primary"`
	// Prompt is the ID of a review prompt profile to use for this repo.
	// Overrides agent-level and global default prompts.
	Prompt     string `toml:"prompt"`
	Fallback   string `toml:"fallback"`
	ReviewMode string `toml:"review_mode"` // "" = inherit global
	LocalDir   string `toml:"local_dir"`   // local repo path for full-repo analysis
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
			if r.ReviewMode == "" {
				r.ReviewMode = c.AI.ReviewMode
			}
			return r
		}
	}
	return RepoAI{Primary: c.AI.Primary, Fallback: c.AI.Fallback, ReviewMode: c.AI.ReviewMode}
}

// AgentConfigFor returns the CLIAgentConfig for a given CLI name, or an empty struct.
func (c *Config) AgentConfigFor(cli string) CLIAgentConfig {
	if c.AI.Agents != nil {
		if a, ok := c.AI.Agents[cli]; ok {
			return a
		}
	}
	return CLIAgentConfig{}
}

func (c *Config) applyDefaults() {
	if c.Server.Port == 0 {
		c.Server.Port = 7842
	}
	if c.Server.BindAddr == "" {
		c.Server.BindAddr = "127.0.0.1"
	}
	if c.GitHub.PollInterval == "" {
		c.GitHub.PollInterval = "5m"
	}
	if c.GitHub.DiscoveryTopic != "" && c.GitHub.DiscoveryInterval == "" {
		c.GitHub.DiscoveryInterval = "15m"
	}
	if c.Retention.MaxDays == 0 {
		c.Retention.MaxDays = 90
	}
	if c.AI.ReviewMode == "" {
		c.AI.ReviewMode = "single"
	}
}

// applyEnvOverrides applies HEIMDALLM_* environment variable overrides.
// Environment variables take precedence over values loaded from the TOML file.
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("HEIMDALLM_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Server.Port = p
		}
	}
	if v := os.Getenv("HEIMDALLM_BIND_ADDR"); v != "" {
		c.Server.BindAddr = v
	}
	if v := os.Getenv("HEIMDALLM_POLL_INTERVAL"); v != "" {
		c.GitHub.PollInterval = v
	}
	if v := os.Getenv("HEIMDALLM_REPOSITORIES"); v != "" {
		repos := strings.Split(v, ",")
		cleaned := make([]string, 0, len(repos))
		for _, r := range repos {
			if s := strings.TrimSpace(r); s != "" {
				cleaned = append(cleaned, s)
			}
		}
		if len(cleaned) > 0 {
			c.GitHub.Repositories = cleaned
		}
	}
	if v := os.Getenv("HEIMDALLM_AI_PRIMARY"); v != "" {
		c.AI.Primary = v
	}
	if v := os.Getenv("HEIMDALLM_AI_FALLBACK"); v != "" {
		c.AI.Fallback = v
	}
	if v := os.Getenv("HEIMDALLM_REVIEW_MODE"); v != "" {
		c.AI.ReviewMode = v
	}
	if v := os.Getenv("HEIMDALLM_RETENTION_DAYS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil {
			c.Retention.MaxDays = d
		}
	}
	if v := os.Getenv("HEIMDALLM_DISCOVERY_TOPIC"); v != "" {
		c.GitHub.DiscoveryTopic = v
	}
	if v := os.Getenv("HEIMDALLM_DISCOVERY_ORGS"); v != "" {
		orgs := strings.Split(v, ",")
		cleaned := make([]string, 0, len(orgs))
		for _, o := range orgs {
			if s := strings.TrimSpace(o); s != "" {
				cleaned = append(cleaned, s)
			}
		}
		if len(cleaned) > 0 {
			c.GitHub.DiscoveryOrgs = cleaned
		}
	}
	if v := os.Getenv("HEIMDALLM_DISCOVERY_INTERVAL"); v != "" {
		c.GitHub.DiscoveryInterval = v
	}
}

// Validate checks that required fields are present and values are valid.
func (c *Config) Validate() error {
	if c.AI.Primary == "" {
		return fmt.Errorf("config: ai.primary is required")
	}
	if c.GitHub.PollInterval != "" && !validIntervals[c.GitHub.PollInterval] {
		return fmt.Errorf("config: invalid poll_interval %q (valid: 1m, 5m, 30m, 1h)", c.GitHub.PollInterval)
	}
	// Validate per-CLI agent configs: permission_mode and approval_mode must be in their allowlists.
	for name, a := range c.AI.Agents {
		if err := executor.ValidatePermissionMode(a.PermissionMode); err != nil {
			return fmt.Errorf("config: agents[%s].permission_mode: %w", name, err)
		}
		if err := executor.ValidateApprovalMode(a.ApprovalMode); err != nil {
			return fmt.Errorf("config: agents[%s].approval_mode: %w", name, err)
		}
	}
	if err := c.validateDiscovery(); err != nil {
		return err
	}
	return nil
}

// validateDiscovery enforces the rules for topic-based repository discovery.
// Topic must follow GitHub's topic format, at least one org is required when
// discovery is enabled (to bound the Search API scope), and the interval must
// be parseable as a positive duration.
func (c *Config) validateDiscovery() error {
	if c.GitHub.DiscoveryTopic == "" {
		return nil
	}
	if !githubTopicPattern.MatchString(c.GitHub.DiscoveryTopic) {
		return fmt.Errorf("config: github.discovery_topic %q is invalid (must match GitHub topic format: lowercase letters, digits and hyphens, up to 50 chars)", c.GitHub.DiscoveryTopic)
	}
	if len(c.GitHub.DiscoveryOrgs) == 0 {
		return fmt.Errorf("config: github.discovery_orgs must list at least one organisation when discovery_topic is set")
	}
	if c.GitHub.DiscoveryInterval != "" {
		d, err := time.ParseDuration(c.GitHub.DiscoveryInterval)
		if err != nil {
			return fmt.Errorf("config: github.discovery_interval %q is invalid: %w", c.GitHub.DiscoveryInterval, err)
		}
		if d <= 0 {
			return fmt.Errorf("config: github.discovery_interval %q must be positive", c.GitHub.DiscoveryInterval)
		}
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
	cfg.applyEnvOverrides()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func writeConfigTOML(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create config file: %w", err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}

// LoadOrCreate loads config from path, or creates a minimal config from
// environment variables if the file does not exist. This is the preferred
// entry point for Docker / headless deployments.
func LoadOrCreate(path string) (*Config, error) {
	if _, err := os.Stat(path); err == nil {
		return Load(path)
	}
	// No config file — build from env vars.
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.applyEnvOverrides()
	if cfg.AI.Primary == "" {
		return nil, fmt.Errorf("no config file and HEIMDALLM_AI_PRIMARY not set")
	}
	if err := writeConfigTOML(path, cfg); err != nil {
		slog.Warn("config: could not persist generated config", "path", path, "err", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}


// DefaultPath returns ~/.config/heimdallm/config.toml
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return home + "/.config/heimdallm/config.toml"
}
