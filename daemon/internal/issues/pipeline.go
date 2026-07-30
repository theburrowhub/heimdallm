// Package issues hosts the fase-2 issue-tracking pipeline — one step per
// issue triage plus a fetcher that orchestrates batches of issues from a
// repo. This pipeline runs the review_only mode (LLM triage + GitHub
// comment). The auto_implement mode lives in a sibling file shipped with
// issue #27.
package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// ErrCircuitBreakerTripped is returned by Run when a triage was skipped
// because the per-issue or per-repo cap was exceeded. Mirrors the PR-side
// error in the pipeline package; callers detect it via errors.As on a
// *CircuitBreakerError value to extract the human-readable reason, or via
// errors.Is(err, ErrCircuitBreakerTripped) when the reason is not needed.
// See theburrowhub/heimdallm#292.
var ErrCircuitBreakerTripped = errors.New("issues pipeline: circuit breaker tripped")

// CircuitBreakerError wraps ErrCircuitBreakerTripped with the specific
// reason the breaker returned. Use errors.As on this type to read Reason
// without parsing the error string.
type CircuitBreakerError struct {
	Reason string
}

func (e *CircuitBreakerError) Error() string {
	return ErrCircuitBreakerTripped.Error() + ": " + e.Reason
}

func (e *CircuitBreakerError) Unwrap() error { return ErrCircuitBreakerTripped }

// maxTitleBytes bounds the length of issue titles that get interpolated into
// commit messages and PR title / body. Long titles turn into unwieldy
// multi-line messages; more importantly, sanitizeTitle strips CR / LF so a
// crafted title cannot inject fake git trailers (Co-Authored-By, etc.).
const maxTitleBytes = 120

// sanitizeTitle cleans issue.Title for interpolation into commit messages
// and PR metadata: newlines and carriage returns are replaced with a space
// (to defuse trailer injection) and the result is rune-truncated to
// maxTitleBytes so a verbose title does not blow up the commit message.
func sanitizeTitle(s string) string {
	cleaned := strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(s))
	if len(cleaned) <= maxTitleBytes {
		return cleaned
	}
	// Walk back to the nearest rune start so we never split a multi-byte
	// character when truncating.
	i := maxTitleBytes
	for i > 0 {
		r := cleaned[i]
		// UTF-8: bytes with high bits 10xxxxxx are continuation bytes.
		if r < 0x80 || r >= 0xC0 {
			break
		}
		i--
	}
	return cleaned[:i] + "…"
}

// IssueCommenter posts a comment on an issue. Same method GitHub exposes for
// PR comments — both routes share `/repos/{owner}/{repo}/issues/{n}/comments`.
type IssueCommenter interface {
	PostComment(repo string, number int, body string) (time.Time, error)
}

// IssueCommentFetcher fetches the existing discussion for an issue so the
// triage LLM can take prior context into account.
//
// The method is `FetchIssueCommentsOnly`, not the generic `FetchComments`
// that PR callers use — on an issue number, FetchComments hits the
// PR-only `/pulls/:n/comments` endpoint and always 404s, which caused
// every issue triage to silently run without prior comment context (bug
// #292).
type IssueCommentFetcher interface {
	FetchIssueCommentsOnly(repo string, number int) ([]github.Comment, error)
}

// DefaultBrancher returns the GitHub repository's default branch. Used by
// the auto_implement pipeline (#27) to base the work branch on the right
// trunk.
type DefaultBrancher interface {
	GetDefaultBranch(repo string) (string, error)
}

// PRCreator opens a pull request — the last external step of the
// auto_implement flow before the review is persisted.
type PRCreator interface {
	CreatePR(repo, title, body, head, base string, draft bool) (*github.CreatedPR, error)
}

// PRMetadataApplier sets reviewers, labels, and assignees on a PR after creation.
type PRMetadataApplier interface {
	SetPRReviewers(repo string, prNumber int, reviewers []string) error
	AddLabels(repo string, number int, labels []string) error
	SetAssignees(repo string, number int, assignees []string) error
}

// IssueGetter fetches a single issue by repo + number. Used by the
// auto_implement pre-push state check to verify the issue is still open
// before creating a PR (#238).
type IssueGetter interface {
	GetIssue(repo string, number int) (*github.Issue, error)
}

// CLIExecutor runs an AI CLI. The pipeline uses ExecuteRaw because the
// triage schema (Triage object) differs from the PR-review schema.
type CLIExecutor interface {
	Detect(primary, fallback string) (string, error)
	ExecuteRaw(cli, prompt string, opts executor.ExecOptions) ([]byte, error)
}

// Publisher is the subset of sse.Broker used for emitting events.
type Publisher interface {
	Publish(e sse.Event)
}

// Notifier sends desktop / system notifications.
type Notifier interface {
	Notify(title, message string)
}

// Triage is the structured triage block returned by the LLM.
type Triage struct {
	Severity             string   `json:"severity"`
	Category             string   `json:"category"`
	AffectedArea         string   `json:"affected_area,omitempty"`
	AffectedPaths        []string `json:"affected_paths,omitempty"`
	PriorityLabel        string   `json:"priority_label,omitempty"`
	SuggestedAssignee    string   `json:"suggested_assignee"`
	AssignedAssignee     string   `json:"assigned_assignee,omitempty"`
	AssigneeReason       string   `json:"assignee_reason,omitempty"`
	AssigneeConfidence   string   `json:"assignee_confidence,omitempty"`
	AssigneeEvidence     []string `json:"assignee_evidence,omitempty"`
	AssignmentSource     string   `json:"assignment_source,omitempty"`
	TentativeAssignee    string   `json:"tentative_assignee,omitempty"`
	AssignmentDiagnostic string   `json:"assignment_diagnostic,omitempty"`
}

// IssueReviewResult is the parsed LLM output for a triage run. Mirrors the
// schema advertised in the prompt template.
type IssueReviewResult struct {
	Summary   string   `json:"summary"`
	Triage    Triage   `json:"triage"`
	NextSteps []string `json:"next_steps"`
	Severity  string   `json:"severity"`
}

// PRDescription is the parsed LLM output for a PR description generation call.
type PRDescription struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// RefinementResult is the structured deep-investigation output produced by
// the refinement stage before development.
type RefinementResult struct {
	AnalysisSummary     string              `json:"analysis_summary"`
	AffectedAreas       []RefinementArea    `json:"affected_areas"`
	Subtasks            []RefinementSubtask `json:"subtasks"`
	ImplementationOrder []string            `json:"implementation_order"`
	Assumptions         []string            `json:"assumptions"`
	OpenQuestions       []string            `json:"open_questions"`
	Risks               []string            `json:"risks"`
	TestPlan            []string            `json:"test_plan"`
}

type RefinementArea struct {
	Path    string   `json:"path"`
	Symbols []string `json:"symbols"`
	Reason  string   `json:"reason"`
}

type RefinementSubtask struct {
	ID             string   `json:"id"`
	Description    string   `json:"description"`
	AffectedFiles  []string `json:"affected_files"`
	Symbols        []string `json:"symbols"`
	ExpectedChange string   `json:"expected_change"`
	Complexity     string   `json:"complexity"`
	Dependencies   []string `json:"dependencies"`
}

// RunOptions carries per-execution settings derived from global + repo +
// agent config by the caller.
//
// ExecOpts.WorkDir is the per-execution isolated snapshot prepared by
// repoctx. A configured local_dir is only a read-only source and must never be
// assigned here directly. The single resolved snapshot field drives both the
// mode downgrade (develop → review_only when absent) and prompt context, so
// they cannot disagree.
//
// GitHubToken is required for the auto_implement path (git push). It is not
// consulted in review_only runs, which is why it lives here rather than in
// the Pipeline itself — the token belongs to the caller and may rotate.
type RunOptions struct {
	Primary     string
	Fallback    string
	ExecOpts    executor.ExecOptions
	GitHubToken string

	// Issue triage prompt customization (resolved by caller from agent profiles).
	// Priority: IssuePromptOverride (full template) > IssueInstructions (injected into default) > built-in default.
	IssuePromptOverride string // full custom template from repo-level agent
	IssueInstructions   string // plain text injected into default template
	TriageOwner         string // fallback issue assignee resolved repo > org > global

	// Auto_implement prompt customization (same resolution shape as the
	// triage pair above, but consulted only on the runAutoImplement path).
	// Priority: ImplementPromptOverride > ImplementInstructions > built-in default.
	ImplementPromptOverride string
	ImplementInstructions   string

	// PR creation metadata (applied after auto_implement creates a PR).
	PRReviewers []string
	PRAssignee  string
	PRLabels    []string
	PRDraft     bool

	// AuthUser is the daemon's cached GitHub login. It is used as the
	// PR author fallback when the create-PR response omits user.login so
	// the stored row never falls back to the issue reporter (#456).
	AuthUser string

	// GeneratePRDescription enables a second LLM call after commit to
	// produce a rich PR title and description from the implementation diff.
	// When false (default), the pipeline uses the template strings.
	GeneratePRDescription bool

	// Force bypasses refinement idempotency for manual reruns.
	Force bool

	// RequireWorkDirForDevelop turns the historical develop→review_only
	// fallback into a hard error. Production auto-clone callers set this so
	// an auto_implement run never silently proceeds without a writable checkout.
	RequireWorkDirForDevelop bool

	// RequireWorkDirForRefinement records the production contract explicitly:
	// refinement needs full repository context and must not degrade to issue-only.
	RequireWorkDirForRefinement bool

	// ReleaseRepoContext releases a checkout handle acquired by the caller.
	// It is intentionally a one-shot cleanup hook rather than another source
	// of path truth; ExecOpts.WorkDir remains authoritative.
	ReleaseRepoContext func()

	// InFlightSalt, when non-empty, is appended to the diagnostic in-flight
	// claim key (stored in issue_triage_in_flight.updated_at) so the autonomous
	// chain can tag which stage kicked off a run without mutating the persisted
	// issue snapshot. Note the store's single-flight contention is keyed on
	// issue_id ALONE (ClaimIssueTriageInFlight / #458), so the salt does NOT
	// change collapse semantics — it is diagnostic only. Default (empty) leaves
	// the key purely issue.UpdatedAt, preserving existing caller behaviour.
	InFlightSalt string
}

// Pipeline runs a single issue triage or implementation end-to-end.
type Pipeline struct {
	store      issueStore
	gh         issueGitHub
	executor   CLIExecutor
	git        GitOps
	broker     Publisher
	notify     Notifier
	botLoginMu sync.RWMutex
	botLogin   string

	// breaker caps the number of triages per issue and per repo. Nil
	// disables both axes (no limit). Configure at startup via
	// SetCircuitBreakerLimits.
	breaker *store.IssueCircuitBreakerLimits

	// watch enrols freshly-created auto_implement PRs into Tier 3 so
	// the new review-state checker observes external reviews on them
	// (#482). Nil-safe: single-tier setups (some tests) leave it unset.
	watch WatchEnroller
}

// SetWatchEnroller wires the Tier 3 watch enroller so auto_implement
// PRs are picked up by the review-state checker right after creation
// (#482). Optional — the pipeline checks for nil before calling.
func (p *Pipeline) SetWatchEnroller(w WatchEnroller) { p.watch = w }

// SetBotLogin sets the GitHub login of the bot account. Used to filter
// the bot's own comments from the "new discussion" section in re-triages.
func (p *Pipeline) SetBotLogin(login string) {
	if p == nil {
		return
	}
	p.botLoginMu.Lock()
	p.botLogin = strings.TrimSpace(login)
	p.botLoginMu.Unlock()
}

func (p *Pipeline) currentBotLogin() string {
	if p == nil {
		return ""
	}
	p.botLoginMu.RLock()
	defer p.botLoginMu.RUnlock()
	return p.botLogin
}

// SetCircuitBreakerLimits enables the per-issue and per-repo triage
// caps. Nil disables both axes; zero values within a non-nil struct
// disable only that axis.
func (p *Pipeline) SetCircuitBreakerLimits(limits *store.IssueCircuitBreakerLimits) {
	p.breaker = limits
}

// issueStore is the subset of *store.Store the pipeline needs. Kept narrow
// so tests can substitute a fake without bringing in SQLite.
//
// ClaimIssueTriageInFlight / ReleaseIssueTriageInFlight gate Run on a
// single-flight-per-issue key (github_issue_id only). updated_at is
// passed for diagnostics but is not part of the contention key — that
// design was relaxed in #458 because the bot's own triage comment bumps
// updated_at, which previously let a second snapshot pass the claim and
// spawn a duplicate run. Mirrors the PR-side claim (#258); see #292.
type issueStore interface {
	UpsertIssue(i *store.Issue) (int64, error)
	InsertIssueReview(r *store.IssueReview) (int64, error)
	LatestIssueReview(issueID int64) (*store.IssueReview, error)
	LatestIssueReviewByAction(issueID int64, action string) (*store.IssueReview, error)
	UpsertPR(pr *store.PR) (int64, error)
	// MarkPRAutoImplementOrigin back-links the freshly-created PR row
	// to the issue's store row id. Tier 3 keys its review-state
	// vigilance on a non-zero value here (#482).
	MarkPRAutoImplementOrigin(prID, issueID int64) error
	ClaimIssueTriageInFlight(issueID int64, updatedAt string) (bool, error)
	ReleaseIssueTriageInFlight(issueID int64, updatedAt string) error
	CheckIssueCircuitBreaker(issueID int64, repo string, cfg store.IssueCircuitBreakerLimits) (bool, string, error)
}

// WatchEnroller enrols an item (PR or issue) into the Tier 3 watch
// bucket so the state monitor periodically refreshes it (#482). The
// pipeline calls this after auto_implement creates a PR so the new
// PR-review-state CheckItem branch picks it up. Wired via
// SetWatchEnroller; nil-safe — single-tier setups (some tests) leave
// it unset.
type WatchEnroller interface {
	Enroll(ctx context.Context, kind, repo string, number int, githubID int64) error
}

// issueGitHub groups every GitHub-facing method the pipeline uses. The
// review_only flow only needs IssueCommenter + IssueCommentFetcher; the
// auto_implement flow additionally needs DefaultBrancher + PRCreator. A
// single fat interface is simpler than juggling two at the caller — the
// real *github.Client implements all four trivially.
type issueGitHub interface {
	IssueCommenter
	IssueCommentFetcher
	DefaultBrancher
	PRCreator
	PRMetadataApplier
	IssueGetter
}

// New wires the pipeline. All dependencies are interfaces so tests can
// inject fakes. `git` may be nil when the caller is sure no auto_implement
// run will happen (e.g. unit tests that only exercise review_only); the
// pipeline guards the nil before any git operation.
func New(s issueStore, gh issueGitHub, exec CLIExecutor, git GitOps, broker Publisher, n Notifier) *Pipeline {
	return &Pipeline{store: s, gh: gh, executor: exec, git: git, broker: broker, notify: n}
}

// Run processes one classified issue and returns the persisted review. The
// returned IssueReview's ActionTaken reflects the mode that actually ran —
// a develop-classified issue that loses its local_dir is persisted as
// "review_only", and an auto_implement run whose agent made no changes is
// downgraded to review_only with an explanatory comment.
//
// Run is the single entry point; it decides the mode and delegates to
// runReviewOnly or runAutoImplement so each flow stays readable. The caller
// passes a context so long-running network operations (git fetch / push,
// CLI invocation) can be cancelled on daemon shutdown.
func (p *Pipeline) Run(ctx context.Context, issue *github.Issue, opts RunOptions) (*store.IssueReview, error) {
	if opts.ReleaseRepoContext != nil {
		defer opts.ReleaseRepoContext()
	}
	if issue == nil {
		return nil, fmt.Errorf("issues pipeline: nil issue")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Single-flight in-flight claim keyed on github_issue_id (#458). Any
	// poller tick that arrives while a run is in progress collapses to a
	// no-op, regardless of whether the issue's updated_at changed mid-
	// flight (which the bot's own PostComment does at the tail of the
	// run). Fail-open on any claim error — the downstream circuit breaker
	// and marker-scan dedup still cap cost. Empty key is treated as "no
	// claim possible"; the scheduler should have prevented that but the
	// guard is cheap. See theburrowhub/heimdallm#292.
	//
	// Key-space note: the claim uses issue.ID (the GitHub-assigned ID,
	// stable and known before any DB write) so we can gate Run before
	// the upsert. The circuit breaker further down uses the internal
	// store ID returned by UpsertIssue because issue_reviews.issue_id
	// references issues.id. The two key spaces serve different purposes
	// (snapshot dedup vs historical count) and are intentionally
	// distinct — do not "unify" them without revisiting the
	// claim-before-upsert ordering that gives Run an early exit.
	var (
		claimed      bool
		breakerHeld  bool // when true, defer must NOT release the claim
		claimKey     string
		claimIssueID = issue.ID
	)
	if !issue.UpdatedAt.IsZero() && claimIssueID != 0 {
		claimKey = issue.UpdatedAt.UTC().Format(time.RFC3339)
		// An optional salt distinguishes consecutive same-snapshot stage runs
		// (autonomous chain) without mutating the persisted issue snapshot.
		if opts.InFlightSalt != "" {
			claimKey += "|" + opts.InFlightSalt
		}
		ok, err := p.store.ClaimIssueTriageInFlight(claimIssueID, claimKey)
		if err != nil {
			// Fail-open: if the INSERT actually landed but the driver
			// surfaced an error reading RowsAffected, the row will leak
			// until ClearStaleIssueTriageInFlight (30 min sweep) reclaims
			// it. Acceptable: the alternative (assume it landed and
			// release in defer) risks releasing a row another daemon
			// process holds.
			slog.Warn("issues pipeline: claim inflight failed, proceeding",
				"repo", issue.Repo, "number", issue.Number, "err", err)
		} else if !ok {
			slog.Info("issues pipeline: already in flight, skipping",
				"repo", issue.Repo, "number", issue.Number, "updated_at", claimKey)
			return nil, nil
		} else {
			claimed = true
		}
	}
	defer func() {
		// Release on every path EXCEPT a circuit-breaker trip. Holding
		// the claim across a trip prevents the next fetcher tick from
		// re-acquiring, re-hitting the breaker, and re-firing the
		// operator notification once per poll.
		//
		// Behavior change under single-flight-per-issue (#458): the hold
		// is now sticky for the full ClearStaleIssueTriageInFlight sweep
		// window (~30 min) — the previous "new updated_at bypasses the
		// held claim" path is gone. This is intentional and conservative
		// for cost-runaway protection: the previous bypass let the same
		// issue re-trip the breaker on every new activity. Operator-
		// driven retries (retry-marker comments, label flips) take effect
		// after the sweep clears the held row. If a tighter manual
		// unblock is needed, file a follow-up — possibly an explicit
		// force-release path for retry markers.
		if claimed && !breakerHeld {
			if err := p.store.ReleaseIssueTriageInFlight(claimIssueID, claimKey); err != nil {
				slog.Warn("issues pipeline: release inflight failed",
					"issue_id", claimIssueID, "updated_at", claimKey, "err", err)
			}
		}
	}()

	// Determine the effective mode. `ExecOpts.WorkDir` is the single source
	// of truth for "is there a local checkout"; Run does not consult any
	// other field.
	workDir := strings.TrimSpace(opts.ExecOpts.WorkDir)
	effective := issue.Mode
	if effective == config.IssueModeDevelop && workDir == "" {
		if opts.RequireWorkDirForDevelop {
			return nil, fmt.Errorf("issues pipeline: develop mode requires local repo context")
		}
		slog.Warn("issues pipeline: develop mode requires local_dir, downgrading to review_only",
			"repo", issue.Repo, "issue", issue.Number)
		effective = config.IssueModeReviewOnly
	}
	if effective == config.IssueModeRefinement && workDir == "" {
		return nil, fmt.Errorf("issues pipeline: refinement mode requires local repo context")
	}
	if effective == config.IssueModeIgnore {
		return nil, fmt.Errorf("issues pipeline: refusing an ignore-classified issue (fetcher should have filtered it out)")
	}

	// Upsert + initial SSE events are common to all flows so we do them
	// here. issue_detected fires before the flow forks, issue_review_started
	// fires after so the UI can show the correct "triaging" vs "implementing"
	// copy — the runner sets the exact flavour it wants.
	//
	// For triage runs, upsert runs BEFORE the circuit breaker so its per-issue
	// count (which keys on the internal store ID via issue_reviews.issue_id)
	// sees the correct row. The upsert is idempotent — on a breaker-trip
	// the issue row stays but no issue_reviews row is written for this
	// attempt, which matches the PR-side behaviour.
	storeIssue, err := IssueToStoreRow(issue)
	if err != nil {
		return nil, err
	}
	issueID, err := p.store.UpsertIssue(storeIssue)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: upsert issue: %w", err)
	}

	// Circuit breaker: hard cap on triage count per issue / per repo. It must
	// not gate refinement or develop, otherwise a correctly promoted issue can
	// get stuck after earlier triage/refinement activity.
	// Runs AFTER the in-flight claim and upsert so it only fires when
	// both dedup layers missed; returns *CircuitBreakerError so the
	// caller (fetcher) can distinguish it from a genuine pipeline
	// failure. See theburrowhub/heimdallm#292.
	if p.breaker != nil && effective == config.IssueModeReviewOnly {
		tripped, reason, err := p.store.CheckIssueCircuitBreaker(issueID, issue.Repo, *p.breaker)
		if err != nil {
			slog.Warn("issues pipeline: circuit breaker check failed, proceeding",
				"repo", issue.Repo, "number", issue.Number, "err", err)
		} else if tripped {
			slog.Error("issues pipeline: CIRCUIT BREAKER TRIPPED — skipping triage",
				"repo", issue.Repo, "number", issue.Number, "reason", reason)
			if p.notify != nil {
				p.notify.Notify("Heimdallm issue circuit breaker",
					fmt.Sprintf("%s #%d: %s", issue.Repo, issue.Number, reason))
			}
			// Hold the claim so the operator notification is not
			// repeated on every subsequent poll for the same snapshot.
			breakerHeld = true
			return nil, &CircuitBreakerError{Reason: reason}
		}
	}

	p.publish(sse.EventIssueDetected, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo,
	})

	switch effective {
	case config.IssueModeReviewOnly:
		return p.runReviewOnly(ctx, issue, issueID, workDir, opts)
	case config.IssueModeRefinement:
		return p.runRefinement(ctx, issue, issueID, workDir, opts)
	case config.IssueModeDevelop:
		return p.runAutoImplement(ctx, issue, issueID, workDir, opts)
	default:
		return nil, fmt.Errorf("issues pipeline: unknown effective mode %q", effective)
	}
}

// runReviewOnly posts a triage comment and persists the review. Shared
// upsert + issue_detected event have already been done by Run. The ctx
// parameter is not yet passed through to executor/gh — those dependencies
// don't accept one — but it is plumbed here so the method signature stays
// in lockstep with runAutoImplement and ready for the day they do.
func (p *Pipeline) runReviewOnly(ctx context.Context, issue *github.Issue, issueID int64, workDir string, opts RunOptions) (*store.IssueReview, error) {
	_ = ctx // reserved for executor/gh cancellation when those deps accept one
	p.publish(sse.EventIssueReviewStarted, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo, "mode": "review_only",
	})
	if p.notify != nil {
		p.notify.Notify("Issue Triage Started", fmt.Sprintf("%s #%d", issue.Repo, issue.Number))
	}

	// Pull existing discussion as additional context. Failure is non-fatal —
	// the triage still runs with title + body alone.
	comments, err := p.gh.FetchIssueCommentsOnly(issue.Repo, issue.Number)
	if err != nil {
		slog.Warn("issues pipeline: failed to fetch comments, proceeding without", "err", err)
		comments = nil
	}
	botLogin := p.currentBotLogin()

	// Build re-triage context if a previous review exists for this issue.
	var triageCtx string
	prevReview, _ := p.store.LatestIssueReview(issueID)
	if prevReview != nil {
		triageCtx = buildTriageContext(
			prevReview.Triage,
			prevReview.NextSteps,
			prevReview.Summary,
			extractSeverity(prevReview.Triage),
			prevReview.CreatedAt,
			comments,
			botLogin,
		)
	}

	// Filter out the bot's own comments so the LLM doesn't see its own
	// previous output as "discussion" (confuses re-triage context).
	var humanComments []github.Comment
	for _, c := range comments {
		if botLogin != "" && strings.EqualFold(c.Author, botLogin) {
			continue
		}
		humanComments = append(humanComments, c)
	}

	// Build prompt + run the CLI. HasLocalDir mirrors workDir above so the
	// LLM hears the same story as the mode-selection logic.
	// Agent profile customization: IssuePromptOverride replaces the entire
	// template; IssueInstructions injects into the default template.
	promptCtx := PromptContext{
		Repo:          issue.Repo,
		Number:        issue.Number,
		Title:         issue.Title,
		Author:        issue.User.Login,
		Labels:        issue.LabelNames(),
		Assignees:     issue.AssigneeLogins(),
		Body:          issue.Body,
		Comments:      humanComments,
		HasLocalDir:   workDir != "",
		TriageContext: triageCtx,
		TriageOwner:   opts.TriageOwner,
	}
	prompt := BuildPromptWithProfile(promptCtx, opts.IssuePromptOverride, opts.IssueInstructions)

	cli, err := p.executor.Detect(opts.Primary, opts.Fallback)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("detect CLI: %w", err))
		return nil, fmt.Errorf("issues pipeline: detect CLI: %w", err)
	}
	execOpts := executor.OptionsForSelectedCLI(opts.Primary, cli, opts.ExecOpts)
	raw, err := p.executor.ExecuteRaw(cli, prompt, execOpts)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("execute %s: %w", cli, err))
		return nil, fmt.Errorf("issues pipeline: execute %s: %w", cli, err)
	}
	result, err := parseIssueResult(raw)
	if err != nil {
		p.publishError(issueID, issue, err)
		return nil, fmt.Errorf("issues pipeline: parse result: %w", err)
	}
	enrichTriageResult(ctx, result, workDir, opts.TriageOwner, defaultGitHistoryRunner{})

	// Build + post the Markdown comment. PostComment failure is not fatal —
	// the review is still persisted locally with a zero pr_created so
	// operators can re-drive it manually without losing the LLM output.
	body := BuildMarkdownComment(result)
	commentedAt, postErr := p.gh.PostComment(issue.Repo, issue.Number, body)
	if postErr != nil {
		slog.Warn("issues pipeline: PostComment failed, review will be stored locally only",
			"repo", issue.Repo, "number", issue.Number, "err", postErr)
	}
	applyIssueTriageMetadata(p.gh, issue.Repo, issue.Number, result)

	triageJSON, err := json.Marshal(result.Triage)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: marshal triage: %w", err)
	}
	nextStepsJSON, err := json.Marshal(result.NextSteps)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: marshal next_steps: %w", err)
	}
	rev := &store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     cli,
		Summary:     result.Summary,
		Triage:      string(triageJSON),
		NextSteps:   string(nextStepsJSON),
		ActionTaken: string(config.IssueModeReviewOnly),
		CreatedAt:   time.Now().UTC(),
		CommentedAt: commentedAt,
	}
	revID, err := p.store.InsertIssueReview(rev)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: store review: %w", err)
	}
	rev.ID = revID

	p.publish(sse.EventIssueReviewCompleted, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo,
		"severity": result.Severity, "post_ok": postErr == nil,
	})
	if p.notify != nil {
		p.notify.Notify("Issue Triage Complete",
			fmt.Sprintf("%s #%d — severity: %s", issue.Repo, issue.Number, result.Severity))
	}
	slog.Info("issues pipeline: triage complete",
		"repo", issue.Repo, "number", issue.Number,
		"severity", result.Severity, "posted", postErr == nil)
	return rev, nil
}

// runAutoImplement creates a branch, asks the agent to implement the issue,
// commits + pushes whatever it changed, opens a PR, and persists the review.
// When the agent produces no changes the run degrades to review_only with an
// explanatory comment rather than opening an empty PR.
// On a Push-succeeded-but-CreatePR-failed path the orphaned remote branch is
// cleaned up so the re-run starts from a clean remote.
func (p *Pipeline) runAutoImplement(ctx context.Context, issue *github.Issue, issueID int64, workDir string, opts RunOptions) (*store.IssueReview, error) {
	if p.git == nil {
		p.publishError(issueID, issue, fmt.Errorf("git dependency not wired"))
		return nil, fmt.Errorf("issues pipeline: auto_implement requires a GitOps dep")
	}
	if opts.GitHubToken == "" {
		p.publishError(issueID, issue, fmt.Errorf("auto_implement requires a GitHub token"))
		return nil, fmt.Errorf("issues pipeline: auto_implement: empty GitHubToken")
	}

	p.publish(sse.EventIssueReviewStarted, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo, "mode": "auto_implement",
	})
	if p.notify != nil {
		p.notify.Notify("Issue Auto-Implement Started", fmt.Sprintf("%s #%d", issue.Repo, issue.Number))
	}

	// Sanitize the title once at the top — every commit / PR string derives
	// from this value. Keeps trailer-injection attempts and runaway-length
	// titles out of git history and PR metadata.
	safeTitle := sanitizeTitle(issue.Title)

	// Resolve the default branch first so we fail fast on a bad token / repo
	// name before the CLI burns a turn on the prompt.
	base, err := p.gh.GetDefaultBranch(issue.Repo)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("default branch: %w", err))
		return nil, fmt.Errorf("issues pipeline: get default branch: %w", err)
	}

	branch := fmt.Sprintf("heimdallm/issue-%d", issue.Number)
	if err := p.git.CheckoutNewBranch(ctx, workDir, issue.Repo, branch, base, opts.GitHubToken); err != nil {
		p.publishError(issueID, issue, fmt.Errorf("checkout: %w", err))
		return nil, fmt.Errorf("issues pipeline: checkout: %w", err)
	}

	// Fetch comments once so the implement prompt carries the same context
	// the triage path would see. Best-effort as before.
	comments, err := p.gh.FetchIssueCommentsOnly(issue.Repo, issue.Number)
	if err != nil {
		slog.Warn("issues pipeline: failed to fetch comments, proceeding without", "err", err)
		comments = nil
	}
	botLogin := p.currentBotLogin()

	cli, err := p.executor.Detect(opts.Primary, opts.Fallback)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("detect CLI: %w", err))
		return nil, fmt.Errorf("issues pipeline: detect CLI: %w", err)
	}
	opts.ExecOpts = executor.OptionsForSelectedCLI(opts.Primary, cli, opts.ExecOpts)

	// Auto_implement requires the CLI to be allowed to write files. When the
	// operator has not configured a permission/approval flag explicitly, fall
	// back to the safest mode that still permits edits — otherwise the agent
	// silently completes without touching the tree and every run degrades to
	// the no-changes fallback (#433).
	opts.ExecOpts = ensureAutoImplementWritePerms(cli, opts.ExecOpts)

	// Build prior-issue context. Prefer the latest refinement plan when it
	// exists so auto_implement can execute the researched plan; otherwise fall
	// back to the historical triage context.
	var triageCtx string
	prevRefinement, _ := p.store.LatestIssueReviewByAction(issueID, string(config.IssueModeRefinement))
	if prevRefinement != nil {
		triageCtx = buildIssueRunContext(prevRefinement, comments, botLogin)
	} else if prevReview, _ := p.store.LatestIssueReview(issueID); prevReview != nil {
		triageCtx = buildIssueRunContext(prevReview, comments, botLogin)
	}

	// Agent profile customization: ImplementPromptOverride replaces the entire
	// template; ImplementInstructions injects into the default template.
	prompt := BuildImplementPromptWithProfile(
		PromptContext{
			Repo: issue.Repo, Number: issue.Number,
			Title: issue.Title, Author: issue.User.Login,
			Labels: issue.LabelNames(), Assignees: issue.AssigneeLogins(),
			Body: issue.Body, Comments: comments, HasLocalDir: true,
			TriageContext: triageCtx,
		},
		opts.ImplementPromptOverride,
		opts.ImplementInstructions,
	)
	if _, err := p.executor.ExecuteRaw(cli, prompt, opts.ExecOpts); err != nil {
		execErr := fmt.Errorf("execute %s: %w", cli, err)
		p.recordAutoImplementFailure(issueID, cli, fmt.Sprintf("auto_implement %s", execErr))
		p.publishError(issueID, issue, execErr)
		return nil, fmt.Errorf("issues pipeline: %w", execErr)
	}

	// If the agent produced no changes we do NOT open an empty PR. Fall back
	// to a review_only-style comment so the issue still gets acknowledged.
	changed, err := p.git.HasChanges(ctx, workDir)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("status: %w", err))
		return nil, fmt.Errorf("issues pipeline: git status: %w", err)
	}
	if !changed {
		return p.autoImplementNoChangesFallback(issue, issueID, cli)
	}

	commitMsg := fmt.Sprintf("feat: implement #%d — %s\n\nAuto-implemented by Heimdallm.\nCloses #%d",
		issue.Number, safeTitle, issue.Number)
	if err := p.git.CommitAll(ctx, workDir, commitMsg); err != nil {
		p.publishError(issueID, issue, fmt.Errorf("commit: %w", err))
		return nil, fmt.Errorf("issues pipeline: commit: %w", err)
	}

	// Pre-push state check (#238): verify the issue is still open. If it
	// was closed during implementation (e.g. another PR merged), abort to
	// avoid creating a duplicate PR. Non-fatal on API error — we proceed
	// with the push rather than block on a transient failure.
	freshIssue, stateErr := p.gh.GetIssue(issue.Repo, issue.Number)
	if stateErr != nil {
		slog.Warn("issues pipeline: pre-push state check failed, proceeding with push",
			"repo", issue.Repo, "number", issue.Number, "err", stateErr)
	} else if freshIssue.State != "open" {
		slog.Info("issues pipeline: issue closed during implementation, aborting",
			"repo", issue.Repo, "number", issue.Number, "state", freshIssue.State)
		return nil, nil
	}

	if err := p.git.Push(ctx, workDir, issue.Repo, branch, opts.GitHubToken); err != nil {
		// Record the push failure so the fetcher can enforce the
		// MaxAutoImplementFailures retry cap (#223). Without this row the
		// dedup logic would have no visibility into failed attempts and the
		// daemon would retry forever on non-fast-forward errors.
		p.recordAutoImplementFailure(issueID, cli, fmt.Sprintf("auto_implement push failed: %v", err))
		p.publishError(issueID, issue, fmt.Errorf("push: %w", err))
		return nil, fmt.Errorf("issues pipeline: push: %w", err)
	}

	prTitle := fmt.Sprintf("feat: implement #%d — %s", issue.Number, safeTitle)
	prBody := fmt.Sprintf("Auto-generated by Heimdallm in response to #%d.\n\nCloses #%d",
		issue.Number, issue.Number)

	if opts.GeneratePRDescription {
		desc, descErr := p.generatePRDescription(ctx, cli, issue, workDir, opts.ExecOpts)
		if descErr != nil {
			slog.Warn("issues pipeline: LLM PR description generation failed, using template",
				"repo", issue.Repo, "number", issue.Number, "err", descErr)
		} else {
			prTitle = sanitizeTitle(desc.Title)
			prBody = desc.Body + fmt.Sprintf("\n\nCloses #%d", issue.Number)
		}
	}

	createdPR, err := p.gh.CreatePR(issue.Repo, prTitle, prBody, branch, base, opts.PRDraft)
	if err != nil {
		// The branch is already live on the remote but has no PR attached.
		// Best-effort delete so a re-run does not trip over the stale ref —
		// a failure here is logged but not escalated (we are already on the
		// error path; do not mask the real cause).
		if delErr := p.git.DeleteRemoteBranch(ctx, workDir, issue.Repo, branch, opts.GitHubToken); delErr != nil {
			slog.Warn("issues pipeline: could not clean up orphaned remote branch",
				"repo", issue.Repo, "branch", branch, "err", delErr)
		}
		p.publishError(issueID, issue, fmt.Errorf("create pr: %w", err))
		return nil, fmt.Errorf("issues pipeline: create pr: %w", err)
	}

	prNumber := createdPR.Number

	// Store the auto-created PR in SQLite so the Activity view shows
	// it immediately with the correct title (fixes #117).
	now := time.Now().UTC()
	prAuthor := strings.TrimSpace(createdPR.Author)
	if prAuthor == "" {
		prAuthor = strings.TrimSpace(strings.TrimLeft(opts.AuthUser, "@"))
	}
	if prAuthor == "" {
		// The PR row's author column is NOT NULL; the upsert will surface
		// the constraint. Warn so the missing identity is observable in
		// logs rather than mysteriously truncating Activity rows.
		slog.Warn("issues pipeline: auto-created PR has no recoverable author identity",
			"repo", issue.Repo, "pr", createdPR.Number,
			"created_pr_author", createdPR.Author, "auth_user", opts.AuthUser)
	}
	prRow := &store.PR{
		GithubID:  createdPR.ID,
		Repo:      issue.Repo,
		Number:    createdPR.Number,
		Title:     prTitle,
		Author:    prAuthor,
		URL:       createdPR.HTMLURL,
		State:     "open",
		UpdatedAt: now,
		FetchedAt: now,
	}
	prRowID, upsertErr := p.store.UpsertPR(prRow)
	if upsertErr != nil {
		slog.Warn("issues pipeline: failed to store auto-created PR",
			"repo", issue.Repo, "pr", createdPR.Number, "err", upsertErr)
	}
	// Mark the PR as auto_implement-origin so Tier 3's review-state
	// checker picks it up (#482). The mark is best-effort: a failure
	// here only means the observation layer misses this PR, not that
	// the PR itself is corrupted.
	if prRowID > 0 {
		if markErr := p.store.MarkPRAutoImplementOrigin(prRowID, issueID); markErr != nil {
			slog.Warn("issues pipeline: failed to mark PR auto_implement origin",
				"repo", issue.Repo, "pr", createdPR.Number, "err", markErr)
		}
	}
	// Enrol the PR in the Tier 3 watch bucket so the review-state
	// checker periodically refreshes it. Nil-safe for single-tier
	// setups; failure is best-effort (the next scan still creates a
	// row if needed when humans review the PR).
	if p.watch != nil {
		if enrollErr := p.watch.Enroll(ctx, "pr", issue.Repo, createdPR.Number, createdPR.ID); enrollErr != nil {
			slog.Warn("issues pipeline: failed to enroll auto-implement PR in Tier 3 watch",
				"repo", issue.Repo, "pr", createdPR.Number, "err", enrollErr)
		}
	}

	// Apply PR metadata (reviewers, labels, assignees). All best-effort —
	// a metadata failure does not roll back the PR, which is already public.
	metadataOpts := opts
	metadataOpts.PRAssignee = resolveAutoImplementPRAssignee(issue, opts)
	metadataOpts.PRReviewers = filterSelfPRReviewers(metadataOpts.PRReviewers, firstNonEmpty(p.currentBotLogin(), opts.AuthUser))
	applyPRMetadata(p.gh, issue.Repo, prNumber, metadataOpts)

	// Post a done-marker comment on the issue so watchers see the PR land
	// and the fetcher's marker scan skips the issue on future polls (#238).
	// Non-fatal on failure — the PR is already public and the review row
	// carries the number, so a missed comment does not lose information.
	linkBackBody := fmt.Sprintf(
		"%s\n✅ Implementation complete — PR #%d created on branch `%s`.\nThis issue will not be reprocessed unless a retry marker is added.",
		MarkerDone, prNumber, branch,
	)
	commentedAt, linkErr := p.gh.PostComment(issue.Repo, issue.Number, linkBackBody)
	if linkErr != nil {
		slog.Warn("issues pipeline: link-back comment failed",
			"repo", issue.Repo, "number", issue.Number, "err", linkErr)
	}

	rev := &store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     cli,
		Summary:     fmt.Sprintf("Auto-implementation landed as PR #%d on branch %s.", prNumber, branch),
		Triage:      "{}", // no triage block for implement runs
		NextSteps:   "[]",
		ActionTaken: string(config.IssueModeDevelop),
		PRCreated:   prNumber,
		CreatedAt:   time.Now().UTC(),
		CommentedAt: commentedAt,
	}
	revID, err := p.store.InsertIssueReview(rev)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: store review: %w", err)
	}
	rev.ID = revID

	// issue_number/pr_number are canonical for activity-log consumers; number
	// and pr_created remain legacy SSE aliases for older UI/session consumers.
	p.publish(sse.EventIssueImplemented, map[string]any{
		"issue_id": issueID, "number": issue.Number, "issue_number": issue.Number, "repo": issue.Repo,
		"issue_title": issue.Title, "cli_used": cli,
		"pr_created": prNumber, "pr_number": prNumber, "pr_url": createdPR.HTMLURL, "branch": branch,
	})
	if p.notify != nil {
		p.notify.Notify("Issue Auto-Implemented",
			fmt.Sprintf("%s #%d → PR #%d", issue.Repo, issue.Number, prNumber))
	}
	slog.Info("issues pipeline: auto_implement complete",
		"repo", issue.Repo, "number", issue.Number,
		"branch", branch, "pr", prNumber)
	return rev, nil
}

// autoImplementNoChangesFallback runs when the agent left the working tree
// untouched — usually because the prompt's "leave untouched if you cannot
// implement" escape hatch fired. We post a review_only-style comment so the
// issue still gets acknowledged and the user sees why no PR appeared.
func (p *Pipeline) autoImplementNoChangesFallback(issue *github.Issue, issueID int64, cli string) (*store.IssueReview, error) {
	// MarkerDone makes this a terminal state for the fetcher's marker scan
	// (#483): without it, the fallback row collides with the dedup gate on
	// every poll and the issue sits in limbo. We deliberately do NOT embed
	// the MarkerRetry literal in the body — ScanMarkers prioritises Retry
	// over Done within a single comment, so the very next poll would
	// reprocess the issue. The retry instructions reference the token by
	// keyword instead and point at the configuration guide.
	body := fmt.Sprintf(
		"%s\n## ⚠️ Heimdallm auto-implement skipped\n\n"+
			"The agent looked at #%d but left the working tree unchanged — it likely needs a human decision or more context than the issue alone provides.\n\n"+
			"To retry auto-implementation, post a new comment containing a Heimdallm `heimdallm:retry` marker (see the configuration guide's `auto_implement` section for the exact syntax). To stop here, remove the develop label.\n\n"+
			"---\n*auto_implement → review_only fallback · Heimdallm*",
		MarkerDone, issue.Number,
	)
	commentedAt, postErr := p.gh.PostComment(issue.Repo, issue.Number, body)
	if postErr != nil {
		slog.Warn("issues pipeline: auto_implement fallback PostComment failed",
			"repo", issue.Repo, "number", issue.Number, "err", postErr)
	}

	rev := &store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     cli,
		Summary:     "auto_implement produced no changes; downgraded to review_only",
		Triage:      "{}",
		NextSteps:   "[]",
		ActionTaken: ActionAutoImplementNoChanges,
		CreatedAt:   time.Now().UTC(),
		CommentedAt: commentedAt,
	}
	revID, err := p.store.InsertIssueReview(rev)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: store fallback review: %w", err)
	}
	rev.ID = revID

	// EventIssueReviewError (not Completed) so the UI renders a
	// needs-attention card. The `error` field follows the convention of
	// other EventIssueReviewError emitters (Flutter's `_errorDetails` and
	// the detail toast both read it); `reason` is a machine-readable cause
	// kept distinct so future UI work can render reason-specific copy
	// without parsing the human string (#483).
	p.publish(sse.EventIssueReviewError, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo,
		"reason":  "auto_implement_no_changes",
		"post_ok": postErr == nil,
		"error":   "Agent left the working tree unchanged; post a retry marker comment to retry, or close the issue.",
	})
	slog.Info("issues pipeline: auto_implement had no changes, posted fallback comment",
		"repo", issue.Repo, "number", issue.Number, "posted", postErr == nil)
	return rev, nil
}

// ensureAutoImplementWritePerms returns opts with a sane default permission /
// approval flag for the auto_implement path when the operator has not set one.
// Without this, the daemon spawns the CLI in a mode where Edit/Write tool
// calls require a human prompt — in non-interactive `-p` mode they simply
// don't fire, the CLI exits cleanly, and `git status` shows a clean tree, so
// every issue degrades to the no-changes fallback comment (#433). Any
// explicit operator setting wins; this only fills the gap when *nothing* is
// configured.
func ensureAutoImplementWritePerms(cli string, opts executor.ExecOptions) executor.ExecOptions {
	switch cli {
	case "claude":
		if strings.TrimSpace(opts.PermissionMode) == "" && !opts.DangerouslySkipPerms {
			opts.PermissionMode = "acceptEdits"
			slog.Info("issues pipeline: defaulting claude permission_mode to acceptEdits for auto_implement",
				"cli", cli)
		}
	case "codex":
		if strings.TrimSpace(opts.ApprovalMode) == "" {
			opts.ApprovalMode = "never"
			slog.Info("issues pipeline: defaulting codex approval_mode to never for auto_implement",
				"cli", cli)
		}
	case "gemini":
		if strings.TrimSpace(opts.ApprovalMode) == "" {
			opts.ApprovalMode = "auto_edit"
			slog.Info("issues pipeline: defaulting gemini approval_mode to auto_edit for auto_implement",
				"cli", cli)
		}
	}
	return opts
}

func (p *Pipeline) recordAutoImplementFailure(issueID int64, cli, summary string) {
	if p == nil || p.store == nil {
		return
	}
	if strings.TrimSpace(cli) == "" {
		cli = "unknown"
	}
	failedRev := &store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     cli,
		Summary:     summary,
		Triage:      "{}",
		NextSteps:   "[]",
		ActionTaken: "auto_implement_failed",
		CreatedAt:   time.Now().UTC(),
	}
	if _, storeErr := p.store.InsertIssueReview(failedRev); storeErr != nil {
		slog.Warn("issues pipeline: could not record auto_implement failure in store",
			"issue_id", issueID, "err", storeErr)
	}
}

// generatePRDescription invokes the LLM with the implementation diff to
// produce a rich PR title and description. Non-fatal: the caller falls back
// to template strings on any error.
func (p *Pipeline) generatePRDescription(ctx context.Context, cli string, issue *github.Issue, workDir string, execOpts executor.ExecOptions) (*PRDescription, error) {
	diff, err := p.git.Diff(ctx, workDir, "FETCH_HEAD")
	if err != nil {
		return nil, fmt.Errorf("get diff: %w", err)
	}
	if diff == "" {
		return nil, fmt.Errorf("empty diff")
	}
	prompt := BuildPRDescriptionPrompt(issue.Number, issue.Title, diff)
	raw, err := p.executor.ExecuteRaw(cli, prompt, execOpts)
	if err != nil {
		return nil, fmt.Errorf("execute LLM: %w", err)
	}
	return parsePRDescription(raw)
}

// parsePRDescription strips LLM wrappers and unmarshals the PR description JSON.
func parsePRDescription(data []byte) (*PRDescription, error) {
	clean := executor.StripToJSON(data)
	var d PRDescription
	if err := json.Unmarshal(clean, &d); err != nil {
		return nil, fmt.Errorf("parse PR description JSON: %w (raw: %.200s)", err, clean)
	}
	if d.Title == "" {
		return nil, fmt.Errorf("PR description missing title")
	}
	if d.Body == "" {
		return nil, fmt.Errorf("PR description missing body")
	}
	return &d, nil
}

// publish emits an SSE event with a pre-built data map. Swallowed if
// broker is nil (tests that don't care about SSE).
func (p *Pipeline) publish(eventType string, data map[string]any) {
	if p.broker == nil {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	p.broker.Publish(sse.Event{Type: eventType, Data: string(b)})
}

// publishError emits an issue_review_error event with issue context + reason.
func (p *Pipeline) publishError(issueID int64, issue *github.Issue, err error) {
	p.publish(sse.EventIssueReviewError, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo,
		"error": err.Error(),
	})
}

// parseIssueResult strips LLM wrappers and unmarshals the triage JSON.
// A missing `severity` at top level falls back to the triage block's value
// (and ultimately to "low") so downstream consumers never see empty.
func parseIssueResult(data []byte) (*IssueReviewResult, error) {
	clean := executor.StripToJSON(data)
	var r IssueReviewResult
	if err := json.Unmarshal(clean, &r); err != nil {
		return nil, fmt.Errorf("issues pipeline: parse JSON: %w (raw: %.200s)", err, clean)
	}
	if r.Severity == "" {
		r.Severity = r.Triage.Severity
	}
	if r.Severity == "" {
		r.Severity = "low"
	}
	return &r, nil
}

// IssueToStoreRow converts the github.Issue wire shape into the store row. The
// store keeps assignees and labels as JSON arrays (`[]` when empty), matching
// the schema introduced in #24. Exported so out-of-package idempotent-upsert
// callers (e.g. the autonomous poller) share this single projection.
//
// The issue's processing mode is intentionally not part of store.Issue — the
// issues table captures the issue itself, while the mode of *each pipeline
// run* lives on issue_reviews.action_taken. That
// separation lets a single issue accumulate multiple reviews across mode
// changes (e.g. initial review_only → later auto_implement in #27) without
// losing the history.

// applyPRMetadata sets reviewers, labels, and assignees on a newly created PR.
// All operations are best-effort — failures are logged but do not affect the
// pipeline result. The PR is already public at this point.
func applyPRMetadata(gh PRMetadataApplier, repo string, prNumber int, opts RunOptions) {
	if len(opts.PRReviewers) > 0 {
		if err := gh.SetPRReviewers(repo, prNumber, opts.PRReviewers); err != nil {
			slog.Warn("issues pipeline: set pr reviewers failed",
				"repo", repo, "pr", prNumber, "err", err)
		}
	}
	if len(opts.PRLabels) > 0 {
		if err := gh.AddLabels(repo, prNumber, opts.PRLabels); err != nil {
			slog.Warn("issues pipeline: add pr labels failed",
				"repo", repo, "pr", prNumber, "err", err)
		}
	}
	if opts.PRAssignee != "" {
		if err := gh.SetAssignees(repo, prNumber, []string{opts.PRAssignee}); err != nil {
			slog.Warn("issues pipeline: set pr assignee failed",
				"repo", repo, "pr", prNumber, "err", err)
		}
	}
}

func filterSelfPRReviewers(reviewers []string, authUser string) []string {
	authUser = normalizeGitHubLogin(authUser)
	if authUser == "" || len(reviewers) == 0 {
		return reviewers
	}
	filtered := make([]string, 0, len(reviewers))
	skipped := 0
	for _, reviewer := range reviewers {
		if strings.EqualFold(normalizeGitHubLogin(reviewer), authUser) {
			skipped++
			continue
		}
		filtered = append(filtered, reviewer)
	}
	if skipped > 0 {
		slog.Info("issues pipeline: self reviewer skipped on auto-created PR", "count", skipped, "user", authUser)
	}
	return filtered
}

func resolveAutoImplementPRAssignee(issue *github.Issue, opts RunOptions) string {
	if assignee := normalizeGitHubLogin(opts.PRAssignee); assignee != "" {
		return assignee
	}
	if issue == nil {
		return ""
	}
	assignees := issue.AssigneeLogins()
	if len(assignees) != 1 {
		return ""
	}
	return normalizeGitHubLogin(assignees[0])
}

// IssueLabelCatalog is optionally implemented by GitHub clients that can
// inspect/create repository labels. Tests and custom clients can omit it; the
// pipeline still attempts AddLabels and treats failures as non-fatal.
type IssueLabelCatalog interface {
	FetchLabels(repo string) ([]string, error)
	CreateLabel(repo, name, color, description string) error
}

func applyIssueTriageMetadata(gh issueGitHub, repo string, number int, r *IssueReviewResult) {
	if r == nil {
		return
	}
	if label := strings.TrimSpace(r.Triage.PriorityLabel); label != "" {
		label = ensureIssueLabel(gh, repo, label, r.Severity)
		if err := gh.AddLabels(repo, number, []string{label}); err != nil {
			slog.Warn("issues pipeline: add priority label failed",
				"repo", repo, "issue", number, "label", label, "err", err)
		}
	}
	if assignee := strings.TrimSpace(r.Triage.AssignedAssignee); assignee != "" {
		if err := gh.SetAssignees(repo, number, []string{assignee}); err != nil {
			slog.Warn("issues pipeline: set issue assignee failed",
				"repo", repo, "issue", number, "assignee", assignee, "err", err)
		}
	}
}

func ensureIssueLabel(gh issueGitHub, repo, label, severity string) string {
	catalog, ok := gh.(IssueLabelCatalog)
	if !ok {
		slog.Warn("issues pipeline: label catalog unavailable; trying priority label directly",
			"repo", repo, "label", label)
		return label
	}
	labels, err := catalog.FetchLabels(repo)
	if err != nil {
		slog.Warn("issues pipeline: fetch labels failed; trying priority label directly",
			"repo", repo, "label", label, "err", err)
		return label
	}
	for _, existing := range labels {
		if strings.EqualFold(existing, label) {
			return existing
		}
	}
	if err := catalog.CreateLabel(repo, label, priorityLabelColor(severity), "Heimdallm triage priority"); err != nil {
		slog.Warn("issues pipeline: create priority label failed; trying to apply anyway",
			"repo", repo, "label", label, "err", err)
	}
	return label
}

// extractSeverity pulls the severity string from a triage JSON blob.
// Returns empty string on any failure so callers can fall back gracefully.
func extractSeverity(triageJSON string) string {
	if triageJSON == "" || triageJSON == "{}" {
		return ""
	}
	var t Triage
	if err := json.Unmarshal([]byte(triageJSON), &t); err != nil {
		return ""
	}
	return t.Severity
}

func IssueToStoreRow(i *github.Issue) (*store.Issue, error) {
	assignees := i.AssigneeLogins()
	if assignees == nil {
		assignees = []string{}
	}
	labels := i.LabelNames()
	if labels == nil {
		labels = []string{}
	}
	assigneesJSON, err := json.Marshal(assignees)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: marshal assignees: %w", err)
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: marshal labels: %w", err)
	}
	return &store.Issue{
		GithubID:  i.ID,
		Repo:      i.Repo,
		Number:    i.Number,
		Title:     i.Title,
		Body:      i.Body,
		Author:    i.User.Login,
		Assignees: string(assigneesJSON),
		Labels:    string(labelsJSON),
		State:     i.State,
		CreatedAt: i.CreatedAt,
		FetchedAt: time.Now().UTC(),
	}, nil
}

// BuildMarkdownComment renders the triage result as the comment body posted
// to GitHub. Kept stable and human-readable because it lands under the user's
// nose on every triaged issue; changes should be deliberate.
func BuildMarkdownComment(r *IssueReviewResult) string {
	sev := strings.ToUpper(r.Severity)
	icon := "🟡"
	switch r.Severity {
	case "critical":
		icon = "🛑"
	case "high":
		icon = "🔴"
	case "medium":
		icon = "⚠️"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## %s Heimdallm triage — %s\n\n", icon, sev))
	if r.Summary != "" {
		sb.WriteString(r.Summary)
		sb.WriteString("\n\n")
	}

	sb.WriteString("### Classification\n\n")
	if r.Triage.Category != "" {
		sb.WriteString(fmt.Sprintf("- **Category:** %s\n", r.Triage.Category))
	}
	if r.Triage.Severity != "" {
		sb.WriteString(fmt.Sprintf("- **Suggested severity:** %s\n", r.Triage.Severity))
	}
	if r.Triage.PriorityLabel != "" {
		sb.WriteString(fmt.Sprintf("- **Priority label:** %s\n", r.Triage.PriorityLabel))
	}
	if r.Triage.AffectedArea != "" {
		sb.WriteString(fmt.Sprintf("- **Likely affected area:** %s\n", r.Triage.AffectedArea))
	}
	if len(r.Triage.AffectedPaths) > 0 {
		sb.WriteString(fmt.Sprintf("- **Affected paths:** %s\n", strings.Join(r.Triage.AffectedPaths, ", ")))
	}
	if r.Triage.AssignedAssignee != "" {
		sb.WriteString(fmt.Sprintf("- **Assigned owner:** @%s\n", strings.TrimLeft(r.Triage.AssignedAssignee, "@")))
	} else if r.Triage.TentativeAssignee != "" {
		sb.WriteString(fmt.Sprintf("- **Tentative owner:** @%s\n", strings.TrimLeft(r.Triage.TentativeAssignee, "@")))
	}
	if r.Triage.SuggestedAssignee != "" {
		// Strip any leading '@' the LLM may have included so the template
		// does not render a double '@@alice' that pings nobody.
		assignee := strings.TrimLeft(r.Triage.SuggestedAssignee, "@")
		sb.WriteString(fmt.Sprintf("- **Suggested assignee:** @%s\n", assignee))
	}
	if r.Triage.AssigneeReason != "" {
		sb.WriteString(fmt.Sprintf("- **Owner rationale:** %s\n", r.Triage.AssigneeReason))
	}
	if r.Triage.AssigneeConfidence != "" {
		sb.WriteString(fmt.Sprintf("- **Owner confidence:** %s\n", r.Triage.AssigneeConfidence))
	}
	if len(r.Triage.AssigneeEvidence) > 0 {
		sb.WriteString("- **Owner evidence:**\n")
		for _, ev := range r.Triage.AssigneeEvidence {
			sb.WriteString(fmt.Sprintf("  - %s\n", ev))
		}
	}
	sb.WriteString("\n")

	if len(r.NextSteps) > 0 {
		sb.WriteString("### Next steps\n\n")
		for _, s := range r.NextSteps {
			sb.WriteString("- " + s + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n*review_only mode · reviewed by Heimdallm*")
	return sb.String()
}
