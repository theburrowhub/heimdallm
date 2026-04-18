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

// githubOrgPattern matches the GitHub org/user slug format: alphanumeric plus
// internal hyphens, 1–39 characters, not starting or ending with a hyphen.
// Validating this defensively prevents injection into the Search API query
// (e.g. a value like "evil-org archived:false org:other" being interpolated
// verbatim into the `q=` parameter).
var githubOrgPattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

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

	// IssueTracking turns the issue-tracking pipeline (fase-2) on and off and
	// governs how issues are filtered and classified. The pipeline itself
	// lives in downstream issues (#25 onward); this struct is the
	// configuration surface only.
	IssueTracking IssueTrackingConfig `toml:"issue_tracking"`
}

// IssueMode is the processing mode assigned to an issue after label
// classification. Used by the pipeline (#26/#27) to pick review_only vs.
// auto_implement vs. skip. Exported so downstream packages can reuse it.
type IssueMode string

const (
	IssueModeIgnore     IssueMode = "ignore"
	IssueModeDevelop    IssueMode = "develop"
	IssueModeReviewOnly IssueMode = "review_only"
)

// FilterMode names how the org / assignee / label filters are combined.
// Keeping it as a named type (mirrors IssueMode) lets validation surface type
// mismatches at compile time rather than as a runtime string compare.
type FilterMode string

const (
	FilterModeExclusive FilterMode = "exclusive" // AND
	FilterModeInclusive FilterMode = "inclusive" // OR
)

// IssueTrackingConfig is the `[github.issue_tracking]` section.
//
// Classification precedence (applied in Classify):
//
//	skip_labels  >  review_only_labels  >  develop_labels  >  default_action
//
// An issue that carries a label present in multiple lists resolves to the
// highest-precedence match. This is a fail-safe ordering: when in doubt,
// review before developing, and skip before either.
type IssueTrackingConfig struct {
	Enabled bool `toml:"enabled"`

	// FilterMode decides how the org / assignee / label dimensions are
	// combined ("exclusive" = AND, "inclusive" = OR). Applied by the
	// pipeline; not consulted by Classify itself.
	FilterMode FilterMode `toml:"filter_mode"`

	// Organizations limits processing to issues belonging to these orgs.
	// Empty = no org filter.
	Organizations []string `toml:"organizations"`

	// Assignees limits processing to issues assigned to these GitHub users.
	// Empty = no assignee filter.
	Assignees []string `toml:"assignees"`

	// DevelopLabels are labels that mark an issue as "please implement".
	DevelopLabels []string `toml:"develop_labels"`

	// ReviewOnlyLabels are labels that mark an issue as "please analyse and
	// comment only". Takes precedence over DevelopLabels when both are
	// present on the same issue — fail-safe default.
	ReviewOnlyLabels []string `toml:"review_only_labels"`

	// SkipLabels are labels that opt an issue out of processing entirely.
	// Highest precedence.
	SkipLabels []string `toml:"skip_labels"`

	// DefaultAction is applied when an issue carries no label from any of
	// the three lists above. Must be "ignore" or "review_only".
	DefaultAction string `toml:"default_action"`
}

// Classify returns the processing mode for an issue given its labels.
// Matching is case-insensitive to match the way GitHub displays labels; the
// underlying labels API is case-preserving but the UI is not, so users
// routinely mix "Bug" and "bug" in practice.
func (c IssueTrackingConfig) Classify(labels []string) IssueMode {
	set := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		set[strings.ToLower(strings.TrimSpace(l))] = struct{}{}
	}
	if labelSetIntersects(set, c.SkipLabels) {
		return IssueModeIgnore
	}
	if labelSetIntersects(set, c.ReviewOnlyLabels) {
		return IssueModeReviewOnly
	}
	if labelSetIntersects(set, c.DevelopLabels) {
		return IssueModeDevelop
	}
	switch strings.ToLower(c.DefaultAction) {
	case "review_only":
		return IssueModeReviewOnly
	default:
		return IssueModeIgnore
	}
}

func labelSetIntersects(set map[string]struct{}, list []string) bool {
	for _, l := range list {
		if _, ok := set[strings.ToLower(strings.TrimSpace(l))]; ok {
			return true
		}
	}
	return false
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
	PRMetadata PRMetadataConfig            `toml:"pr_metadata"` // global PR creation defaults
}

type RepoAI struct {
	Primary    string `toml:"primary"`
	// Prompt is the ID of a review prompt profile to use for this repo.
	// Overrides agent-level and global default prompts.
	Prompt      string `toml:"prompt"`
	// IssuePrompt is the ID of an agent profile for issue triage.
	// Overrides agent-level and global default issue prompts.
	IssuePrompt string `toml:"issue_prompt"`
	// ImplementPrompt is the ID of an agent profile whose ImplementPrompt /
	// ImplementInstructions fields drive the auto_implement code-generation
	// prompt for this repo. Overrides agent-level and global default.
	ImplementPrompt string `toml:"implement_prompt"`
	Fallback    string `toml:"fallback"`
	ReviewMode  string `toml:"review_mode"` // "" = inherit global
	LocalDir    string `toml:"local_dir"`   // local repo path for full-repo analysis

	// PR creation metadata (applied by auto_implement after CreatePR).
	PRReviewers []string `toml:"pr_reviewers"` // GitHub logins to request review from
	PRAssignee  string   `toml:"pr_assignee"`  // GitHub login to assign the PR to
	PRLabels    []string `toml:"pr_labels"`    // labels to add to the PR
	PRDraft     bool     `toml:"pr_draft"`     // create as draft PR
}

// PRMetadataConfig holds global defaults for PR creation metadata,
// used as fallback when per-repo config is not set.
// Only Reviewers and Labels have global defaults — Assignee and Draft are
// per-repo only because assignee is team-specific and draft mode depends
// on the repo's workflow (some repos auto-merge non-drafts).
type PRMetadataConfig struct {
	Reviewers []string `toml:"reviewers"`
	Labels    []string `toml:"labels"`
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
			// Fallback PR metadata to global defaults when repo-level is empty
			if len(r.PRReviewers) == 0 {
				r.PRReviewers = c.AI.PRMetadata.Reviewers
			}
			if len(r.PRLabels) == 0 {
				r.PRLabels = c.AI.PRMetadata.Labels
			}
			return r
		}
	}
	return RepoAI{
		Primary: c.AI.Primary, Fallback: c.AI.Fallback, ReviewMode: c.AI.ReviewMode,
		PRReviewers: c.AI.PRMetadata.Reviewers, PRLabels: c.AI.PRMetadata.Labels,
	}
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
	if c.GitHub.IssueTracking.FilterMode == "" {
		c.GitHub.IssueTracking.FilterMode = FilterModeExclusive
	}
	if c.GitHub.IssueTracking.DefaultAction == "" {
		c.GitHub.IssueTracking.DefaultAction = string(IssueModeIgnore)
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
	c.applyIssueTrackingEnv()
	c.applyPRMetadataEnv()
}

// applyPRMetadataEnv maps HEIMDALLM_PR_* env vars into PRMetadataConfig globals.
func (c *Config) applyPRMetadataEnv() {
	if list, ok := csvEnv("HEIMDALLM_PR_REVIEWERS"); ok {
		c.AI.PRMetadata.Reviewers = list
	}
	if list, ok := csvEnv("HEIMDALLM_PR_LABELS"); ok {
		c.AI.PRMetadata.Labels = list
	}
}

// applyIssueTrackingEnv maps HEIMDALLM_ISSUE_* env vars into IssueTrackingConfig.
// CSV lists only overwrite the TOML value when at least one non-blank entry is
// present, matching the behaviour of HEIMDALLM_REPOSITORIES.
func (c *Config) applyIssueTrackingEnv() {
	if v := os.Getenv("HEIMDALLM_ISSUE_TRACKING_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.GitHub.IssueTracking.Enabled = b
		}
	}
	if v := os.Getenv("HEIMDALLM_ISSUE_FILTER_MODE"); v != "" {
		c.GitHub.IssueTracking.FilterMode = FilterMode(v)
	}
	if v := os.Getenv("HEIMDALLM_ISSUE_DEFAULT_ACTION"); v != "" {
		c.GitHub.IssueTracking.DefaultAction = v
	}
	if list, ok := csvEnv("HEIMDALLM_ISSUE_ORGANIZATIONS"); ok {
		c.GitHub.IssueTracking.Organizations = list
	}
	if list, ok := csvEnv("HEIMDALLM_ISSUE_ASSIGNEES"); ok {
		c.GitHub.IssueTracking.Assignees = list
	}
	if list, ok := csvEnv("HEIMDALLM_ISSUE_DEVELOP_LABELS"); ok {
		c.GitHub.IssueTracking.DevelopLabels = list
	}
	if list, ok := csvEnv("HEIMDALLM_ISSUE_REVIEW_ONLY_LABELS"); ok {
		c.GitHub.IssueTracking.ReviewOnlyLabels = list
	}
	if list, ok := csvEnv("HEIMDALLM_ISSUE_SKIP_LABELS"); ok {
		c.GitHub.IssueTracking.SkipLabels = list
	}
}

// csvEnv parses a comma-separated env var into a trimmed, non-empty list.
// Returns ok=false when the env var is unset OR contains only blanks, so the
// caller can preserve any existing TOML value (same contract as the
// HEIMDALLM_REPOSITORIES override).
func csvEnv(name string) ([]string, bool) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, false
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
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
	if err := c.validateIssueTracking(); err != nil {
		return err
	}
	return nil
}

// validateIssueTracking enforces the small set of invariants the pipeline
// relies on: filter_mode and default_action must be from a known set. Labels
// themselves are free-form strings — intentionally — because GitHub allows
// almost anything in a label and we do not want to reject legitimate values.
// Silent fallbacks in applyDefaults mean the user almost never sees these
// errors; they exist so an explicit typo like filter_mode = "excluive" fails
// fast instead of defaulting silently.
func (c *Config) validateIssueTracking() error {
	it := c.GitHub.IssueTracking
	if !it.Enabled {
		return nil
	}
	switch it.FilterMode {
	case FilterModeExclusive, FilterModeInclusive:
	default:
		return fmt.Errorf("config: github.issue_tracking.filter_mode %q is invalid (must be %q or %q)", it.FilterMode, FilterModeExclusive, FilterModeInclusive)
	}
	switch IssueMode(it.DefaultAction) {
	case IssueModeIgnore, IssueModeReviewOnly:
	default:
		return fmt.Errorf("config: github.issue_tracking.default_action %q is invalid (must be %q or %q)", it.DefaultAction, IssueModeIgnore, IssueModeReviewOnly)
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
	for _, org := range c.GitHub.DiscoveryOrgs {
		if !githubOrgPattern.MatchString(org) {
			return fmt.Errorf("config: github.discovery_orgs entry %q is invalid (must match GitHub org/user slug: 1–39 alphanumerics plus internal hyphens)", org)
		}
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
