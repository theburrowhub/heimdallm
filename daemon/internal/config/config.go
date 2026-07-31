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

// Poll interval bounds. Any time.ParseDuration value within this range is
// accepted by the daemon (config.toml load and the HTTP config API); the
// discrete whitelist {1m,5m,30m,1h} was removed in favour of a continuous range
// so values like 3m or 10m are valid. 1m keeps the GitHub rate-limit floor; 24h
// is a generous ceiling that still rejects pathological values like 1s or 0.
const (
	minPollInterval = time.Minute
	maxPollInterval = 24 * time.Hour
	// Display forms for error messages — time.Duration.String() renders the
	// constants as "1m0s"/"24h0s", which is noisier than the units operators type.
	minPollIntervalStr = "1m"
	maxPollIntervalStr = "24h"
)

// ValidatePollInterval reports whether raw is an acceptable poll_interval: a
// time.ParseDuration value within [minPollInterval, maxPollInterval]. An empty
// string is allowed (callers apply the default). Exported so the HTTP config
// handler enforces the same rule as config load.
func ValidatePollInterval(raw string) error {
	if raw == "" {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid poll_interval %q: %w", raw, err)
	}
	if d < minPollInterval || d > maxPollInterval {
		return fmt.Errorf("poll_interval %q out of range (must be between %s and %s)", raw, minPollIntervalStr, maxPollIntervalStr)
	}
	return nil
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

// repoNamePattern matches the GitHub repository name segment of an "owner/name"
// slug: alphanumerics plus dots, underscores and hyphens. The "." / ".." cases
// are rejected separately by ValidateRepoSlug to avoid path-traversal tokens.
var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Config is the schema root used by the deterministic TOML projection in
// canonical_map.go. Every exported field in this graph must have a unique
// case-insensitive toml tag. Embedded fields, interface values and slices of
// composite structs/maps are deliberately unsupported; the schema invariant
// test fails before such a change can turn into a runtime Load failure.
type Config struct {
	Server         ServerConfig         `toml:"server"`
	GitHub         GitHubConfig         `toml:"github"`
	AI             AIConfig             `toml:"ai"`
	Retention      RetentionConfig      `toml:"retention"`
	ActivityLog    ActivityLogConfig    `toml:"activity_log"`
	CircuitBreaker CircuitBreakerConfig `toml:"circuit_breaker"`
	Autonomous     AutonomousConfig     `toml:"autonomous"`
	Polling        PollingConfig        `toml:"polling"`
}

type ServerConfig struct {
	Port                 int    `toml:"port"`
	BindAddr             string `toml:"bind_addr"`
	MaxConcurrentWorkers int    `toml:"max_concurrent_workers"`
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
	// When empty, discovery follows PollInterval; set this when discovery
	// should run on its own cadence, for example to preserve Search API budget
	// across many discovery_orgs. Accepts any Go time.ParseDuration value.
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
	IssueModeRefinement IssueMode = "refinement"
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
//	skip_labels  >  blocked_labels  >  review_only_labels  >  refinement_labels  >  develop_labels  >  default_action
//
// The stage labels intentionally prefer the earliest configured state. If an
// issue is temporarily double-labelled during a transition, triage wins over
// refinement, and refinement wins over development, so Heimdallm never skips
// ahead silently.
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
	// Empty in raw config means "use the authenticated GitHub login" at
	// runtime. A deliberately shared queue must be introduced explicitly; the
	// issue pipeline must not treat an absent assignee filter as "anyone".
	Assignees []string `toml:"assignees" json:"assignees"`

	// DevelopLabels are labels that mark an issue as "please implement".
	DevelopLabels []string `toml:"develop_labels" json:"develop_labels"`

	// RefinementLabels are labels that mark an issue as "deeply investigate
	// and produce an implementation plan". This is the trigger for the
	// refinement stage and the target for triage -> refinement promotion.
	RefinementLabels []string `toml:"refinement_labels" json:"refinement_labels"`

	// ReviewOnlyLabels are labels that mark an issue as "please analyse and
	// comment only". In the issue state machine this is the triage stage.
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

	// DefaultAction is applied when an issue carries no label from any
	// configured mode list above. Must be "ignore" or "review_only".
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

// WithDefaultAssignee returns a copy whose assignee scope falls back to the
// authenticated GitHub login. This keeps issue processing single-owner by
// default while preserving explicitly configured assignee lists.
func (c IssueTrackingConfig) WithDefaultAssignee(login string) IssueTrackingConfig {
	if len(c.Assignees) > 0 {
		return c
	}
	login = strings.TrimSpace(strings.TrimLeft(login, "@"))
	if login == "" {
		return c
	}
	c.Assignees = []string{login}
	return c
}

// MatchesAssignees reports whether the current assignee filter permits an
// issue assigned to the provided GitHub logins. An inactive filter permits all;
// an active filter only matches issues with exactly one assignee in scope.
// That single-owner invariant prevents two Heimdallm instances from processing
// the same staged issue when GitHub temporarily shows multiple assignees.
func (c IssueTrackingConfig) MatchesAssignees(assignees []string) bool {
	if len(c.Assignees) == 0 {
		return true
	}
	if len(assignees) != 1 {
		return false
	}
	want := make(map[string]struct{}, len(c.Assignees))
	for _, a := range c.Assignees {
		a = strings.ToLower(strings.TrimSpace(strings.TrimLeft(a, "@")))
		if a != "" {
			want[a] = struct{}{}
		}
	}
	if len(want) == 0 {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(strings.TrimLeft(assignees[0], "@")))
	_, ok := want[a]
	return ok
}

// MatchesInstructionAuthors reports whether login is permitted to issue
// persistent review-instruction directives for this repo. Case-insensitive and
// tolerant of a leading "@". An empty allowlist denies everyone — comment-driven
// instructions are opt-in and must be explicitly granted (issue #383).
func (r RepoAI) MatchesInstructionAuthors(login string) bool {
	login = strings.ToLower(strings.TrimSpace(strings.TrimLeft(login, "@")))
	if login == "" {
		return false
	}
	for _, a := range r.InstructionAuthors {
		if strings.ToLower(strings.TrimSpace(strings.TrimLeft(a, "@"))) == login {
			return true
		}
	}
	return false
}

// Classify returns the processing mode for an issue given its labels.
// Matching is case-insensitive to match the way GitHub displays labels; the
// underlying labels API is case-preserving but the UI is not, so users
// routinely mix "Bug" and "bug" in practice.
//
// Precedence: skip > blocked > review_only > refinement > develop > default_action.
// The stage order follows the state machine: triage (review_only) comes before
// refinement, which comes before development. This keeps messy multi-label
// states recoverable by choosing the earliest stage instead of jumping ahead.
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
	if labelSetIntersects(set, c.ReviewOnlyLabels) {
		return IssueModeReviewOnly
	}
	if labelSetIntersects(set, c.RefinementLabels) {
		return IssueModeRefinement
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
//
// JSON tags must match the snake_case keys written by the PUT /config handler
// (handlers.go:normalizeAgentConfigsForPut) so ApplyStore can unmarshal a
// stored row back into this struct symmetrically.
//
// Boolean fields deliberately do NOT use `omitempty`. With omitempty a future
// caller that marshals this struct directly (instead of the current map[string]any
// path in the handler) would silently drop a `false` value, and ApplyStore's
// "unmarshal into the existing struct" merge would then preserve a TOML `true`
// the operator was trying to override. Keeping the zero value in the JSON
// guarantees the override semantic regardless of how the JSON is produced.
type CLIAgentConfig struct {
	Model        string `toml:"model" json:"model,omitempty"`                 // e.g. "claude-opus-4-6"
	MaxTurns     int    `toml:"max_turns" json:"max_turns,omitempty"`         // claude: --max-turns (0 = not set)
	ApprovalMode string `toml:"approval_mode" json:"approval_mode,omitempty"` // codex/gemini typed approval mode
	ExtraFlags   string `toml:"extra_flags" json:"extra_flags,omitempty"`     // free-form additional CLI flags
	PromptID     string `toml:"prompt" json:"prompt,omitempty"`               // agent-level prompt override

	// Claude-specific flags
	Effort               string `toml:"effort" json:"effort,omitempty"`                       // low|medium|high|max
	PermissionMode       string `toml:"permission_mode" json:"permission_mode,omitempty"`     // default|auto|acceptEdits|dontAsk (bypassPermissions is explicitly forbidden)
	Bare                 bool   `toml:"bare" json:"bare"`                                     // --bare
	DangerouslySkipPerms bool   `toml:"dangerously_skip_perms" json:"dangerously_skip_perms"` // --dangerously-skip-permissions (HTTP may only disable it, see M-5)
	NoSessionPersistence bool   `toml:"no_session_persistence" json:"no_session_persistence"` // --no-session-persistence
	ExecutionTimeout     string `toml:"execution_timeout" json:"execution_timeout,omitempty"` // per-agent override, e.g. "20m"
}

// DefaultTier2RepoConcurrency is the fallback used by both
// applyDefaults and processReposInParallel so the two paths can't
// drift out of sync. See #481.
const DefaultTier2RepoConcurrency = 5

type AIConfig struct {
	Primary          string                    `toml:"primary"`
	Fallback         string                    `toml:"fallback"`
	ReviewMode       string                    `toml:"review_mode"`       // "single" | "multi"
	ExecutionTimeout string                    `toml:"execution_timeout"` // e.g. "20m", "1h"
	Agents           map[string]CLIAgentConfig `toml:"agents"`            // keyed by CLI name
	Repos            map[string]RepoAI         `toml:"repos"`
	Orgs             map[string]OrgAI          `toml:"orgs"`        // per-org AI/issue/PR metadata overrides
	PRMetadata       PRMetadataConfig          `toml:"pr_metadata"` // global PR creation defaults

	// Top-level PR metadata fields — flat alternatives to [ai.pr_metadata].
	// Populated from HEIMDALLM_PR_* env vars or TOML keys directly under [ai].
	PRReviewers []string `toml:"pr_reviewers"`
	PRLabels    []string `toml:"pr_labels"`
	PRAssignee  string   `toml:"pr_assignee"`
	PRDraft     *bool    `toml:"pr_draft,omitempty"`

	// InstructionAuthors are GitHub logins permitted to set persistent,
	// per-repo review instructions via PR comment directives (#383). Resolved
	// through the repo > org > global hierarchy like PRReviewers. Empty means
	// nobody is authorized — the comment-driven feature is opt-in.
	InstructionAuthors []string `toml:"instruction_authors"`

	// IssuePrompt is the global default agent profile ID for issue triage.
	// Per-repo overrides in [ai.repos.<name>] take precedence.
	IssuePrompt string `toml:"issue_prompt"`
	// ImplementPrompt is the global default agent profile ID for auto-implement.
	// Per-repo overrides in [ai.repos.<name>] take precedence.
	ImplementPrompt string `toml:"implement_prompt"`
	// RefinementTimeout caps the deep-investigation stage. It defaults higher
	// than the general executor timeout because refinement is expected to read
	// the repository and build an implementation plan.
	RefinementTimeout string `toml:"refinement_timeout"`

	// Future issue pipeline fields. They are parsed and resolved through the
	// same repo > org > global hierarchy so follow-up pipeline work can consume
	// them without changing the config contract again.
	TriageOwner           string `toml:"triage_owner"`
	CloneDir              string `toml:"clone_dir"`
	AutoPromoteTriage     *bool  `toml:"auto_promote_triage,omitempty"`
	AutoPromoteRefinement *bool  `toml:"auto_promote_refinement,omitempty"`

	// MaxWorktreesPerRepo caps how many per-execution independent snapshots
	// the daemon will hold concurrently for a single repository (#461). A
	// fresh value of 0 inherits the daemon default (5). Set higher if
	// the repo has many independent stages running in parallel; lower
	// if disk pressure dominates.
	MaxWorktreesPerRepo int `toml:"max_worktrees_per_repo"`

	// Tier2RepoConcurrency caps how many repos the Tier 2 issue
	// polling loop processes in parallel within a single tick (#481).
	// A fresh value of 0 inherits the daemon default
	// (DefaultTier2RepoConcurrency). The cap applies to wall-clock
	// parallelism; the GitHub API rate limiter
	// (scheduler.RateLimiter) still throttles network usage.
	Tier2RepoConcurrency int `toml:"tier2_repo_concurrency"`

	// RepoRenameCheckInterval controls how often the rename probe
	// queries GitHub for each monitored repo's canonical full_name
	// to detect a repo or org rename (#489). Empty string falls back
	// to the daemon default (1h). Setting "0" disables the probe
	// entirely; operators can still trigger renames manually via
	// POST /admin/repo-rename.
	RepoRenameCheckInterval string `toml:"repo_rename_check_interval"`

	// GeneratePRDescription enables LLM-generated PR titles and descriptions
	// for auto_implement PRs. When true, after the implementation commit,
	// a second LLM call generates a rich PR description from the diff.
	// Default: false (backwards compat).
	GeneratePRDescription bool `toml:"generate_pr_description"`

	// NeverApproveWithIssues, when true, downgrades an otherwise-APPROVE review
	// to COMMENT whenever the review found any issue. REQUEST_CHANGES (high
	// severity) is unaffected. Default: false (backwards compat). Overridable
	// per-org and per-repo.
	NeverApproveWithIssues bool `toml:"never_approve_with_issues"`

	// NeverApproveMinSeverity is the minimum finding severity that triggers
	// the NeverApproveWithIssues downgrade: "low", "medium" or "high".
	// Empty defaults to "low" (any finding downgrades — backwards compat).
	// With "medium", reviews whose findings are all low-severity nits still
	// approve. Only meaningful when NeverApproveWithIssues is on.
	// Overridable per-org and per-repo.
	NeverApproveMinSeverity string `toml:"never_approve_min_severity"`

	// ReviewResponse configures phase 2 of the PR review-state vigilance
	// feature (#482): the daemon optionally posts an AI-generated reply
	// when an external reviewer leaves COMMENTED feedback on a PR that
	// auto_implement created. Off by default — operators opt in per
	// daemon by flipping Enabled.
	ReviewResponse ReviewResponseConfig `toml:"review_response"`

	// ReviewFix configures phase 3: when an external reviewer requests
	// changes, the daemon optionally re-runs the agent on the PR's head
	// branch and pushes a fix. Off by default; carries a hard
	// per-PR-lifetime cap so the worst case is bounded.
	ReviewFix ReviewFixConfig `toml:"review_fix"`
}

// ReviewResponseConfig caps the AI cost of phase-2 auto-responses to
// PR review comments (#482). All thresholds default to safe values via
// applyDefaults; a zero or negative value also falls back to the
// default rather than meaning "unlimited".
type ReviewResponseConfig struct {
	// Enabled defaults to false. When false the Responder is a no-op
	// regardless of every other knob — opt-in only.
	Enabled bool `toml:"enabled"`
	// PerPRLifetime caps how many responder runs a single PR can ever
	// trigger. The counter is persisted on the PR row so a daemon
	// restart cannot reset it; operators can manually zero it in SQL
	// if they want the agent to start over. A future follow-up may
	// add a sliding-24h cap as a second axis, but lifetime is the
	// safer default for the first opt-in surface.
	PerPRLifetime int `toml:"per_pr_lifetime"`
	// CooldownSecs is the minimum gap between two responder runs on
	// the same PR. Protects against a chatty reviewer firing the
	// responder once per comment within the same tick.
	CooldownSecs int `toml:"cooldown_secs"`
}

// ReviewFixConfig caps the AI cost of phase-3 auto-fix runs (#482).
// The lifetime cap is intentionally low: an operator who wants more
// rounds must opt-in explicitly so we never silently amplify cost.
type ReviewFixConfig struct {
	Enabled       bool `toml:"enabled"`
	PerPRLifetime int  `toml:"per_pr_lifetime"`
	CooldownSecs  int  `toml:"cooldown_secs"`
}

// Defaults for the review-response and review-fix paths (#482).
// Single source of truth: applyDefaults reads these, the runtime
// guards reference them when callers leave a 0 in TOML.
const (
	DefaultReviewResponsePerPRLifetime = 5
	DefaultReviewResponseCooldownSecs  = 300
	DefaultReviewFixPerPRLifetime      = 3
	DefaultReviewFixCooldownSecs       = 300
)

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
	ImplementPrompt       string `toml:"implement_prompt"`
	RefinementTimeout     string `toml:"refinement_timeout"`
	Fallback              string `toml:"fallback"`
	ReviewMode            string `toml:"review_mode"` // "" = inherit global
	LocalDir              string `toml:"local_dir"`   // local repo path for full-repo analysis
	TriageOwner           string `toml:"triage_owner"`
	CloneDir              string `toml:"clone_dir"`
	AutoPromoteTriage     *bool  `toml:"auto_promote_triage,omitempty"`
	AutoPromoteRefinement *bool  `toml:"auto_promote_refinement,omitempty"`

	// PR creation metadata (applied by auto_implement after CreatePR).
	// Nil slices inherit from org/global; non-nil empty slices explicitly
	// clear inherited values for this repo.
	PRReviewers []string `toml:"pr_reviewers"`       // GitHub logins to request review from
	PRAssignee  string   `toml:"pr_assignee"`        // GitHub login to assign the PR to
	PRLabels    []string `toml:"pr_labels"`          // labels to add to the PR
	PRDraft     *bool    `toml:"pr_draft,omitempty"` // create as draft PR

	InstructionAuthors []string `toml:"instruction_authors"` // GitHub logins allowed to set standing instructions (#383)

	// GeneratePRDescription overrides the global ai.generate_pr_description
	// for this repo. nil = inherit from global.
	GeneratePRDescription *bool `toml:"generate_pr_description,omitempty"`

	// NeverApproveWithIssues overrides ai.never_approve_with_issues for this
	// repo. nil = inherit from org/global.
	NeverApproveWithIssues *bool `toml:"never_approve_with_issues,omitempty"`

	// NeverApproveMinSeverity overrides ai.never_approve_min_severity for
	// this repo. Empty = inherit from org/global.
	NeverApproveMinSeverity string `toml:"never_approve_min_severity,omitempty"`

	// Per-repo issue tracking override. Nil fields inherit from org/global.
	IssueTracking *IssueTrackingOverride `toml:"issue_tracking,omitempty" json:"issue_tracking,omitempty"`
	// CircuitBreaker overrides circuit-breaker caps for this repo.
	// nil = inherit from org/global. Present fields overlay the inherited baseline.
	CircuitBreaker *CircuitBreakerConfig `toml:"circuit_breaker,omitempty"`
}

// PRMetadataConfig holds global defaults for PR creation metadata,
// used as fallback when per-repo config is not set.
type PRMetadataConfig struct {
	Reviewers []string `toml:"reviewers"`
	Labels    []string `toml:"labels"`
	Assignee  string   `toml:"pr_assignee"`
	Draft     *bool    `toml:"pr_draft,omitempty"`
}

// IssueTrackingOverride holds repo/org scoped issue-tracking overrides.
//
// Pointer bools and nil slices mean "inherit". Non-nil slices, including
// empty slices, are explicit overrides. That distinction matters for org scope:
// an org must be able to intentionally clear a global label list for all repos.
type IssueTrackingOverride struct {
	Enabled          *bool      `toml:"enabled,omitempty" json:"enabled,omitempty"`
	DevelopEnabled   *bool      `toml:"develop_enabled,omitempty" json:"develop_enabled,omitempty"`
	FilterMode       FilterMode `toml:"filter_mode,omitempty" json:"filter_mode,omitempty"`
	Organizations    []string   `toml:"organizations,omitempty" json:"organizations,omitempty"`
	Assignees        []string   `toml:"assignees,omitempty" json:"assignees,omitempty"`
	DevelopLabels    []string   `toml:"develop_labels,omitempty" json:"develop_labels,omitempty"`
	RefinementLabels []string   `toml:"refinement_labels,omitempty" json:"refinement_labels,omitempty"`
	ReviewOnlyLabels []string   `toml:"review_only_labels,omitempty" json:"review_only_labels,omitempty"`
	SkipLabels       []string   `toml:"skip_labels,omitempty" json:"skip_labels,omitempty"`
	BlockedLabels    []string   `toml:"blocked_labels,omitempty" json:"blocked_labels,omitempty"`
	PromoteToLabel   string     `toml:"promote_to_label,omitempty" json:"promote_to_label,omitempty"`
	DefaultAction    string     `toml:"default_action,omitempty" json:"default_action,omitempty"`
}

// OrgAI holds per-organisation overrides, applied to all repos in the org
// unless overridden per-repo. Keyed by GitHub org slug under [ai.orgs."org-name"].
type OrgAI struct {
	Primary               string `toml:"primary"`
	Prompt                string `toml:"prompt"`
	IssuePrompt           string `toml:"issue_prompt"`
	ImplementPrompt       string `toml:"implement_prompt"`
	RefinementTimeout     string `toml:"refinement_timeout"`
	Fallback              string `toml:"fallback"`
	ReviewMode            string `toml:"review_mode"`
	LocalDir              string `toml:"local_dir"`
	TriageOwner           string `toml:"triage_owner"`
	CloneDir              string `toml:"clone_dir"`
	AutoPromoteTriage     *bool  `toml:"auto_promote_triage,omitempty"`
	AutoPromoteRefinement *bool  `toml:"auto_promote_refinement,omitempty"`

	// Nil slices inherit from global; non-nil empty slices explicitly clear
	// inherited values for every repo in this org.
	PRReviewers        []string `toml:"pr_reviewers"`
	PRAssignee         string   `toml:"pr_assignee"`
	PRLabels           []string `toml:"pr_labels"`
	PRDraft            *bool    `toml:"pr_draft,omitempty"`
	InstructionAuthors []string `toml:"instruction_authors"` // see RepoAI.InstructionAuthors (#383)

	GeneratePRDescription   *bool                  `toml:"generate_pr_description,omitempty"`
	NeverApproveWithIssues  *bool                  `toml:"never_approve_with_issues,omitempty"`
	NeverApproveMinSeverity string                 `toml:"never_approve_min_severity,omitempty"`
	IssueTracking           *IssueTrackingOverride `toml:"issue_tracking,omitempty" json:"issue_tracking,omitempty"`
	// CircuitBreaker overrides circuit-breaker caps for all repos in this org.
	// nil = inherit from global. Present fields overlay the global baseline.
	CircuitBreaker *CircuitBreakerConfig `toml:"circuit_breaker,omitempty"`
}

// MaxRetentionDays is the upper bound (≈10 years) shared by every retention
// limit: retention.max_days, activity_log.retention_days, and the HTTP
// PUT /config validator. Keeping it in one place stops the three checks drifting.
const MaxRetentionDays = 3650

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

// ResolveLocalDir picks the effective operator-owned repository source for a
// given repo, using this precedence:
//
//  1. The explicit `local_dir` from config (the `configured` argument).
//  2. Each path in `localDirBases` checked in order — first match wins.
//     Supports multiple workspace groups (e.g. ai-platform repos in one
//     dir, another team in another) without per-repo local_dir entries.
//  3. `DefaultReposMountPath/<short-name>` when that directory exists —
//     lets an operator drop a single HEIMDALLM_LOCAL_DIR_BASE into
//     docker/.env and have every monitored repo picked up without also
//     touching the per-repo override in the UI.
//  4. Empty string — no local source is available (diff-only mode unless the
//     caller prepares a managed clone).
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
	gGenDesc := c.AI.GeneratePRDescription
	gNever := c.AI.NeverApproveWithIssues
	out := RepoAI{
		Primary:                 c.AI.Primary,
		Fallback:                c.AI.Fallback,
		ReviewMode:              c.AI.ReviewMode,
		IssuePrompt:             c.AI.IssuePrompt,
		ImplementPrompt:         c.AI.ImplementPrompt,
		RefinementTimeout:       c.AI.RefinementTimeout,
		PRReviewers:             gReviewers,
		PRLabels:                gLabels,
		PRAssignee:              gAssignee,
		PRDraft:                 gDraft,
		GeneratePRDescription:   &gGenDesc,
		NeverApproveWithIssues:  &gNever,
		NeverApproveMinSeverity: c.AI.NeverApproveMinSeverity,
		TriageOwner:             c.AI.TriageOwner,
		CloneDir:                c.AI.CloneDir,
		AutoPromoteTriage:       c.AI.AutoPromoteTriage,
		AutoPromoteRefinement:   c.AI.AutoPromoteRefinement,
		InstructionAuthors:      c.AI.InstructionAuthors,
	}
	if org := repoOrg(repo); org != "" && c.AI.Orgs != nil {
		if o, ok := c.AI.Orgs[org]; ok {
			applyOrgAI(&out, o)
		}
	}
	if c.AI.Repos != nil {
		if r, ok := c.AI.Repos[repo]; ok {
			applyRepoAI(&out, r)
		}
	}
	return out
}

func applyOrgAI(out *RepoAI, o OrgAI) {
	applyScopedAI(out, scopedAIFields{
		Primary:                 o.Primary,
		Fallback:                o.Fallback,
		ReviewMode:              o.ReviewMode,
		Prompt:                  o.Prompt,
		IssuePrompt:             o.IssuePrompt,
		ImplementPrompt:         o.ImplementPrompt,
		RefinementTimeout:       o.RefinementTimeout,
		LocalDir:                o.LocalDir,
		TriageOwner:             o.TriageOwner,
		CloneDir:                o.CloneDir,
		AutoPromoteTriage:       o.AutoPromoteTriage,
		AutoPromoteRefinement:   o.AutoPromoteRefinement,
		PRReviewers:             o.PRReviewers,
		PRLabels:                o.PRLabels,
		PRAssignee:              o.PRAssignee,
		PRDraft:                 o.PRDraft,
		GeneratePRDescription:   o.GeneratePRDescription,
		NeverApproveWithIssues:  o.NeverApproveWithIssues,
		NeverApproveMinSeverity: o.NeverApproveMinSeverity,
		InstructionAuthors:      o.InstructionAuthors,
	})
}

func applyRepoAI(out *RepoAI, r RepoAI) {
	applyScopedAI(out, scopedAIFields{
		Primary:                 r.Primary,
		Fallback:                r.Fallback,
		ReviewMode:              r.ReviewMode,
		Prompt:                  r.Prompt,
		IssuePrompt:             r.IssuePrompt,
		ImplementPrompt:         r.ImplementPrompt,
		RefinementTimeout:       r.RefinementTimeout,
		LocalDir:                r.LocalDir,
		TriageOwner:             r.TriageOwner,
		CloneDir:                r.CloneDir,
		AutoPromoteTriage:       r.AutoPromoteTriage,
		AutoPromoteRefinement:   r.AutoPromoteRefinement,
		PRReviewers:             r.PRReviewers,
		PRLabels:                r.PRLabels,
		PRAssignee:              r.PRAssignee,
		PRDraft:                 r.PRDraft,
		GeneratePRDescription:   r.GeneratePRDescription,
		NeverApproveWithIssues:  r.NeverApproveWithIssues,
		NeverApproveMinSeverity: r.NeverApproveMinSeverity,
		InstructionAuthors:      r.InstructionAuthors,
	})
}

type scopedAIFields struct {
	Primary                 string
	Fallback                string
	ReviewMode              string
	Prompt                  string
	IssuePrompt             string
	ImplementPrompt         string
	RefinementTimeout       string
	LocalDir                string
	TriageOwner             string
	CloneDir                string
	AutoPromoteTriage       *bool
	AutoPromoteRefinement   *bool
	PRReviewers             []string
	PRLabels                []string
	PRAssignee              string
	PRDraft                 *bool
	GeneratePRDescription   *bool
	NeverApproveWithIssues  *bool
	NeverApproveMinSeverity string
	InstructionAuthors      []string
}

func applyScopedAI(out *RepoAI, fields scopedAIFields) {
	if fields.Primary != "" {
		out.Primary = fields.Primary
	}
	if fields.Fallback != "" {
		out.Fallback = fields.Fallback
	}
	if fields.ReviewMode != "" {
		out.ReviewMode = fields.ReviewMode
	}
	if fields.Prompt != "" {
		out.Prompt = fields.Prompt
	}
	if fields.IssuePrompt != "" {
		out.IssuePrompt = fields.IssuePrompt
	}
	if fields.ImplementPrompt != "" {
		out.ImplementPrompt = fields.ImplementPrompt
	}
	if fields.RefinementTimeout != "" {
		out.RefinementTimeout = fields.RefinementTimeout
	}
	if fields.LocalDir != "" {
		out.LocalDir = fields.LocalDir
	}
	if fields.TriageOwner != "" {
		out.TriageOwner = fields.TriageOwner
	}
	if fields.CloneDir != "" {
		out.CloneDir = fields.CloneDir
	}
	if fields.AutoPromoteTriage != nil {
		out.AutoPromoteTriage = fields.AutoPromoteTriage
	}
	if fields.AutoPromoteRefinement != nil {
		out.AutoPromoteRefinement = fields.AutoPromoteRefinement
	}
	if fields.PRReviewers != nil {
		out.PRReviewers = fields.PRReviewers
	}
	if fields.PRLabels != nil {
		out.PRLabels = fields.PRLabels
	}
	if fields.PRAssignee != "" {
		out.PRAssignee = fields.PRAssignee
	}
	if fields.PRDraft != nil {
		out.PRDraft = fields.PRDraft
	}
	if fields.GeneratePRDescription != nil {
		out.GeneratePRDescription = fields.GeneratePRDescription
	}
	if fields.NeverApproveWithIssues != nil {
		out.NeverApproveWithIssues = fields.NeverApproveWithIssues
	}
	if fields.NeverApproveMinSeverity != "" {
		out.NeverApproveMinSeverity = fields.NeverApproveMinSeverity
	}
	if fields.InstructionAuthors != nil {
		out.InstructionAuthors = fields.InstructionAuthors
	}
}

// IssueTrackingForRepo returns the issue tracking config for a specific repo,
// merging repo > org > global overrides field-by-field.
func (c *Config) IssueTrackingForRepo(repo string) IssueTrackingConfig {
	merged := c.GitHub.IssueTracking
	if org := repoOrg(repo); org != "" && c.AI.Orgs != nil {
		if o, ok := c.AI.Orgs[org]; ok {
			applyIssueTrackingOverride(&merged, o.IssueTracking)
		}
	}
	if c.AI.Repos != nil {
		if r, ok := c.AI.Repos[repo]; ok {
			applyIssueTrackingOverride(&merged, r.IssueTracking)
		}
	}
	return merged
}

func applyIssueTrackingOverride(merged *IssueTrackingConfig, ov *IssueTrackingOverride) {
	if ov == nil {
		return
	}
	if ov.DevelopLabels != nil {
		merged.DevelopLabels = ov.DevelopLabels
	}
	if ov.RefinementLabels != nil {
		merged.RefinementLabels = ov.RefinementLabels
	}
	if ov.ReviewOnlyLabels != nil {
		merged.ReviewOnlyLabels = ov.ReviewOnlyLabels
	}
	if ov.SkipLabels != nil {
		merged.SkipLabels = ov.SkipLabels
	}
	if ov.BlockedLabels != nil {
		merged.BlockedLabels = ov.BlockedLabels
	}
	if ov.FilterMode != "" {
		merged.FilterMode = ov.FilterMode
	}
	if ov.DefaultAction != "" {
		merged.DefaultAction = ov.DefaultAction
	}
	if ov.PromoteToLabel != "" {
		merged.PromoteToLabel = ov.PromoteToLabel
	}
	if ov.Organizations != nil {
		merged.Organizations = ov.Organizations
	}
	if ov.Assignees != nil {
		merged.Assignees = ov.Assignees
	}
	if ov.Enabled == nil && (len(ov.DevelopLabels) > 0 || len(ov.RefinementLabels) > 0 || len(ov.ReviewOnlyLabels) > 0) {
		merged.Enabled = true
	}
	if ov.Enabled != nil {
		merged.Enabled = *ov.Enabled
	}
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
	if c.Server.MaxConcurrentWorkers == 0 {
		c.Server.MaxConcurrentWorkers = 5
	}
	if c.GitHub.PollInterval == "" {
		c.GitHub.PollInterval = "5m"
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
	if c.AI.RefinementTimeout == "" {
		c.AI.RefinementTimeout = "30m"
	}
	if c.AI.MaxWorktreesPerRepo == 0 {
		c.AI.MaxWorktreesPerRepo = 5
	}
	if c.AI.Tier2RepoConcurrency == 0 {
		c.AI.Tier2RepoConcurrency = DefaultTier2RepoConcurrency
	}
	// Empty string falls back to 1h. "0" stays "0" — operator-disabled.
	if c.AI.RepoRenameCheckInterval == "" {
		c.AI.RepoRenameCheckInterval = "1h"
	}
	// Review-state vigilance defaults (#482). The Enabled flag is NOT
	// touched here — it must stay false unless the operator explicitly
	// opts in via TOML. Only the cap/cooldown axes fall back to defaults
	// when left at zero (treating zero as "unlimited" would defeat the
	// safety story).
	if c.AI.ReviewResponse.PerPRLifetime <= 0 {
		c.AI.ReviewResponse.PerPRLifetime = DefaultReviewResponsePerPRLifetime
	}
	if c.AI.ReviewResponse.CooldownSecs <= 0 {
		c.AI.ReviewResponse.CooldownSecs = DefaultReviewResponseCooldownSecs
	}
	if c.AI.ReviewFix.PerPRLifetime <= 0 {
		c.AI.ReviewFix.PerPRLifetime = DefaultReviewFixPerPRLifetime
	}
	if c.AI.ReviewFix.CooldownSecs <= 0 {
		c.AI.ReviewFix.CooldownSecs = DefaultReviewFixCooldownSecs
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
	if c.CircuitBreaker.PerImplRepoHr == 0 {
		c.CircuitBreaker.PerImplRepoHr = 5
	}
	c.applyAutonomousDefaults()
	c.applyPollingDefaults()
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
	if v := os.Getenv("HEIMDALLM_MAX_CONCURRENT_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Server.MaxConcurrentWorkers = n
		}
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
	if v := os.Getenv("HEIMDALLM_REFINEMENT_TIMEOUT"); v != "" {
		c.AI.RefinementTimeout = v
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
	if list, ok := csvEnv("HEIMDALLM_ISSUE_REFINEMENT_LABELS"); ok {
		c.GitHub.IssueTracking.RefinementLabels = list
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
	if err := ValidatePollInterval(c.GitHub.PollInterval); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// [polling] takes precedence over [github].poll_interval at resolution
	// time, so it has to clear the same bar — otherwise the newer section is a
	// way around the quota guard the older one enforces.
	if err := c.ValidatePolling(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// Validate every persisted execution option before it reaches Executor.
	// This catches legacy TOML/store rows during reload; ExecuteRaw repeats the
	// same checks at the subprocess boundary as defense in depth.
	for name, a := range c.AI.Agents {
		// Preserve unknown legacy profiles as inert configuration. They cannot
		// be selected by Detect/ExecuteRaw, and every HTTP write still rejects
		// unknown CLI names, but an unused historical section must not prevent
		// the daemon from starting after an upgrade.
		if err := executor.ValidateCLIName(name); err != nil {
			continue
		}
		if err := executor.ValidateModel(a.Model); err != nil {
			return fmt.Errorf("config: agents[%s].model: %w", name, err)
		}
		if a.MaxTurns < 0 {
			return fmt.Errorf("config: agents[%s].max_turns must be non-negative", name)
		}
		if err := executor.ValidateEffort(a.Effort); err != nil {
			return fmt.Errorf("config: agents[%s].effort: %w", name, err)
		}
		if err := executor.ValidatePermissionMode(a.PermissionMode); err != nil {
			return fmt.Errorf("config: agents[%s].permission_mode: %w", name, err)
		}
		if err := executor.ValidateApprovalModeForCLI(name, a.ApprovalMode); err != nil {
			return fmt.Errorf("config: agents[%s].approval_mode: %w", name, err)
		}
		if err := executor.ValidateExtraFlagsForCLI(name, a.ExtraFlags); err != nil {
			return fmt.Errorf("config: agents[%s].extra_flags: %w", name, err)
		}
	}
	if err := c.validateRefinementTimeouts(); err != nil {
		return err
	}
	if err := c.validateDiscovery(); err != nil {
		return err
	}
	if err := c.validateOrgKeys(); err != nil {
		return err
	}
	if err := c.validateIssueTracking(); err != nil {
		return err
	}
	if err := c.validateScopedIssueTracking(); err != nil {
		return err
	}
	if err := c.validateNeverApproveMinSeverity(); err != nil {
		return err
	}
	// Bound the review-retention window for the TOML and env paths (the HTTP
	// PUT /config validator covers its own path). A negative value would push
	// PurgeOldReviews' cutoff into the future and wipe all reviews (#551).
	if c.Retention.MaxDays < 0 || c.Retention.MaxDays > MaxRetentionDays {
		return fmt.Errorf("config: retention.max_days must be between 0 and %d, got %d", MaxRetentionDays, c.Retention.MaxDays)
	}
	if c.ActivityLog.RetentionDays != nil {
		d := *c.ActivityLog.RetentionDays
		if d < 0 || d > MaxRetentionDays {
			return fmt.Errorf("config: activity_log.retention_days must be between 0 and %d, got %d", MaxRetentionDays, d)
		}
	}
	return nil
}

// sanitizeLegacyAgentExecutionPolicy keeps trusted TOML/env configurations
// from becoming a startup regression when Heimdallm learns about a newly
// dangerous provider flag. HTTP writes remain strict and ExecuteRaw validates
// again at the subprocess boundary; this compatibility path only canonicalizes
// safe values and removes the individual unsafe field with an actionable log.
//
// Unknown profiles are preserved but inert. Detect and ExecuteRaw only accept
// the four supported CLI names, while retaining the map entry avoids deleting
// a user's dormant configuration during an upgrade.
func (c *Config) sanitizeLegacyAgentExecutionPolicy(source string) {
	for name, agent := range c.AI.Agents {
		if err := executor.ValidateCLIName(name); err != nil {
			slog.Warn("config: preserving inert legacy agent profile for unknown CLI",
				"source", source, "agent", name, "err", err)
			continue
		}
		c.AI.Agents[name] = sanitizeLegacyAgentConfig(name, agent, source)
	}
}

func sanitizeLegacyAgentConfig(name string, agent CLIAgentConfig, source string) CLIAgentConfig {
	agent = sanitizeAgentExecutionFields(name, agent, source, "legacy")
	if normalized, err := executor.NormalizePermissionMode(agent.PermissionMode); err != nil {
		slog.Warn("config: ignored unsafe legacy agent field",
			"source", source, "agent", name, "field", "permission_mode", "err", err)
		agent.PermissionMode = ""
	} else {
		agent.PermissionMode = normalized
	}
	if normalized, err := executor.NormalizeApprovalModeForCLI(name, agent.ApprovalMode); err != nil {
		slog.Warn("config: ignored unsafe legacy agent field",
			"source", source, "agent", name, "field", "approval_mode", "err", err)
		agent.ApprovalMode = ""
	} else {
		agent.ApprovalMode = normalized
	}

	opts, migrated := executor.MigrateLegacyTypedExtraFlagsForCLI(name, executor.ExecOptions{
		Model:      agent.Model,
		MaxTurns:   agent.MaxTurns,
		ExtraFlags: agent.ExtraFlags,
		Effort:     agent.Effort,
	})
	if len(migrated) > 0 {
		slog.Warn("config: migrated legacy extra_flags to typed agent fields",
			"source", source, "agent", name, "fields", migrated)
	}
	agent.Model = opts.Model
	agent.MaxTurns = opts.MaxTurns
	agent.Effort = opts.Effort
	agent.ExtraFlags = opts.ExtraFlags
	// MigrateLegacyTypedExtraFlagsForCLI currently emits only validated
	// values. Keep the compatibility boundary defensive nevertheless: a
	// future migration must not turn a formerly survivable legacy config into
	// a startup failure by returning an unsafe typed value.
	agent = sanitizeAgentExecutionFields(name, agent, source, "migrated")
	if err := executor.ValidateExtraFlagsForCLI(name, agent.ExtraFlags); err != nil {
		slog.Warn("config: ignored unsafe legacy agent field",
			"source", source, "agent", name, "field", "extra_flags", "err", err)
		agent.ExtraFlags = ""
	}
	return agent
}

func sanitizeAgentExecutionFields(
	name string,
	agent CLIAgentConfig,
	source string,
	provenance string,
) CLIAgentConfig {
	unsafeMessage := "config: ignored unsafe " + provenance + " agent field"
	invalidMessage := "config: ignored invalid " + provenance + " agent field"
	agent.Model = strings.TrimSpace(agent.Model)
	if err := executor.ValidateModel(agent.Model); err != nil {
		slog.Warn(unsafeMessage,
			"source", source, "agent", name, "field", "model", "err", err)
		agent.Model = ""
	}
	if agent.MaxTurns < 0 {
		slog.Warn(invalidMessage,
			"source", source, "agent", name, "field", "max_turns",
			"err", "value must be non-negative")
		agent.MaxTurns = 0
	}
	if normalized, err := executor.NormalizeEffort(agent.Effort); err != nil {
		slog.Warn(unsafeMessage,
			"source", source, "agent", name, "field", "effort", "err", err)
		agent.Effort = ""
	} else {
		agent.Effort = normalized
	}
	return agent
}

// SanitizeLegacyAgentExecutionPolicyMap migrates only the trusted agent
// settings already present in a TOML map. HTTP handlers call this before
// applying their separately validated payload, so an old local --model flag
// cannot make an unrelated PATCH fail while a new HTTP --model flag remains a
// strict 400. Known CLIAgentConfig leaves for supported providers are
// canonicalized before any typed decode; unknown fields and inert provider
// profiles remain untouched. Trusted dangerously_skip_perms values are
// preserved, with conflicting aliases resolved fail-closed.
func SanitizeLegacyAgentExecutionPolicyMap(m map[string]any, source string) error {
	ai, present, err := canonicalizeLegacyMapChild(m, "ai", "config")
	if err != nil || !present {
		return err
	}
	agents, present, err := canonicalizeLegacyMapChild(ai, "agents", "config.ai")
	if err != nil || !present {
		return err
	}
	for name, rawAgent := range agents {
		if err := executor.ValidateCLIName(name); err != nil {
			// Unknown profiles are intentionally inert and preserved exactly.
			// Do not let newly introduced validation for supported providers
			// turn a dormant future/legacy profile into a startup regression.
			continue
		}
		agentMap, ok := rawAgent.(map[string]any)
		if !ok {
			continue
		}
		agentPath := fmt.Sprintf(`config.ai.agents[%q]`, name)
		before, rewrite, err := legacyAgentConfigFromMap(agentMap, agentPath)
		if err != nil {
			return fmt.Errorf("config: sanitize legacy TOML agent %q: %w", name, err)
		}
		after := sanitizeLegacyAgentConfig(name, before, source)
		syncLegacyAgentStringField(agentMap, "model", before.Model, after.Model, rewrite["model"])
		syncLegacyAgentIntField(agentMap, "max_turns", before.MaxTurns, after.MaxTurns, rewrite["max_turns"])
		syncLegacyAgentStringField(agentMap, "approval_mode", before.ApprovalMode, after.ApprovalMode, rewrite["approval_mode"])
		syncLegacyAgentStringField(agentMap, "extra_flags", before.ExtraFlags, after.ExtraFlags, rewrite["extra_flags"])
		syncLegacyAgentStringField(agentMap, "effort", before.Effort, after.Effort, rewrite["effort"])
		syncLegacyAgentStringField(agentMap, "permission_mode", before.PermissionMode, after.PermissionMode, rewrite["permission_mode"])
		syncLegacyAgentTrustedStringField(agentMap, "prompt", before.PromptID, after.PromptID, rewrite["prompt"])
		syncLegacyAgentTrustedStringField(agentMap, "execution_timeout", before.ExecutionTimeout, after.ExecutionTimeout, rewrite["execution_timeout"])
		syncLegacyAgentBoolField(agentMap, "bare", before.Bare, after.Bare, rewrite["bare"])
		syncLegacyAgentBoolField(agentMap, "dangerously_skip_perms", before.DangerouslySkipPerms, after.DangerouslySkipPerms, rewrite["dangerously_skip_perms"])
		syncLegacyAgentBoolField(agentMap, "no_session_persistence", before.NoSessionPersistence, after.NoSessionPersistence, rewrite["no_session_persistence"])
	}
	return nil
}

func canonicalizeLegacyMapChild(m map[string]any, canonical, path string) (map[string]any, bool, error) {
	aliases := caseFoldedMapKeys(m, canonical)
	switch len(aliases) {
	case 0:
		return nil, false, nil
	case 1:
		key := aliases[0]
		child, ok := m[key].(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("legacy TOML key %q must be a table", path+"."+key)
		}
		if key != canonical {
			delete(m, key)
			m[canonical] = child
		}
		return child, true, nil
	default:
		return nil, false, fmt.Errorf(
			"ambiguous structural aliases at %q for %q (%s); keep a single canonical %q key",
			path, canonical, strings.Join(aliases, ", "), canonical,
		)
	}
}

func legacyAgentConfigFromMap(m map[string]any, path string) (CLIAgentConfig, map[string]bool, error) {
	var agent CLIAgentConfig
	rewrite := make(map[string]bool)
	stringField := func(canonical string, target *string) error {
		raw, present, canonicalize, err := legacyMapValue(m, canonical, path)
		if err != nil {
			return err
		}
		rewrite[canonical] = canonicalize
		if !present {
			return nil
		}
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", canonical)
		}
		*target = value
		return nil
	}
	boolField := func(canonical string, target *bool) error {
		raw, present, canonicalize, err := legacyMapValue(m, canonical, path)
		if err != nil {
			return err
		}
		rewrite[canonical] = canonicalize
		if !present {
			return nil
		}
		value, ok := raw.(bool)
		if !ok {
			return fmt.Errorf("%s must be a boolean", canonical)
		}
		*target = value
		return nil
	}
	if err := stringField("model", &agent.Model); err != nil {
		return CLIAgentConfig{}, nil, err
	}
	if err := stringField("approval_mode", &agent.ApprovalMode); err != nil {
		return CLIAgentConfig{}, nil, err
	}
	if err := stringField("extra_flags", &agent.ExtraFlags); err != nil {
		return CLIAgentConfig{}, nil, err
	}
	if err := stringField("effort", &agent.Effort); err != nil {
		return CLIAgentConfig{}, nil, err
	}
	if err := stringField("permission_mode", &agent.PermissionMode); err != nil {
		return CLIAgentConfig{}, nil, err
	}
	if err := stringField("prompt", &agent.PromptID); err != nil {
		return CLIAgentConfig{}, nil, err
	}
	if err := stringField("execution_timeout", &agent.ExecutionTimeout); err != nil {
		return CLIAgentConfig{}, nil, err
	}
	if err := boolField("bare", &agent.Bare); err != nil {
		return CLIAgentConfig{}, nil, err
	}
	if err := boolField("no_session_persistence", &agent.NoSessionPersistence); err != nil {
		return CLIAgentConfig{}, nil, err
	}
	raw, present, canonicalize, err := legacyMapValue(m, "max_turns", path)
	if err != nil {
		return CLIAgentConfig{}, nil, err
	}
	if present {
		rewrite["max_turns"] = canonicalize
		value, err := legacyMapInt(raw)
		if err != nil {
			return CLIAgentConfig{}, nil, fmt.Errorf("max_turns: %w", err)
		}
		agent.MaxTurns = value
	} else {
		rewrite["max_turns"] = canonicalize
	}
	dangerous, present, canonicalize, err := legacyDangerousBoolValue(m)
	if err != nil {
		return CLIAgentConfig{}, nil, err
	}
	rewrite["dangerously_skip_perms"] = canonicalize
	if present {
		// This is trusted TOML state. Preserve true exactly; the HTTP boundary
		// separately prevents untrusted callers from granting the capability.
		// Conflicting trusted aliases resolve fail-closed: any false wins.
		agent.DangerouslySkipPerms = dangerous
	}
	return agent, rewrite, nil
}

func legacyDangerousBoolValue(m map[string]any) (value bool, present, canonicalize bool, err error) {
	const canonical = "dangerously_skip_perms"
	aliases := caseFoldedMapKeys(m, canonical)
	if len(aliases) == 0 {
		return false, false, false, nil
	}
	value, err = resolveFailClosedBool(m, aliases, canonical)
	if err != nil {
		return false, false, false, err
	}
	return value, true, len(aliases) != 1 || aliases[0] != canonical, nil
}

func legacyMapValue(m map[string]any, canonical, path string) (value any, present, canonicalize bool, err error) {
	aliases := caseFoldedMapKeys(m, canonical)
	if len(aliases) == 0 {
		return nil, false, false, nil
	}
	selected, discarded, err := selectCanonicalMapKey(aliases, canonical)
	if err != nil {
		return nil, false, false, err
	}
	warnDiscardedAliases(path, canonical, discarded)
	return m[selected], true, selected != canonical || len(discarded) > 0, nil
}

func legacyMapInt(raw any) (int, error) {
	switch value := raw.(type) {
	case int:
		return value, nil
	case int64:
		converted := int(value)
		if int64(converted) != value {
			return 0, fmt.Errorf("value is outside the supported integer range")
		}
		return converted, nil
	case float64:
		converted := int(value)
		if float64(converted) != value {
			return 0, fmt.Errorf("value must be an integer")
		}
		return converted, nil
	default:
		return 0, fmt.Errorf("value must be an integer")
	}
}

func syncLegacyAgentStringField(m map[string]any, canonical, before, after string, canonicalize bool) {
	if before == after && !canonicalize {
		return
	}
	deleteLegacyAgentFieldAliases(m, canonical)
	if after != "" {
		m[canonical] = after
	}
}

func syncLegacyAgentTrustedStringField(m map[string]any, canonical, before, after string, canonicalize bool) {
	if before == after && !canonicalize {
		return
	}
	deleteLegacyAgentFieldAliases(m, canonical)
	m[canonical] = after
}

func syncLegacyAgentIntField(m map[string]any, canonical string, before, after int, canonicalize bool) {
	if before == after && !canonicalize {
		return
	}
	deleteLegacyAgentFieldAliases(m, canonical)
	if after != 0 {
		m[canonical] = int64(after)
	}
}

func syncLegacyAgentBoolField(m map[string]any, canonical string, before, after bool, canonicalize bool) {
	if before == after && !canonicalize {
		return
	}
	deleteLegacyAgentFieldAliases(m, canonical)
	m[canonical] = after
}

func deleteLegacyAgentFieldAliases(m map[string]any, canonical string) {
	for _, key := range caseFoldedMapKeys(m, canonical) {
		delete(m, key)
	}
}

// validateNeverApproveMinSeverity bounds never_approve_min_severity to the
// canonical severities at every scope. Empty means "inherit" (repo/org) or
// "low" (global), so it is always accepted.
func (c *Config) validateNeverApproveMinSeverity() error {
	check := func(scope, v string) error {
		switch v {
		case "", "low", "medium", "high":
			return nil
		default:
			return fmt.Errorf("config: %s.never_approve_min_severity must be one of: low, medium, high; got %q", scope, v)
		}
	}
	if err := check("ai", c.AI.NeverApproveMinSeverity); err != nil {
		return err
	}
	for org, o := range c.AI.Orgs {
		if err := check(fmt.Sprintf("ai.orgs[%s]", org), o.NeverApproveMinSeverity); err != nil {
			return err
		}
	}
	for repo, r := range c.AI.Repos {
		if err := check(fmt.Sprintf("ai.repos[%s]", repo), r.NeverApproveMinSeverity); err != nil {
			return err
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
	return validateIssueTrackingConfig("github.issue_tracking", it)
}

// validateIssueTracking enforces the small set of invariants the pipeline
// relies on: filter_mode and default_action must be from a known set. Labels
// themselves are free-form strings — intentionally — because GitHub allows
// almost anything in a label and we do not want to reject legitimate values.
// Silent fallbacks in applyDefaults mean the user almost never sees these
// errors; they exist so an explicit typo like filter_mode = "excluive" fails
// fast instead of defaulting silently.
func (c *Config) validateIssueTracking() error {
	return validateIssueTrackingConfig("github.issue_tracking", c.GitHub.IssueTracking)
}

func validateIssueTrackingConfig(path string, it IssueTrackingConfig) error {
	if !it.Enabled {
		return nil
	}
	switch it.FilterMode {
	case FilterModeExclusive, FilterModeInclusive:
	default:
		return fmt.Errorf("config: %s.filter_mode %q is invalid (must be %q or %q)", path, it.FilterMode, FilterModeExclusive, FilterModeInclusive)
	}
	switch IssueMode(it.DefaultAction) {
	case IssueModeIgnore, IssueModeReviewOnly:
	default:
		return fmt.Errorf("config: %s.default_action %q is invalid (must be %q or %q)", path, it.DefaultAction, IssueModeIgnore, IssueModeReviewOnly)
	}
	if len(it.BlockedLabels) > 0 && it.ResolvePromoteToLabel() == "" {
		return fmt.Errorf("config: %s.blocked_labels set but no promote target — set promote_to_label or populate develop_labels", path)
	}
	return nil
}

func (c *Config) validateScopedIssueTracking() error {
	for org, o := range c.AI.Orgs {
		if o.IssueTracking == nil {
			continue
		}
		it := c.GitHub.IssueTracking
		applyIssueTrackingOverride(&it, o.IssueTracking)
		if err := validateIssueTrackingConfig(fmt.Sprintf("ai.orgs.%q.issue_tracking", org), it); err != nil {
			return err
		}
	}
	for repo, r := range c.AI.Repos {
		it := c.GitHub.IssueTracking
		if org := repoOrg(repo); org != "" && c.AI.Orgs != nil {
			if o, ok := c.AI.Orgs[org]; ok {
				applyIssueTrackingOverride(&it, o.IssueTracking)
			}
		}
		applyIssueTrackingOverride(&it, r.IssueTracking)
		if err := validateIssueTrackingConfig(fmt.Sprintf("ai.repos.%q.issue_tracking", repo), it); err != nil {
			return err
		}
	}
	return nil
}

// ValidateOrgSlug validates a GitHub org/user slug used as an [ai.orgs] key.
func ValidateOrgSlug(org string) error {
	if !githubOrgPattern.MatchString(org) {
		return fmt.Errorf("config: org %q is invalid (must match GitHub org/user slug: 1-39 alphanumerics plus internal hyphens)", org)
	}
	return nil
}

// ValidateRepoSlug validates a GitHub "owner/name" repo slug used as an
// [ai.repos] key. It enforces exactly one slash separating a valid org/user
// owner (see ValidateOrgSlug) from a valid repository name. Validating this
// defensively prevents a malformed key — e.g. an empty owner, embedded path
// separators, or "." / ".." traversal tokens — from being written verbatim
// into the config or interpolated into GitHub API paths.
func ValidateRepoSlug(repo string) error {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("config: repo %q is invalid (must be in owner/name form)", repo)
	}
	if err := ValidateOrgSlug(parts[0]); err != nil {
		return err
	}
	name := parts[1]
	if name == "." || name == ".." || !repoNamePattern.MatchString(name) {
		return fmt.Errorf("config: repo name %q is invalid (allowed: alphanumerics, '.', '_', '-')", name)
	}
	return nil
}

func (c *Config) validateOrgKeys() error {
	for org := range c.AI.Orgs {
		if err := ValidateOrgSlug(org); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateRefinementTimeouts() error {
	if err := validatePositiveDuration("ai.refinement_timeout", c.AI.RefinementTimeout); err != nil {
		return err
	}
	for org, ai := range c.AI.Orgs {
		path := fmt.Sprintf(`ai.orgs.%q.refinement_timeout`, org)
		if err := validatePositiveDuration(path, ai.RefinementTimeout); err != nil {
			return err
		}
	}
	for repo, ai := range c.AI.Repos {
		path := fmt.Sprintf(`ai.repos.%q.refinement_timeout`, repo)
		if err := validatePositiveDuration(path, ai.RefinementTimeout); err != nil {
			return err
		}
	}
	return nil
}

func validatePositiveDuration(path, raw string) error {
	if raw == "" {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("config: %s %q is invalid: %w", path, raw, err)
	}
	if d <= 0 {
		return fmt.Errorf("config: %s %q must be positive", path, raw)
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
	// Decode through a generic map first. BurntSushi's struct decoder matches
	// TOML keys case-insensitively, so aliases that differ only in casing can
	// otherwise race on Go map iteration. Project only schema-known fields
	// under canonical names before the typed decode. Unknown TOML remains
	// accepted and never passes through the encoder, which cannot re-emit
	// every valid value its decoder accepts (for example mixed-type arrays).
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	known, err := projectKnownConfigMap(raw)
	if err != nil {
		return nil, fmt.Errorf("config: canonicalize %s: %w", path, err)
	}
	var canonical strings.Builder
	if err := toml.NewEncoder(&canonical).Encode(known); err != nil {
		return nil, fmt.Errorf("config: canonicalize %s: %w", path, err)
	}
	var cfg Config
	if err := toml.Unmarshal([]byte(canonical.String()), &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	cfg.applyEnvOverrides()
	cfg.sanitizeLegacyAgentExecutionPolicy("toml/env")
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
	cfg.sanitizeLegacyAgentExecutionPolicy("generated env")
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

// CircuitBreakerForRepo resolves circuit-breaker caps for a repo through
// three levels: per-repo > per-org > global. A nil override at a level is
// skipped; present override fields overlay onto the inherited value.
func (c *Config) CircuitBreakerForRepo(repo string) CircuitBreakerConfig {
	out := c.CircuitBreaker
	if org := repoOrg(repo); org != "" && c.AI.Orgs != nil {
		if o, ok := c.AI.Orgs[org]; ok && o.CircuitBreaker != nil {
			out = mergeCircuitBreaker(out, *o.CircuitBreaker)
		}
	}
	if c.AI.Repos != nil {
		if r, ok := c.AI.Repos[repo]; ok && r.CircuitBreaker != nil {
			out = mergeCircuitBreaker(out, *r.CircuitBreaker)
		}
	}
	return out
}

// mergeCircuitBreaker overlays non-zero fields of override onto base, so an
// org/repo can tune a single axis without restating the rest.
func mergeCircuitBreaker(base, override CircuitBreakerConfig) CircuitBreakerConfig {
	if override.PerPR24h != 0 {
		base.PerPR24h = override.PerPR24h
	}
	if override.PerRepoHr != 0 {
		base.PerRepoHr = override.PerRepoHr
	}
	if override.PerIssue24h != 0 {
		base.PerIssue24h = override.PerIssue24h
	}
	if override.PerIssueRepoHr != 0 {
		base.PerIssueRepoHr = override.PerIssueRepoHr
	}
	if override.PerImplRepoHr != 0 {
		base.PerImplRepoHr = override.PerImplRepoHr
	}
	return base
}
