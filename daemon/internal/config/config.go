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
	Server         ServerConfig         `toml:"server"`
	GitHub         GitHubConfig         `toml:"github"`
	AI             AIConfig             `toml:"ai"`
	Retention      RetentionConfig      `toml:"retention"`
	ActivityLog    ActivityLogConfig    `toml:"activity_log"`
	CircuitBreaker CircuitBreakerConfig `toml:"circuit_breaker"`
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

	// AutoEnablePROnDiscovery controls the initial prEnabled value for repos
	// auto-added from the poll cycle's review-requested results. nil means
	// "use default". Default is true to preserve pre-feature behaviour.
	AutoEnablePROnDiscovery *bool `toml:"auto_enable_pr_review_on_discovery"`

	// WatchInterval controls Tier 3 per-item polling — how often active
	// items (PRs/issues with recent activity) are re-checked for state
	// changes (label updates, new comments, merge/close). Defaults to "1m".
	WatchInterval string `toml:"watch_interval"`

	// LocalDirBase is a list of base directories for auto-resolving local_dir
	// per repo. ResolveLocalDir checks each path in order, looking for
	// {base}/{repo-name}, before falling back to /home/heimdallm/repos/{repo-name}.
	// This supports multiple workspace groups (e.g. ai-platform in one dir,
	// another team's repos in another). Put more specific paths first.
	LocalDirBase []string `toml:"local_dir_base"`

	// IssueTracking turns the issue-tracking pipeline (fase-2) on and off and
	// governs how issues are filtered and classified. The pipeline itself
	// lives in downstream issues (#25 onward); this struct is the
	// configuration surface only.
	IssueTracking IssueTrackingConfig `toml:"issue_tracking"`

	// ReviewGuards configures the caller-side skip rules applied before a PR
	// enters the review pipeline (skip drafts, skip self-authored PRs).
	ReviewGuards ReviewGuardsConfig `toml:"review_guards"`
}

// IssueMode is the processing mode assigned to an issue after label
// classification. Used by the pipeline (#26/#27) to pick review_only vs.
// auto_implement vs. skip. Exported so downstream packages can reuse it.
type IssueMode string

const (
	IssueModeIgnore     IssueMode = "ignore"
	IssueModeBlocked    IssueMode = "blocked"
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
//	skip_labels  >  blocked_labels  >  develop_labels  >  review_only_labels  >  default_action
//
// develop_labels intentionally takes precedence over review_only_labels: when
// an issue carries both a "please implement" label and a "please review only"
// label, the operator intent is auto_implement (the stronger action). This
// prevents misclassification as IT (review_only) when labels overlap, which
// would otherwise cause an infinite retry loop if the daemon later tried to
// auto-implement an issue that was already classified as IT. See issue #223.
type IssueTrackingConfig struct {
	Enabled bool `toml:"enabled" json:"enabled"`

	// FilterMode decides how the org / assignee / label dimensions are
	// combined ("exclusive" = AND, "inclusive" = OR). Applied by the
	// pipeline; not consulted by Classify itself.
	FilterMode FilterMode `toml:"filter_mode" json:"filter_mode"`

	// Organizations limits processing to issues belonging to these orgs.
	// Empty = no org filter.
	Organizations []string `toml:"organizations" json:"organizations"`

	// Assignees limits processing to issues assigned to these GitHub users.
	// Empty = no assignee filter.
	Assignees []string `toml:"assignees" json:"assignees"`

	// DevelopLabels are labels that mark an issue as "please implement".
	DevelopLabels []string `toml:"develop_labels" json:"develop_labels"`

	// ReviewOnlyLabels are labels that mark an issue as "please analyse and
	// comment only". DevelopLabels take precedence over ReviewOnlyLabels when
	// both are present on the same issue — the operator explicitly tagged it
	// for implementation, which is the stronger intent. See issue #223.
	ReviewOnlyLabels []string `toml:"review_only_labels" json:"review_only_labels"`

	// SkipLabels are labels that opt an issue out of processing entirely.
	// Highest precedence.
	SkipLabels []string `toml:"skip_labels" json:"skip_labels"`

	// BlockedLabels mark issues whose dependencies (declared in the body
	// under a `## Depends on` section) are still open. An issue carrying
	// any of these labels is classified as IssueModeBlocked and skipped by
	// the fetcher; a separate promotion pass flips the label to the
	// configured PromoteToLabel once all dependencies close. Precedence
	// sits between SkipLabels and ReviewOnlyLabels.
	BlockedLabels []string `toml:"blocked_labels" json:"blocked_labels"`

	// PromoteToLabel is the label added when an issue's dependencies all
	// close. If empty, the first entry in DevelopLabels is used. Must
	// resolve to a non-empty value when BlockedLabels is set — otherwise
	// promotion has no target and blocked issues would stick forever.
	PromoteToLabel string `toml:"promote_to_label" json:"promote_to_label"`

	// DefaultAction is applied when an issue carries no label from any of
	// the three lists above. Must be "ignore" or "review_only".
	DefaultAction string `toml:"default_action" json:"default_action"`
}

// ReviewGuardsConfig configures the caller-side skip rules applied before a PR
// enters the review pipeline. Pointer-to-bool lets "unset" apply the default;
// explicit false disables a guard.
type ReviewGuardsConfig struct {
	SkipDrafts     *bool `toml:"skip_drafts"`
	SkipSelfAuthor *bool `toml:"skip_self_author"`
}

// ResolvedReviewGuards is a shadow of pipeline.GateConfig that exists to break
// an import cycle: config cannot import pipeline because pipeline imports
// github, and github imports config (for IssueMode). This type has identical
// field names, types, and order to pipeline.GateConfig — callers convert via
// Go's same-shape struct cast:
//
//	resolved := cfg.ReviewGuards(botLogin)
//	gc := pipeline.GateConfig(resolved)
//
// If you add a field to pipeline.GateConfig, add it here in the same position
// and type; the drift-prevention test in config_guards_drift_test.go will fail
// if the two types diverge.
type ResolvedReviewGuards struct {
	SkipDrafts     bool
	SkipSelfAuthor bool
	BotLogin       string
}

// ResolvePromoteToLabel returns the label that should replace the blocked
// label(s) when all of an issue's dependencies close. Explicit
// PromoteToLabel wins; otherwise the first configured DevelopLabel is the
// natural "ready" target (mirrors the user's existing auto_implement
// convention). Returns "" when neither is configured — Validate refuses
// this combination when BlockedLabels is set.
func (c IssueTrackingConfig) ResolvePromoteToLabel() string {
	if c.PromoteToLabel != "" {
		return c.PromoteToLabel
	}
	if len(c.DevelopLabels) > 0 {
		return c.DevelopLabels[0]
	}
	return ""
}

// Classify returns the processing mode for an issue given its labels.
// Matching is case-insensitive to match the way GitHub displays labels; the
// underlying labels API is case-preserving but the UI is not, so users
// routinely mix "Bug" and "bug" in practice.
//
// Precedence: skip > blocked > develop > review_only > default_action.
// develop beats review_only so that an issue tagged with both a DEV label and
// an IT label is always auto-implemented, never silently downgraded to
// review_only. This prevents the infinite retry loop described in issue #223.
func (c IssueTrackingConfig) Classify(labels []string) IssueMode {
	set := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		set[strings.ToLower(strings.TrimSpace(l))] = struct{}{}
	}
	if labelSetIntersects(set, c.SkipLabels) {
		return IssueModeIgnore
	}
	if labelSetIntersects(set, c.BlockedLabels) {
		return IssueModeBlocked
	}
	// develop takes precedence over review_only: when both are present the
	// operator wants auto_implement (the stronger action). See issue #223.
	if labelSetIntersects(set, c.DevelopLabels) {
		return IssueModeDevelop
	}
	if labelSetIntersects(set, c.ReviewOnlyLabels) {
		return IssueModeReviewOnly
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
	Model        string `toml:"model"`         // e.g. "claude-opus-4-6"
	MaxTurns     int    `toml:"max_turns"`     // claude: --max-turns (0 = not set)
	ApprovalMode string `toml:"approval_mode"` // codex: --approval-mode
	ExtraFlags   string `toml:"extra_flags"`   // free-form additional CLI flags
	PromptID     string `toml:"prompt"`        // agent-level prompt override

	// Claude-specific flags
	Effort               string `toml:"effort"`                 // low|medium|high|max
	PermissionMode       string `toml:"permission_mode"`        // default|auto|acceptEdits|dontAsk (bypassPermissions is explicitly forbidden)
	Bare                 bool   `toml:"bare"`                   // --bare
	DangerouslySkipPerms bool   `toml:"dangerously_skip_perms"` // --dangerously-skip-permissions (cannot be set via HTTP API, see M-5)
	NoSessionPersistence bool   `toml:"no_session_persistence"` // --no-session-persistence
	ExecutionTimeout     string `toml:"execution_timeout"`      // per-agent override, e.g. "20m"
}

type AIConfig struct {
	Primary          string                    `toml:"primary"`
	Fallback         string                    `toml:"fallback"`
	ReviewMode       string                    `toml:"review_mode"`       // "single" | "multi"
	ExecutionTimeout string                    `toml:"execution_timeout"` // e.g. "20m", "1h"
	Agents           map[string]CLIAgentConfig `toml:"agents"`            // keyed by CLI name
	Repos            map[string]RepoAI         `toml:"repos"`
	Orgs             map[string]OrgAI          `toml:"orgs"`        // per-org PR metadata overrides
	PRMetadata       PRMetadataConfig          `toml:"pr_metadata"` // global PR creation defaults

	// Top-level PR metadata fields — flat alternatives to [ai.pr_metadata].
	// Populated from HEIMDALLM_PR_* env vars or TOML keys directly under [ai].
	PRReviewers []string `toml:"pr_reviewers"`
	PRLabels    []string `toml:"pr_labels"`
	PRAssignee  string   `toml:"pr_assignee"`
	PRDraft     *bool    `toml:"pr_draft,omitempty"`

	// IssuePrompt is the global default agent profile ID for issue triage.
	// Per-repo overrides in [ai.repos.<name>] take precedence.
	IssuePrompt string `toml:"issue_prompt"`
	// ImplementPrompt is the global default agent profile ID for auto-implement.
	// Per-repo overrides in [ai.repos.<name>] take precedence.
	ImplementPrompt string `toml:"implement_prompt"`

	// GeneratePRDescription enables LLM-generated PR titles and descriptions
	// for auto_implement PRs. When true, after the implementation commit,
	// a second LLM call generates a rich PR description from the diff.
	// Default: false (backwards compat).
	GeneratePRDescription bool `toml:"generate_pr_description"`
}

type RepoAI struct {
	Primary string `toml:"primary"`
	// Prompt is the ID of a review prompt profile to use for this repo.
	// Overrides agent-level and global default prompts.
	Prompt string `toml:"prompt"`
	// IssuePrompt is the ID of an agent profile for issue triage.
	// Overrides agent-level and global default issue prompts.
	IssuePrompt string `toml:"issue_prompt"`
	// ImplementPrompt is the ID of an agent profile whose ImplementPrompt /
	// ImplementInstructions fields drive the auto_implement code-generation
	// prompt for this repo. Overrides agent-level and global default.
	ImplementPrompt string `toml:"implement_prompt"`
	Fallback        string `toml:"fallback"`
	ReviewMode      string `toml:"review_mode"` // "" = inherit global
	LocalDir        string `toml:"local_dir"`   // local repo path for full-repo analysis

	// PR creation metadata (applied by auto_implement after CreatePR).
	PRReviewers []string `toml:"pr_reviewers"`       // GitHub logins to request review from
	PRAssignee  string   `toml:"pr_assignee"`        // GitHub login to assign the PR to
	PRLabels    []string `toml:"pr_labels"`          // labels to add to the PR
	PRDraft     *bool    `toml:"pr_draft,omitempty"` // create as draft PR

	// GeneratePRDescription overrides the global ai.generate_pr_description
	// for this repo. nil = inherit from global.
	GeneratePRDescription *bool `toml:"generate_pr_description,omitempty"`

	// Per-repo issue tracking override. When set, non-zero fields replace
	// the global [github.issue_tracking] values for this repo only.
	IssueTracking *IssueTrackingConfig `toml:"issue_tracking,omitempty" json:"issue_tracking,omitempty"`
}

// PRMetadataConfig holds global defaults for PR creation metadata,
// used as fallback when per-repo config is not set.
type PRMetadataConfig struct {
	Reviewers []string `toml:"reviewers"`
	Labels    []string `toml:"labels"`
	Assignee  string   `toml:"pr_assignee"`
	Draft     *bool    `toml:"pr_draft,omitempty"`
}

// OrgAI holds per-organisation PR metadata overrides, applied to all repos
// in the org unless overridden per-repo. Keyed by GitHub org slug under
// [ai.orgs."org-name"].
type OrgAI struct {
	PRReviewers []string `toml:"pr_reviewers"`
	PRAssignee  string   `toml:"pr_assignee"`
	PRLabels    []string `toml:"pr_labels"`
	PRDraft     *bool    `toml:"pr_draft,omitempty"`
}

type RetentionConfig struct {
	MaxDays int `toml:"max_days"`
}

// ActivityLogConfig controls the daily activity log (#113). When enabled,
// the daemon records a row per significant action (review, triage,
// implement, promote, error) into the activity_log table.
//
// Enabled is a pointer so we can tell "absent from TOML" (nil → default
// true, opt-out behaviour) from "explicitly disabled" (&false). Post
// applyDefaults it is always non-nil.
type ActivityLogConfig struct {
	Enabled       *bool `toml:"enabled"`
	RetentionDays *int  `toml:"retention_days"`
}

// DefaultReposMountPath is the conventional location inside the daemon's
// container where an operator's repos root is bind-mounted (e.g. via
// HEIMDALLM_LOCAL_DIR_BASE=/Users/you/projects → /home/heimdallm/repos).
// The path MUST live under /home/heimdallm (or /tmp) because the
// executor's ValidateWorkDir rejects any workdir outside the daemon
// user's home — shipping the mount at /repos at the filesystem root
// was a latent bug (silently rejected at review time). Exposed as a
// package variable so tests can redirect auto-detection at a temp dir
// without also having to mock the filesystem. On desktop installs
// nothing is mounted at this path, so detection simply returns false
// for every repo and we fall through to the configured value (or empty).
var DefaultReposMountPath = "/home/heimdallm/repos"

// ShortRepoName returns the sub-repo name of an "org/repo" string, or
// the input unchanged when there is no slash. Used by auto-detection
// to map a monitored repo like "freepik-company/ai-api-specs" to the
// conventional mount sub-dir "/home/heimdallm/repos/ai-api-specs".
func ShortRepoName(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// ResolveLocalDir picks the effective working directory the AI agent
// should run in for a given repo, using this precedence:
//
//  1. The explicit `local_dir` from config (the `configured` argument).
//  2. Each path in `localDirBases` checked in order — first match wins.
//     Supports multiple workspace groups (e.g. ai-platform repos in one
//     dir, another team in another) without per-repo local_dir entries.
//  3. `DefaultReposMountPath/<short-name>` when that directory exists —
//     lets an operator drop a single HEIMDALLM_LOCAL_DIR_BASE into
//     docker/.env and have every monitored repo picked up without also
//     touching the per-repo override in the UI.
//  4. Empty string — the agent runs in its default CWD (diff-only mode).
//
// Calls `os.Stat` on the candidate path, so callers should invoke it
// outside any config-mutex critical section. The result is not cached;
// re-invocation picks up newly-mounted repos on the next review cycle.
func ResolveLocalDir(configured, repo string, localDirBases []string) string {
	if configured != "" {
		return configured
	}
	short := ShortRepoName(repo)
	if short == "" {
		return ""
	}
	// 1. Check each local_dir_base in order (first match wins)
	for _, base := range localDirBases {
		if base == "" {
			continue
		}
		candidate := filepath.Join(base, short)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	// 2. Fallback to default mount path (/home/heimdallm/repos/{short-name})
	if DefaultReposMountPath != "" {
		candidate := filepath.Join(DefaultReposMountPath, short)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

// repoOrg extracts the organisation slug from an "org/repo" string.
// Returns "" when the input has no slash.
func repoOrg(repo string) string {
	if i := strings.Index(repo, "/"); i > 0 {
		return repo[:i]
	}
	return ""
}

// resolvedPRMetadata returns the effective global PR metadata by merging
// flat [ai] fields on top of [ai.pr_metadata]. Flat fields win when set,
// matching the contract that HEIMDALLM_PR_* env vars populate the flat
// fields and should override the nested section.
func (c *Config) ResolvedPRMetadata() (reviewers, labels []string, assignee string, draft *bool) {
	reviewers = c.AI.PRMetadata.Reviewers
	labels = c.AI.PRMetadata.Labels
	assignee = c.AI.PRMetadata.Assignee
	if c.AI.PRMetadata.Draft != nil {
		draft = c.AI.PRMetadata.Draft
	}
	if len(c.AI.PRReviewers) > 0 {
		reviewers = c.AI.PRReviewers
	}
	if len(c.AI.PRLabels) > 0 {
		labels = c.AI.PRLabels
	}
	if c.AI.PRAssignee != "" {
		assignee = c.AI.PRAssignee
	}
	if c.AI.PRDraft != nil {
		draft = c.AI.PRDraft
	}
	return
}

// AIForRepo returns the AI config for a specific repo, falling back through
// three levels: per-repo > per-org > global defaults. Each PR metadata
// field resolves independently.
func (c *Config) AIForRepo(repo string) RepoAI {
	gReviewers, gLabels, gAssignee, gDraft := c.ResolvedPRMetadata()

	// Org-level layer: start from global, overlay org-level fields.
	orgReviewers, orgLabels, orgAssignee, orgDraft := gReviewers, gLabels, gAssignee, gDraft
	if org := repoOrg(repo); org != "" && c.AI.Orgs != nil {
		if o, ok := c.AI.Orgs[org]; ok {
			if len(o.PRReviewers) > 0 {
				orgReviewers = o.PRReviewers
			}
			if len(o.PRLabels) > 0 {
				orgLabels = o.PRLabels
			}
			if o.PRAssignee != "" {
				orgAssignee = o.PRAssignee
			}
			if o.PRDraft != nil {
				orgDraft = o.PRDraft
			}
		}
	}

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
			if len(r.PRReviewers) == 0 {
				r.PRReviewers = orgReviewers
			}
			if len(r.PRLabels) == 0 {
				r.PRLabels = orgLabels
			}
			if r.PRAssignee == "" {
				r.PRAssignee = orgAssignee
			}
			if r.PRDraft == nil {
				r.PRDraft = orgDraft
			}
			if r.GeneratePRDescription == nil {
				v := c.AI.GeneratePRDescription
				r.GeneratePRDescription = &v
			}
			return r
		}
	}
	gGenDesc := c.AI.GeneratePRDescription
	return RepoAI{
		Primary: c.AI.Primary, Fallback: c.AI.Fallback, ReviewMode: c.AI.ReviewMode,
		PRReviewers: orgReviewers, PRLabels: orgLabels, PRAssignee: orgAssignee, PRDraft: orgDraft,
		GeneratePRDescription: &gGenDesc,
	}
}

// IssueTrackingForRepo returns the issue tracking config for a specific repo,
// merging per-repo overrides (field-level) with the global config.
// Non-zero per-repo fields win; zero/nil fields inherit from global.
func (c *Config) IssueTrackingForRepo(repo string) IssueTrackingConfig {
	global := c.GitHub.IssueTracking
	if c.AI.Repos == nil {
		return global
	}
	r, ok := c.AI.Repos[repo]
	if !ok || r.IssueTracking == nil {
		return global
	}
	ov := r.IssueTracking
	merged := global
	// Enabled resolution: the per-repo override enables IT if Enabled is
	// explicitly true OR if labels are configured (implicit intent). This
	// prevents the common mistake of configuring labels but forgetting the
	// toggle.
	//
	// Limitation: a per-repo override cannot explicitly disable IT when the
	// global is on, because Enabled=false is indistinguishable from "not set"
	// (bool zero value). A *bool refactor would fix this if needed.
	if ov.Enabled || len(ov.DevelopLabels) > 0 || len(ov.ReviewOnlyLabels) > 0 {
		merged.Enabled = true
	}
	if len(ov.DevelopLabels) > 0 {
		merged.DevelopLabels = ov.DevelopLabels
	}
	if len(ov.ReviewOnlyLabels) > 0 {
		merged.ReviewOnlyLabels = ov.ReviewOnlyLabels
	}
	if len(ov.SkipLabels) > 0 {
		merged.SkipLabels = ov.SkipLabels
	}
	if ov.FilterMode != "" {
		merged.FilterMode = ov.FilterMode
	}
	if ov.DefaultAction != "" {
		merged.DefaultAction = ov.DefaultAction
	}
	if len(ov.Organizations) > 0 {
		merged.Organizations = ov.Organizations
	}
	if len(ov.Assignees) > 0 {
		merged.Assignees = ov.Assignees
	}
	return merged
}

// AutoEnablePRForDiscovery returns the effective boolean value.
func (c *GitHubConfig) AutoEnablePRForDiscovery() bool {
	if c.AutoEnablePROnDiscovery == nil {
		return true
	}
	return *c.AutoEnablePROnDiscovery
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
	if c.ActivityLog.Enabled == nil {
		v := true
		c.ActivityLog.Enabled = &v
	}
	if c.ActivityLog.RetentionDays == nil {
		v := 90
		c.ActivityLog.RetentionDays = &v
	}
	if c.CircuitBreaker.PerPR24h == 0 {
		c.CircuitBreaker.PerPR24h = 3
	}
	if c.CircuitBreaker.PerRepoHr == 0 {
		c.CircuitBreaker.PerRepoHr = 20
	}
	if c.CircuitBreaker.PerIssue24h == 0 {
		c.CircuitBreaker.PerIssue24h = 3
	}
	if c.CircuitBreaker.PerIssueRepoHr == 0 {
		c.CircuitBreaker.PerIssueRepoHr = 10
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
	if v := os.Getenv("HEIMDALLM_EXECUTION_TIMEOUT"); v != "" {
		c.AI.ExecutionTimeout = v
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
	if v := os.Getenv("HEIMDALLM_WATCH_INTERVAL"); v != "" {
		c.GitHub.WatchInterval = v
	}
	if v := os.Getenv("HEIMDALLM_LOCAL_DIR_BASE"); v != "" {
		paths := strings.Split(v, ",")
		cleaned := make([]string, 0, len(paths))
		for _, p := range paths {
			if s := strings.TrimSpace(p); s != "" {
				cleaned = append(cleaned, s)
			}
		}
		if len(cleaned) > 0 {
			c.GitHub.LocalDirBase = cleaned
		}
	}
	if v := os.Getenv("HEIMDALLM_GENERATE_PR_DESCRIPTION"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.AI.GeneratePRDescription = b
		}
	}
	c.applyIssueTrackingEnv()
	c.applyPRMetadataEnv()
}

// applyPRMetadataEnv maps HEIMDALLM_PR_* env vars into the flat [ai] fields.
func (c *Config) applyPRMetadataEnv() {
	if list, ok := csvEnv("HEIMDALLM_PR_REVIEWERS"); ok {
		c.AI.PRReviewers = list
	}
	if list, ok := csvEnv("HEIMDALLM_PR_LABELS"); ok {
		c.AI.PRLabels = list
	}
	if v := os.Getenv("HEIMDALLM_PR_ASSIGNEE"); v != "" {
		c.AI.PRAssignee = strings.TrimSpace(v)
	}
	if v := os.Getenv("HEIMDALLM_PR_DRAFT"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.AI.PRDraft = &b
		}
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
	if list, ok := csvEnv("HEIMDALLM_ISSUE_BLOCKED_LABELS"); ok {
		c.GitHub.IssueTracking.BlockedLabels = list
	}
	if v := os.Getenv("HEIMDALLM_ISSUE_PROMOTE_TO_LABEL"); v != "" {
		c.GitHub.IssueTracking.PromoteToLabel = strings.TrimSpace(v)
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
	if c.ActivityLog.RetentionDays != nil {
		d := *c.ActivityLog.RetentionDays
		if d < 0 || d > 3650 {
			return fmt.Errorf("config: activity_log.retention_days must be between 0 and 3650, got %d", d)
		}
	}
	return nil
}

// ValidateIssueTracking is the package-exported form of validateIssueTracking.
// Used by the PUT /config handler to pre-check a standalone IssueTrackingConfig
// without having to assemble a full Config (which would trip over other
// required fields like ai.primary).
//
// COUPLING: this helper wraps the struct in a zero-valued Config. It stays
// correct only as long as validateIssueTracking reads exclusively from
// c.GitHub.IssueTracking. If you ever extend it to cross-check other Config
// fields (e.g. assignees against GitHub.Repositories), this wrapper must
// take the extra fields as parameters too or future validations will pass
// silently against zero values.
func ValidateIssueTracking(it IssueTrackingConfig) error {
	c := &Config{}
	c.GitHub.IssueTracking = it
	return c.validateIssueTracking()
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
	if len(it.BlockedLabels) > 0 && it.ResolvePromoteToLabel() == "" {
		return fmt.Errorf("config: github.issue_tracking.blocked_labels set but no promote target — set promote_to_label or populate develop_labels")
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

// ReviewGuards resolves configured guard toggles against their defaults and
// returns a ResolvedReviewGuards ready for use by the poller. Both booleans
// default to true when not explicitly configured.
//
// Callers convert the result to pipeline.GateConfig to avoid an import cycle
// (pipeline transitively imports config via the github package).
//
// Callers convert the returned value to pipeline.GateConfig via struct cast:
//
//	gc := pipeline.GateConfig(cfg.ReviewGuards(botLogin))
//
// See the comment on ResolvedReviewGuards for why this shadow type exists.
func (c *Config) ReviewGuards(botLogin string) ResolvedReviewGuards {
	g := ResolvedReviewGuards{
		SkipDrafts:     true,
		SkipSelfAuthor: true,
		BotLogin:       botLogin,
	}
	if v := c.GitHub.ReviewGuards.SkipDrafts; v != nil {
		g.SkipDrafts = *v
	}
	if v := c.GitHub.ReviewGuards.SkipSelfAuthor; v != nil {
		g.SkipSelfAuthor = *v
	}
	return g
}

// DefaultPath returns ~/.config/heimdallm/config.toml
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return home + "/.config/heimdallm/config.toml"
}
