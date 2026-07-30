package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// ErrCircuitBreakerTripped is returned by Run when a review was skipped
// because the per-PR HEAD or per-repo cap was exceeded. Callers detect it via
// errors.As on a *CircuitBreakerError value to extract the human-readable
// reason for telemetry/UI, or via errors.Is(err, ErrCircuitBreakerTripped)
// when the reason is not needed.
var ErrCircuitBreakerTripped = errors.New("pipeline: circuit breaker tripped")

// CircuitBreakerError wraps ErrCircuitBreakerTripped with the specific
// reason the breaker returned ("per-PR HEAD cap reached: ...", etc). Use
// errors.As on this type to read Reason without parsing the error string.
type CircuitBreakerError struct {
	Reason string
}

func (e *CircuitBreakerError) Error() string {
	return ErrCircuitBreakerTripped.Error() + ": " + e.Reason
}

func (e *CircuitBreakerError) Unwrap() error { return ErrCircuitBreakerTripped }

// DiffFetcher retrieves the diff for one immutable pull-request HEAD.
type DiffFetcher interface {
	FetchDiffForCommit(repo string, number int, headSHA string) (string, error)
}

// HeadSHAResolver fetches a PR's current HEAD commit SHA. The Search Issues
// API (used by Tier 2 to discover review requests) does not populate head.sha,
// so the pipeline needs an explicit lookup before it can dedup by commit.
type HeadSHAResolver interface {
	GetPRHeadSHA(repo string, number int) (string, error)
}

// CLIExecutor detects and runs an AI CLI tool.
type CLIExecutor interface {
	Detect(primary, fallback string) (string, error)
	Execute(cli, prompt string, opts executor.ExecOptions) (*executor.ReviewResult, error)
}

// Notifier sends desktop or system notifications.
type Notifier interface {
	Notify(title, message string)
}

// GitHubReviewer can submit a review and post issue comments to GitHub.
type GitHubReviewer interface {
	SubmitReviewForCommit(repo string, number int, body, event, commitID string) (int64, string, error)
	// PostComment posts a general PR comment (used in multi-feedback mode).
	PostComment(repo string, number int, body string) (time.Time, error)
}

// CommentFetcher retrieves PR comments for context injection into the AI prompt.
type CommentFetcher interface {
	FetchComments(repo string, number int) ([]github.Comment, error)
}

// TimelineFetcher returns the review_requested / review_dismissed events
// targeting a specific reviewer login on a PR. Used by the SHA-skip
// path to detect explicit re-request review actions that the user
// performed via the GitHub UI even though the HEAD SHA is unchanged.
// See theburrowhub/heimdallm#322 Bug 5.
//
// Optional dependency: when not set (nil), the pipeline falls back to
// the previous behaviour (skip on SHA match regardless of timeline).
type TimelineFetcher interface {
	GetPRTimelineEventsForReviewer(repo string, number int, login string) ([]github.TimelineEvent, error)
}

// ReviewerFetcher reports the PR's current requested_reviewers (via the Pulls
// API). Used by the SHA-skip path to catch re-reviews that leave NO
// review_requested timeline event: GitHub re-adds the bot to
// requested_reviewers on some flows (observed on ai-bumblebee-proxy#1532)
// without emitting an event the TimelineFetcher can see, so timeline-only
// detection is blind to them. Combined with a HEAD SHA that advanced past the
// last reviewed commit, being a current requested reviewer is treated as an
// implicit re-request.
//
// Optional dependency: when not set (nil), the requested-reviewer bypass is
// disabled and the pipeline falls back to timeline-only detection.
type ReviewerFetcher interface {
	GetPRHeadInfo(repo string, number int) (github.PRHeadInfo, error)
}

// Publisher is the broker subset the pipeline uses to emit lifecycle
// events. *sse.Broker satisfies it directly so main.go can wire the
// production broker as the publisher with no adapter.
//
// Why the pipeline (not the caller) emits these events: the caller
// cannot know which path Run will take (real review, SHA-skip,
// legacy-backfill, gate-skip) until Run returns, but the UI/notify
// stack consumes review_started the instant it lands. Emitting from
// the caller before Run produced phantom "reviewing" spinners in
// Flutter and a desktop notification per poll cycle on stable PRs —
// exactly the regression Bug 3 was supposed to fix. Centralising the
// emission inside Run keeps the lifecycle SSEs honest. See
// theburrowhub/heimdallm#322 Bugs 3 and 4.
//
// Optional dependency: when not set (nil), the pipeline emits no
// lifecycle events and the caller is responsible (legacy contract,
// preserved so existing tests don't need a stub publisher each).
type Publisher interface {
	Publish(e sse.Event)
}

// Pipeline orchestrates the full PR review flow.
type Pipeline struct {
	store *store.Store
	gh    interface {
		DiffFetcher
		GitHubReviewer
		CommentFetcher
		HeadSHAResolver
	}
	executor CLIExecutor
	notify   Notifier
	botLogin string
	// breaker caps the number of reviews per PR and per repo. Nil disables
	// all caps (the pre-issue-243 behaviour). Populated at daemon startup via
	// SetCircuitBreakerLimits.
	breaker *store.CircuitBreakerLimits
	// timeline is the optional event-history fetcher used to bypass the
	// SHA-skip path on explicit re-request review actions. Nil keeps the
	// pre-#322 behaviour (skip on SHA match regardless of user intent).
	timeline TimelineFetcher
	// reviewers is the optional requested_reviewers fetcher used to bypass
	// the SHA-skip path when the HEAD advanced past the last reviewed commit
	// and the bot is a current requested reviewer, even with no
	// review_requested timeline event. Nil disables that bypass.
	reviewers ReviewerFetcher
	// publisher emits lifecycle SSE events (pr_detected, review_started,
	// review_completed, review_skipped) at the same semantic point Run
	// makes the actual decision. Nil disables emission (legacy contract).
	publisher Publisher
}

// New creates a new Pipeline with the provided dependencies.
func New(s *store.Store, gh interface {
	DiffFetcher
	GitHubReviewer
	CommentFetcher
	HeadSHAResolver
}, exec CLIExecutor, n Notifier) *Pipeline {
	return &Pipeline{store: s, gh: gh, executor: exec, notify: n}
}

// SetBotLogin sets the GitHub login of the bot account. Used to filter
// the bot's own comments from re-review discussion context.
func (p *Pipeline) SetBotLogin(login string) { p.botLogin = login }

// SetCircuitBreakerLimits enables the per-PR and per-repo caps. Nil
// disables all caps. Captured by pointer at wiring time — config reloads
// do NOT re-read this; see theburrowhub/heimdallm#243 for the rationale
// and the follow-up ticket for re-plumbing via a getter.
func (p *Pipeline) SetCircuitBreakerLimits(limits *store.CircuitBreakerLimits) {
	p.breaker = limits
}

// SetTimelineFetcher enables the explicit-re-request-review bypass for
// the SHA-skip path. Nil keeps the pre-#322 behaviour (skip on SHA
// match regardless of user intent). Production wires the *github.Client
// here at daemon startup.
func (p *Pipeline) SetTimelineFetcher(t TimelineFetcher) {
	p.timeline = t
}

// SetReviewerFetcher enables the requested-reviewer bypass for the SHA-skip
// path (re-reviews that leave no review_requested timeline event). Nil keeps
// timeline-only detection. Production wires the *github.Client here.
func (p *Pipeline) SetReviewerFetcher(r ReviewerFetcher) {
	p.reviewers = r
}

// SetPublisher wires the SSE broker so Run can emit lifecycle events
// at the correct semantic point (after the SHA-skip / gate decisions
// rather than blindly at function entry). Nil disables emission and
// callers must handle lifecycle themselves — legacy contract.
func (p *Pipeline) SetPublisher(pub Publisher) {
	p.publisher = pub
}

// stopIfRepoBecameIneligible is called at side-effect boundaries. A review
// that has already been stored stays pending: disabling monitoring is
// reversible, so the publish-pending scanner must be able to resume it after
// the repo is re-enabled. The scanner and publish worker both apply the same
// live eligibility gate before enqueueing/submitting it.
func (p *Pipeline) stopIfRepoBecameIneligible(
	pr *github.PullRequest,
	check func(string) bool,
) (bool, error) {
	if check == nil || check(pr.Repo) {
		return false, nil
	}
	p.publishSkipped(pr, SkipReasonNotMonitored)
	slog.Info("pipeline: repo no longer monitored, deferring review",
		"repo", pr.Repo, "pr", pr.Number)
	return true, nil
}

// publish emits an SSE lifecycle event with the given payload. No-op
// when no publisher is wired. A marshal failure on a map[string]any
// of basic types should not happen in practice (every payload site
// uses string / int / int64 / float64), but if it ever does we log
// at Warn level rather than swallow silently — debugging a missing
// SSE event without that breadcrumb is painful.
func (p *Pipeline) publish(eventType string, data map[string]any) {
	if p.publisher == nil {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		slog.Warn("pipeline: failed to marshal SSE payload, dropping event",
			"event", eventType, "err", err)
		return
	}
	p.publisher.Publish(sse.Event{Type: eventType, Data: string(b)})
}

// publishSkipped is a small helper for the four skip paths in Run that
// need to emit EventReviewSkipped with the same shape: repo, pr_number,
// pr_title, reason. Centralised so changes to the payload schema only
// touch one site.
func (p *Pipeline) publishSkipped(pr *github.PullRequest, reason SkipReason) {
	p.publish(sse.EventReviewSkipped, map[string]any{
		"repo":      pr.Repo,
		"pr_number": pr.Number,
		"pr_title":  pr.Title,
		"reason":    string(reason),
	})
}

// shouldBypassSHASkipForReReview returns true iff the operator
// explicitly re-requested a review on this PR after the previous
// review's CreatedAt and that re-request is still in effect (not
// superseded by a later dismissal). All preconditions fail closed:
// missing dependencies (nil timeline / empty bot login / nil
// prevReview) or a timeline API error keep the SHA skip in place so a
// transient outage cannot widen the cost surface. See
// theburrowhub/heimdallm#322 Bug 5.
func (p *Pipeline) shouldBypassSHASkipForReReview(pr *github.PullRequest, prevReview *store.Review) bool {
	if p.timeline == nil || p.botLogin == "" || prevReview == nil {
		return false
	}
	events, err := p.timeline.GetPRTimelineEventsForReviewer(pr.Repo, pr.Number, p.botLogin)
	if err != nil {
		slog.Warn("pipeline: re-request timeline lookup failed, keeping SHA skip (fail-closed)",
			"repo", pr.Repo, "pr", pr.Number, "err", err)
		return false
	}
	// events is sorted ascending by CreatedAt. We need the LAST event
	// whose timestamp is strictly newer than prevReview.CreatedAt — by
	// definition the tail of the slice — so iterate backward and stop
	// at the first qualifying entry. Marginally faster than a forward
	// walk for active PRs (which is exactly when this runs hot) and
	// reads more clearly: "is the most recent re-review-relevant event
	// still a request?".
	for i := len(events) - 1; i >= 0; i-- {
		ev := &events[i]
		if !ev.CreatedAt.After(prevReview.CreatedAt) {
			// Past the cutoff: everything earlier is already-satisfied
			// (events are sorted ascending), so no need to continue.
			return false
		}
		return ev.Event == "review_requested"
	}
	return false
}

// shouldReReviewNewCommitsAsRequestedReviewer returns true when the PR's HEAD
// has advanced past the last reviewed commit AND the bot is currently a
// requested reviewer. This catches re-reviews that leave no review_requested
// timeline event (so shouldBypassSHASkipForReReview is blind to them): GitHub
// re-adds the bot to requested_reviewers on some flows without emitting an
// event — observed on ai-bumblebee-proxy#1532, where the bot is a pending
// reviewer on unreviewed commits yet the timeline has no fresh request.
//
// Deliberately requires BOTH conditions:
//   - HEAD SHA advanced past prevReview.HeadSHA — there is genuinely new,
//     unreviewed code. A same-SHA re-add is NOT covered: no new code means
//     re-reviewing would just reopen the auto-re-add loop #509 guarded against.
//   - the bot is still a requested reviewer — a new commit with NO pending
//     request must not trigger a review (the operator's explicit rule).
//
// Fail-closed on a missing dependency (nil reviewers / empty bot login / nil
// prevReview / empty prevReview.HeadSHA) or an API error, matching the
// timeline bypass's posture.
func (p *Pipeline) shouldReReviewNewCommitsAsRequestedReviewer(pr *github.PullRequest, prevReview *store.Review) bool {
	if p.reviewers == nil || p.botLogin == "" || prevReview == nil {
		return false
	}
	// New code only: the HEAD must have advanced past the reviewed commit.
	// Empty prevReview.HeadSHA is the ambiguous legacy row handled elsewhere;
	// don't treat "" != sha as "new commits" here.
	if prevReview.HeadSHA == "" || prevReview.HeadSHA == pr.Head.SHA {
		return false
	}
	info, err := p.reviewers.GetPRHeadInfo(pr.Repo, pr.Number)
	if err != nil {
		slog.Warn("pipeline: requested-reviewer lookup failed, keeping SHA skip (fail-closed)",
			"repo", pr.Repo, "pr", pr.Number, "err", err)
		return false
	}
	return info.ReviewRequestedFor(p.botLogin)
}

// applyPrompt resolves a prompt with priority: repoPromptID > agentPromptID > global default.
func (p *Pipeline) applyPrompt(repoPromptID, agentPromptID string, tmpl *string, flags *string) {
	agents, err := p.store.ListAgents()
	if err != nil || len(agents) == 0 {
		return
	}
	var a *store.Agent
	// 1. Repo-level override
	for _, ag := range agents {
		if repoPromptID != "" && ag.ID == repoPromptID {
			a = ag
			break
		}
	}
	// 2. Agent-level override
	if a == nil {
		for _, ag := range agents {
			if agentPromptID != "" && ag.ID == agentPromptID {
				a = ag
				break
			}
		}
	}
	// 3. Global default for the PR-review category (the three categories
	// now have independent active flags, see store.AgentCategory).
	if a == nil {
		for _, ag := range agents {
			if ag.IsDefaultPR {
				a = ag
				break
			}
		}
	}
	if a == nil {
		return
	}
	switch {
	case a.Prompt != "":
		*tmpl = a.Prompt
	case a.Instructions != "":
		*tmpl = executor.DefaultTemplateWithInstructions(a.Instructions)
	}
	*flags = a.CLIFlags
}

func applyStoredProfileCLIFlags(cli, raw string, opts executor.ExecOptions) (executor.ExecOptions, error) {
	profileOpts, migrated, err := executor.NormalizeLegacyCLIFlagsForCLI(cli, raw)
	if err != nil {
		return opts, err
	}
	for _, field := range migrated {
		switch field {
		case "model":
			opts.Model = profileOpts.Model
		case "effort":
			opts.Effort = profileOpts.Effort
		case "max_turns":
			opts.MaxTurns = profileOpts.MaxTurns
		}
	}
	opts.ExtraFlags = profileOpts.ExtraFlags
	return opts, nil
}

// RunOptions carries per-execution settings derived from global + repo + agent config.
type RunOptions struct {
	Primary        string
	Fallback       string
	PromptOverride string // repo-level prompt (highest priority)
	AgentPromptID  string // agent-level prompt (used if no repo-level override)
	ReviewMode     string
	ExecOpts       executor.ExecOptions // model, flags, workdir
	// Guards are evaluated at the top of Run as defense-in-depth. Callers
	// (Tier 2 / Tier 3) should have already filtered with pipeline.Evaluate
	// before pushing PRs into the pipeline; this layer prevents regressions
	// if a new caller forgets.
	Guards GateConfig
	// InstructionAuthors is the resolved allowlist of GitHub logins permitted
	// to set persistent per-repo instructions via comment directives (#383).
	// Empty disables the comment-driven path for this repo.
	InstructionAuthors []string
	// NeverApproveWithIssues, when true, publishes the review as COMMENT
	// instead of APPROVE whenever the review found any issue (see ReviewEvent).
	NeverApproveWithIssues bool
	// NeverApproveMinSeverity is the minimum finding severity that triggers
	// the NeverApproveWithIssues downgrade ("low"|"medium"|"high"). Empty is
	// equivalent to "low": any finding downgrades (see ReviewEvent).
	NeverApproveMinSeverity string
	// RepoEligible is a live gate for automatic work. It is checked again at
	// execution/publication boundaries so a config reload can stop an in-flight
	// review. Nil intentionally allows explicit/manual calls to proceed.
	RepoEligible func(string) bool
	// Force marks an explicit operator-initiated re-review (the app's
	// "Re-review" button → POST /prs/{id}/review). It bypasses the two
	// cost-control gates that exist to suppress the AUTOMATIC poll path:
	//   1. the re-review dedup gate — SHA-unchanged / no-new-review_requested
	//      (see #139/#245/#509). The app cannot create a GitHub
	//      review_requested event (Heimdallm authenticates as the operator's
	//      own account, which cannot request a review from itself), so the
	//      timeline bypass never fires and every manual re-review was
	//      silently skipped.
	//   2. the circuit breaker (per-PR/per-repo cap, #243) — a human clicking
	//      the button is deliberate intent, not a runaway loop.
	// Force must ONLY be set by the manual-trigger callback, never by the
	// pollers, so the automatic path keeps every protection intact. The
	// state guards (opts.Guards: closed / draft / self-authored) still apply.
	Force bool
}

// Run executes the full review pipeline for one PR and publishes the review to GitHub.
// Config priority: repo-level > agent-level > global default.
// SQLite is the source of truth: review is stored first, then published.
// If publishing fails, it is retried on the next call (when GitHubReviewID == 0).
//
// Return contract:
//   - (review, nil)  — normal success path; review has been stored (and
//     published unless GitHub was unreachable, in which case GitHubReviewID==0
//     and PublishPending will retry).
//   - (nil, err)     — a non-recoverable error before the review was stored.
//   - (nil, nil)     — the defense-in-depth gate (opts.Guards) rejected the
//     PR. Callers MUST nil-check the returned review before dereferencing it.
//     Skip-event publication is the caller's responsibility; the pipeline
//     only logs on this path so missed caller-side filtering is diagnosable.
func (p *Pipeline) Run(pr *github.PullRequest, opts RunOptions) (*store.Review, error) {
	primary := opts.Primary
	fallback := opts.Fallback
	promptOverride := opts.PromptOverride
	reviewMode := opts.ReviewMode
	slog.Info("pipeline: starting review", "repo", pr.Repo, "pr", pr.Number)

	// 1. Upsert PR record
	prRow := &store.PR{
		GithubID:  pr.ID,
		Repo:      pr.Repo,
		Number:    pr.Number,
		Title:     pr.Title,
		Author:    pr.User.Login,
		URL:       pr.HTMLURL,
		State:     pr.State,
		UpdatedAt: pr.UpdatedAt,
		FetchedAt: time.Now().UTC(),
	}
	prID, err := p.store.UpsertPR(prRow)
	if err != nil {
		return nil, fmt.Errorf("pipeline: upsert PR: %w", err)
	}
	if stopped, err := p.stopIfRepoBecameIneligible(pr, opts.RepoEligible); stopped {
		return nil, err
	}

	// Defense-in-depth: refuse to run the CLI if the gate rejects this PR.
	// Callers usually pre-filter with pipeline.Evaluate; the warn log on
	// reaching this branch flags missed caller-side checks. Emit the skip
	// SSE here too (with the actual reason, not a fabricated one) so the
	// UI lifecycle stays honest if a caller forgets to publish its own.
	if reason := Evaluate(PRGate{
		State:  pr.State,
		Draft:  pr.Draft,
		Author: pr.User.Login,
	}, opts.Guards); reason != SkipReasonNone {
		slog.Warn("pipeline: gate skip (caller did not filter)",
			"repo", pr.Repo, "pr", pr.Number, "reason", string(reason))
		p.publishSkipped(pr, reason)
		return nil, nil
	}

	// 2. Resolve the immutable review target before fetching any code. The
	// diff, local worktree, stored row and GitHub review must all refer to this
	// exact SHA; silently switching to a newer HEAD midway through a run would
	// mix two snapshots.
	targetSHA := strings.TrimSpace(pr.Head.SHA)
	if targetSHA == "" {
		sha, err := p.gh.GetPRHeadSHA(pr.Repo, pr.Number)
		if err != nil {
			// Short backoff before the single retry — 0ms back-to-back retries
			// are useless against 429s (the rate window is still active).
			// #243's specific failure mode was rate-limit 429s, so the retry
			// needs at least a small gap to have any chance of succeeding.
			time.Sleep(500 * time.Millisecond)
			sha, err = p.gh.GetPRHeadSHA(pr.Repo, pr.Number)
		}
		if err != nil {
			slog.Warn("pipeline: HEAD SHA unresolved — skipping review (fail-closed)",
				"repo", pr.Repo, "pr", pr.Number, "err", err)
			return nil, fmt.Errorf("pipeline: resolve HEAD SHA: %w", err)
		}
		targetSHA = strings.TrimSpace(sha)
		if targetSHA == "" {
			// Empty-but-nil-error: treat as a lookup failure. Proceeding would
			// store a review row with an empty HeadSHA, recreating the exact
			// ambiguous legacy-row shape (#322 Bug 4) the backfill exists to
			// repair — and on a forced run Force has no downstream dedup/breaker
			// backstop, so this is the only place to fail closed. Applies to
			// every caller, including the manual trigger.
			slog.Warn("pipeline: HEAD SHA resolved empty — skipping review (fail-closed)",
				"repo", pr.Repo, "pr", pr.Number)
			return nil, fmt.Errorf("pipeline: resolve HEAD SHA: empty SHA returned")
		}
	}
	// Keep the model in sync for helpers that still consume the PR view, while
	// retaining targetSHA as the authoritative immutable value for persistence
	// and publication below.
	pr.Head.SHA = targetSHA

	// 2a. Fetch a commit-addressed diff. The GitHub client verifies that the
	// live PR still points at targetSHA before comparing captured base/head
	// SHAs. A stale queued job is a normal skip; a later poll will schedule the
	// replacement commit.
	diff, err := p.gh.FetchDiffForCommit(pr.Repo, pr.Number, targetSHA)
	if err != nil {
		var headChanged *github.HeadChangedError
		if errors.As(err, &headChanged) {
			slog.Info("pipeline: HEAD changed before diff, skipping stale review",
				"repo", pr.Repo, "pr", pr.Number,
				"expected_head_sha", headChanged.Expected,
				"current_head_sha", headChanged.Actual)
			p.publishSkipped(pr, SkipReasonHeadChanged)
			return nil, nil
		}
		return nil, fmt.Errorf("pipeline: fetch diff: %w", err)
	}

	// 2b. Authoritative dedup by HEAD commit SHA. The Tier 2/3 dedup uses
	// updated_at — but any peer reviewer submitting a review (human or another
	// heimdallm instance) bumps updated_at, which would otherwise cause us to
	// re-review the same commit indefinitely (see theburrowhub/heimdallm#139).
	// If the last stored review is for the same HEAD SHA, return it unchanged.
	//
	prevReview, _ := p.store.LatestReviewForPR(prID)
	// A superseded pending review was generated for an older HEAD while the
	// repo was temporarily disabled. It was never published, so it must not
	// act like a completed prior review and require a fresh review_requested
	// event. Treat it as absent; the current request can evaluate the new HEAD.
	// Permanent/orphaned rows use a different sentinel and retain the existing
	// fail-closed dedup behavior.
	if prevReview != nil && prevReview.GitHubReviewID == SupersededReviewID {
		slog.Info("pipeline: ignoring superseded unpublished review",
			"repo", pr.Repo, "pr", pr.Number, "review_id", prevReview.ID,
			"review_head_sha", prevReview.HeadSHA, "head_sha", pr.Head.SHA)
		prevReview = nil
	}
	// Legacy rows (before the head_sha column was populated) have empty
	// HeadSHA and would otherwise bypass the guard because "" never equals a
	// real SHA. Treat as "cannot confirm safe" — backfill the column from the
	// current snapshot and skip. The user can trigger a re-review manually if
	// they want one, but we never spend Claude credits on a legacy row whose
	// dedup state is ambiguous.
	// Both skip paths return (nil, nil) — the same contract the gate-skip
	// branch above uses. Returning prevReview here was the source of the
	// activity-log spam observed in #322 (Bug 4): the caller in
	// cmd/heimdallm/main.go has a defensive `if rev == nil { return }` that
	// suppresses the EventReviewCompleted SSE / "review done" log /
	// activity_log row when Run does not produce a fresh review. Returning a
	// non-nil prevReview bypassed that filter and made every poll cycle on a
	// stable PR look like a brand-new review in every UI surface, even though
	// no Claude credits were spent.
	if prevReview != nil && prevReview.HeadSHA == "" && pr.Head.SHA != "" {
		// Backfill the empty HeadSHA on the legacy row REGARDLESS of Force:
		// the backfill write and the re-review skip are independent. A forced
		// run that proceeds must not leave the ambiguous legacy row untouched
		// (if it then fails before storing a fresh review, the row would still
		// read as "cannot confirm safe" on the next automatic cycle).
		slog.Info("pipeline: backfilling empty HeadSHA on legacy review row",
			"repo", pr.Repo, "pr", pr.Number, "review_id", prevReview.ID,
			"head_sha", pr.Head.SHA, "forced", opts.Force)
		if err := p.store.UpdateReviewHeadSHA(prevReview.ID, pr.Head.SHA); err != nil {
			slog.Warn("pipeline: failed to backfill HeadSHA",
				"review_id", prevReview.ID, "err", err)
		}
		// Only the automatic path skips here; a forced re-review proceeds.
		if !opts.Force {
			p.publishSkipped(pr, SkipReasonLegacyBackfill)
			return nil, nil
		}
	}

	// 2c. Process persistent-instruction directives (#383) BEFORE the
	// re-review skip gate so a remember/forget takes effect even on cycles
	// that will not produce a review. Opt-in: only when the repo configures an
	// instruction-author allowlist, so unconfigured repos pay no extra fetch.
	var prComments []github.Comment
	if len(opts.InstructionAuthors) > 0 {
		if cs, err := p.gh.FetchComments(pr.Repo, pr.Number); err != nil {
			slog.Warn("pipeline: fetch comments for directives failed", "err", err, "repo", pr.Repo, "pr", pr.Number)
		} else {
			prComments = cs
			if stopped, err := p.stopIfRepoBecameIneligible(pr, opts.RepoEligible); stopped {
				return nil, err
			}
			p.processDirectives(pr, prComments, opts.InstructionAuthors)
		}
	}

	if opts.Force {
		// Explicit operator-initiated review (app "Re-review" button).
		// Bypasses the SHA/re-request dedup gate below AND the circuit
		// breaker (see RunOptions.Force). Logged unconditionally — NOT gated
		// on prevReview — because the breaker bypass at the check further
		// down also covers a forced FIRST review whose per-repo cap is
		// tripped, which would otherwise be suppressed silently in a
		// cost-sensitive path.
		slog.Info("pipeline: forced review — bypassing re-request dedup + circuit breaker",
			"repo", pr.Repo, "pr", pr.Number, "head_sha", pr.Head.SHA,
			"has_prev_review", prevReview != nil)
	}
	if !opts.Force && prevReview != nil && pr.Head.SHA != "" {
		// Regardless of whether the HEAD SHA changed, the bot must not
		// re-review unless the operator explicitly re-requested it. The
		// SHA-unchanged half is the original #322 Bug 5 / #245
		// behaviour: cross-instance updated_at bumps must not trigger
		// re-review. The SHA-changed half closes theburrowhub/heimdallm#509:
		// when a target repo auto-re-adds the bot to requested_reviewers
		// after a push (Dismiss stale reviews on push, CODEOWNERS
		// auto-request workflows, …), the Tier 2 ReviewRequestedFor
		// gate (#385) lets the PR through even though no human asked
		// for a new review. The shared bypass predicate consults the
		// PR timeline for a review_requested event newer than the
		// previous review.
		//
		// Decision rule (same for both SHA states): proceed iff the
		// most recent review_requested or review_dismissed event for
		// the bot login is a review_requested newer than
		// prevReview.CreatedAt. A later review_dismissed (or any other
		// state) cancels the bypass — dismiss-then-no-new-request
		// means the operator no longer wants our review. Fail-closed
		// posture (same as #245 and #322): a missing dependency or
		// timeline API error keeps the skip in place rather than
		// widening the cost surface on a transient outage.
		switch {
		case p.shouldBypassSHASkipForReReview(pr, prevReview):
			slog.Info("pipeline: explicit re-request detected — proceeding with review",
				"repo", pr.Repo, "pr", pr.Number,
				"prev_head_sha", prevReview.HeadSHA, "head_sha", pr.Head.SHA)
		case p.shouldReReviewNewCommitsAsRequestedReviewer(pr, prevReview):
			// New unreviewed commits AND the bot is a current requested
			// reviewer, but GitHub emitted no review_requested timeline event
			// (see shouldReReviewNewCommitsAsRequestedReviewer / #1532). Treat
			// the pending review request on new code as an implicit re-request.
			slog.Info("pipeline: new commits + bot is a requested reviewer — proceeding with review",
				"repo", pr.Repo, "pr", pr.Number,
				"prev_head_sha", prevReview.HeadSHA, "head_sha", pr.Head.SHA)
		default:
			reason := SkipReasonSHAUnchanged
			if prevReview.HeadSHA != pr.Head.SHA {
				reason = SkipReasonNoReReviewRequest
			}
			slog.Info("pipeline: skipping re-review, no explicit re-request",
				"repo", pr.Repo, "pr", pr.Number,
				"prev_head_sha", prevReview.HeadSHA, "head_sha", pr.Head.SHA,
				"reason", string(reason))
			p.publishSkipped(pr, reason)
			return nil, nil
		}
	}

	// 2d. Fetch PR comments for context (non-fatal: proceed without if unavailable).
	// prComments may already be populated by the directive-fetch above; reuse it
	// to avoid a duplicate API call.
	if prComments == nil {
		cs, err := p.gh.FetchComments(pr.Repo, pr.Number)
		if err != nil {
			slog.Warn("pipeline: failed to fetch PR comments, proceeding without", "err", err)
			cs = nil
		}
		prComments = cs
	}
	commentsSection := formatComments(prComments, pr.User.Login)

	// 2e. Build re-review context if a previous review exists for this PR.
	var reviewCtx string
	if prevReview != nil {
		reviewCtx = buildReviewContext(
			prevReview.Issues,
			prevReview.Severity,
			prevReview.CreatedAt,
			prComments,
			p.botLogin,
			pr.User.Login,
		)
	}

	// 3. Build prompt:
	//    Priority: repo override > agent-level prompt > globally active default > built-in default
	promptTemplate := executor.DefaultTemplate()
	var cliFlags string
	p.applyPrompt(promptOverride, opts.AgentPromptID, &promptTemplate, &cliFlags)
	var standing string
	if instr, err := p.store.ListRepoInstructions(pr.Repo); err != nil {
		slog.Warn("pipeline: list repo instructions for prompt failed", "err", err, "repo", pr.Repo)
	} else {
		standing = formatStandingInstructions(instr)
	}
	prompt := executor.BuildPromptFromTemplate(promptTemplate, executor.PRContext{
		Title:                pr.Title,
		Number:               pr.Number,
		Repo:                 pr.Repo,
		Author:               pr.User.Login,
		Link:                 pr.HTMLURL,
		Diff:                 diff,
		Comments:             commentsSection,
		ReviewContext:        reviewCtx,
		StandingInstructions: standing,
	})

	// 4. Select CLI (profile can override the global primary/fallback)
	cli, err := p.executor.Detect(primary, fallback)
	_ = cliFlags // passed to Execute below
	if err != nil {
		return nil, fmt.Errorf("pipeline: detect CLI: %w", err)
	}
	slog.Info("pipeline: using CLI", "cli", cli)

	// 4b. Circuit breaker: hard cap on review count per PR HEAD / per repo.
	// Runs AFTER all dedup layers so it only fires when the dedup failed but
	// the caller is about to spend Claude credits anyway. See
	// theburrowhub/heimdallm#243.
	if !opts.Force && p.breaker != nil {
		tripped, reason, err := p.store.CheckCircuitBreaker(prID, pr.Repo, targetSHA, *p.breaker)
		if err != nil {
			slog.Warn("pipeline: circuit breaker check failed, proceeding", "err", err)
		} else if tripped {
			slog.Error("pipeline: CIRCUIT BREAKER TRIPPED — skipping review",
				"repo", pr.Repo, "pr", pr.Number, "reason", reason)
			p.notify.Notify("Heimdallm circuit breaker",
				fmt.Sprintf("%s #%d: %s", pr.Repo, pr.Number, reason))
			return nil, &CircuitBreakerError{Reason: reason}
		}
	}

	// All early-exit paths above are exhausted (gate, SHA-skip,
	// legacy-backfill, circuit breaker): from this point we are committed
	// to running the CLI and posting a real review. Both the desktop
	// notification AND the lifecycle SSEs (pr_detected / review_started)
	// fire here, NOT at the top of Run and NOT before the breaker check,
	// because the UI stack consumes review_started the instant it lands
	// — the Flutter dashboard marks the PR as "reviewing" and triggers a
	// desktop notification of its own (see #322 Bugs 3+4). Emitting on
	// any path that does NOT proceed to Execute would leave a phantom
	// spinner and a phantom desktop notification per poll cycle. Caller
	// already wraps the CircuitBreakerError into its own SSE event, so
	// the breaker-trip path remains observable without a bogus
	// review_started preceding it.
	if stopped, err := p.stopIfRepoBecameIneligible(pr, opts.RepoEligible); stopped {
		return nil, err
	}
	p.notify.Notify("PR Review Started", fmt.Sprintf("%s #%d", pr.Repo, pr.Number))
	p.publish(sse.EventPRDetected, map[string]any{
		"pr_number": pr.Number,
		"repo":      pr.Repo,
	})
	p.publish(sse.EventReviewStarted, map[string]any{
		"pr_number": pr.Number,
		"repo":      pr.Repo,
	})

	// 5. Execute review (merge cliFlags from prompt into ExecOptions.ExtraFlags)
	// Validate cliFlags from the prompt profile against the selected provider's
	// execution policy — a stored prompt must not override sandbox, approval,
	// permission or workspace guards.
	execOpts := executor.OptionsForSelectedCLI(primary, cli, opts.ExecOpts)
	if cliFlags != "" && execOpts.ExtraFlags == "" {
		migratedOpts, err := applyStoredProfileCLIFlags(cli, cliFlags, execOpts)
		if err != nil {
			slog.Warn("pipeline: prompt cli_flags rejected by execution policy, ignoring", "err", err)
			// Don't abort the review — just skip the unsafe flags
		} else {
			execOpts = migratedOpts
		}
	}
	result, err := p.executor.Execute(cli, prompt, execOpts)
	if err != nil {
		return nil, fmt.Errorf("pipeline: execute %s: %w", cli, err)
	}

	// 5b. Revalidate the live HEAD after analysis and before any GitHub write.
	// The worktree and diff remain a coherent snapshot even when a push lands
	// during Execute, but findings for the superseded commit must not be emitted
	// as current PR feedback. A subsequent poll/re-request will create a fresh
	// execution and worktree for the replacement SHA.
	currentSHA, err := p.gh.GetPRHeadSHA(pr.Repo, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("pipeline: revalidate HEAD SHA after execute: %w", err)
	}
	currentSHA = strings.TrimSpace(currentSHA)
	if currentSHA == "" {
		return nil, fmt.Errorf("pipeline: revalidate HEAD SHA after execute: empty SHA returned")
	}
	if currentSHA != targetSHA {
		slog.Info("pipeline: HEAD changed during analysis, discarding stale review",
			"repo", pr.Repo, "pr", pr.Number,
			"review_head_sha", targetSHA, "current_head_sha", currentSHA)
		p.publishSkipped(pr, SkipReasonHeadChanged)
		return nil, nil
	}

	// 5c. Reconcile severity: ensure top-level severity >= max(issues[].severity).
	// Guards against LLM inconsistencies (prompt injection, model errors).
	reconciledSeverity := ReconcileSeverity(result, "repo", pr.Repo, "pr", pr.Number)

	// 5d. Extract comment signals and apply escalation to the severity that
	// will be persisted. By folding signal-driven escalation into the stored
	// severity, retry paths (PublishPending, NATS worker) reproduce the same
	// APPROVE/REQUEST_CHANGES decision without needing to re-extract signals.
	//
	// Filter out the bot's own comments before extraction — Heimdallm's prior
	// review bodies often contain blocker keywords ("security issue",
	// "must fix", etc.) that would otherwise self-trigger escalation.
	signalComments := filterBotComments(prComments, p.botLogin)
	commentSignals := ExtractCommentSignals(signalComments, pr.User.Login)
	finalSeverity := ApplySignalEscalation(reconciledSeverity, commentSignals)
	reviewEvent := ReviewEvent(finalSeverity, MaxIssueSeverity(result.Issues),
		opts.NeverApproveWithIssues, opts.NeverApproveMinSeverity)

	// 6. Marshal issues to JSON for storage
	issuesJSON, err := json.Marshal(result.Issues)
	if err != nil {
		return nil, fmt.Errorf("pipeline: marshal issues: %w", err)
	}

	// 7. Store review in SQLite first (backup before publishing).
	// Suggestions were dropped from the review agent (a review either raises an
	// issue or it doesn't); the DB column is retained for backward compatibility
	// with historical rows and always written as an empty array.
	rev := &store.Review{
		PRID:           prID,
		CLIUsed:        cli,
		Summary:        result.Summary,
		Issues:         string(issuesJSON),
		Suggestions:    "[]",
		Severity:       finalSeverity,
		Event:          reviewEvent,
		CreatedAt:      time.Now().UTC(),
		GitHubReviewID: 0, // will be set after GitHub publish
		HeadSHA:        targetSHA,
	}
	rev.ID, err = p.store.InsertReview(rev)
	if err != nil {
		return nil, fmt.Errorf("pipeline: store review: %w", err)
	}
	slog.Info("pipeline: review stored locally", "review_id", rev.ID)
	if stopped, err := p.stopIfRepoBecameIneligible(pr, opts.RepoEligible); stopped {
		return nil, err
	}

	// 8. Publish review to GitHub
	var reviewBody string
	if reviewMode == "multi" && len(result.Issues) > 0 {
		// Post one comment per issue (best-effort — failures are logged but don't abort)
		for _, issue := range result.Issues {
			if stopped, err := p.stopIfRepoBecameIneligible(pr, opts.RepoEligible); stopped {
				return nil, err
			}
			if _, err := p.gh.PostComment(pr.Repo, pr.Number, buildIssueComment(issue)); err != nil {
				slog.Warn("pipeline: failed to post issue comment", "pr", pr.Number, "err", err)
			}
		}
		reviewBody = buildMultiSummaryBody(result)
	} else {
		reviewBody = BuildGitHubBody(result)
	}

	if stopped, err := p.stopIfRepoBecameIneligible(pr, opts.RepoEligible); stopped {
		return nil, err
	}
	ghReviewID, ghReviewState, publishErr := p.gh.SubmitReviewForCommit(
		pr.Repo, pr.Number,
		AnnotateBodyForEvent(reviewBody, reviewEvent, len(result.Issues)),
		reviewEvent,
		targetSHA,
	)
	if publishErr != nil {
		// Permanent submit failure (PR locked etc.): mark the freshly
		// stored row as orphaned right now so it never enters the
		// PublishPending retry loop. Transient errors fall through to
		// the existing retry path. The orphan-marking pattern is
		// shared with PublishPending via markOrphanIfPermanent so
		// both sites stay in sync if the sentinel convention or
		// logging shape ever changes. See theburrowhub/heimdallm#325.
		if !p.markOrphanIfPermanent(rev.ID, publishErr, "initial publish") {
			// Transient — review saved locally; will retry on next poll (GitHubReviewID == 0 check)
			slog.Warn("pipeline: failed to publish to GitHub, will retry",
				"pr", pr.Number, "err", publishErr)
		}
	} else {
		// Stamp PublishedAt immediately after the API returned success — this
		// is the anchor the dedup window uses. Anchoring on CreatedAt (set
		// before Claude ran) is what let #243 loop repeatedly.
		//
		// Only mirror the successful write onto the in-memory *Review when
		// MarkReviewPublished actually persisted — otherwise a future caller
		// that trusts rev.PublishedAt for a persistence decision would make
		// a choice inconsistent with SQLite. Today no caller does that, but
		// keeping the two views in lockstep closes the latent trap.
		publishedAt := time.Now().UTC()
		if err := p.store.MarkReviewPublished(rev.ID, ghReviewID, ghReviewState, publishedAt); err != nil {
			slog.Warn("pipeline: failed to mark review published", "review_id", rev.ID, "err", err)
		} else {
			rev.PublishedAt = publishedAt
			rev.GitHubReviewID = ghReviewID
			rev.GitHubReviewState = ghReviewState
		}
		slog.Info("pipeline: review published to GitHub",
			"pr", pr.Number,
			"github_review_id", ghReviewID,
			"github_review_state", ghReviewState)
	}

	p.notify.Notify("PR Review Complete",
		fmt.Sprintf("%s #%d — severity: %s", pr.Repo, pr.Number, result.Severity))

	slog.Info("pipeline: review complete", "pr", pr.Number, "severity", result.Severity)
	p.publish(sse.EventReviewCompleted, map[string]any{
		"pr_number": pr.Number,
		"repo":      pr.Repo,
		"pr_id":     rev.PRID,
		"severity":  rev.Severity,
	})
	return rev, nil
}

// orphanedReviewID is the sentinel github_review_id stored for a review that
// can never be published (no repo, corrupt stored JSON, or a permanent submit
// failure). It marks the row as resolved so PublishPending stops retrying it,
// while being distinguishable from a real GitHub review id.
const orphanedReviewID = -1

// SupersededReviewID marks an unpublished review whose analysed HEAD changed
// before a deferred retry could publish it. Unlike orphanedReviewID, this
// sentinel is intentionally ignored by the next pipeline run so an outstanding
// review request can evaluate the replacement commit without an artificial
// re-request. It is exported for the NATS publish worker in cmd/heimdallm.
const SupersededReviewID int64 = -2

// markOrphanIfPermanent inspects the error returned by SubmitReview and,
// when it is a *github.PermanentSubmitError, marks the local review row
// as orphaned via the (-1, "") sentinel that PublishPending also uses
// for PRs with no repo. Returns true when the error was permanent and
// the orphan-marking attempt was made (regardless of whether
// MarkReviewPublished itself succeeded — a store failure here is logged
// at Warn so the retry loop can re-attempt next tick).
//
// Returns false for transient or unknown errors so the caller falls
// back to its existing retry logging. Centralising this keeps the Run
// and PublishPending paths in lockstep — without the helper a future
// edit to the sentinel convention or log shape would silently drift
// between the two sites. See theburrowhub/heimdallm#325 review.
func (p *Pipeline) markOrphanIfPermanent(reviewID int64, submitErr error, source string) bool {
	var permErr *github.PermanentSubmitError
	if !errors.As(submitErr, &permErr) {
		return false
	}
	if mErr := p.store.MarkReviewPublished(reviewID, orphanedReviewID, "", time.Now().UTC()); mErr != nil {
		slog.Warn("pipeline: failed to mark orphaned review, will retry next tick",
			"review_id", reviewID, "source", source, "reason", permErr.Reason, "err", mErr)
		return true
	}
	slog.Info("pipeline: review marked orphan (permanent submit failure, will not retry)",
		"review_id", reviewID, "source", source, "reason", permErr.Reason, "status", permErr.StatusCode)
	return true
}

// PublishPending re-submits locally stored reviews that failed to publish to GitHub.
// Call this on scheduler ticks to retry failed publications.
func (p *Pipeline) PublishPending() {
	reviews, err := p.store.ListUnpublishedReviews()
	if err != nil || len(reviews) == 0 {
		return
	}
	for _, rev := range reviews {
		pr, err := p.store.GetPR(rev.PRID)
		if err != nil {
			continue
		}
		// Skip reviews for PRs with no repo — orphaned records that will never publish.
		// Mark them as permanently published (orphanedReviewID, empty state) to stop retry noise.
		if pr.Repo == "" {
			_ = p.store.MarkReviewPublished(rev.ID, orphanedReviewID, "", time.Now().UTC())
			slog.Info("pipeline: skipping pending review for PR with no repo", "review_id", rev.ID)
			continue
		}
		// Legacy rows without HeadSHA have no provable commit provenance. Never
		// let GitHub default them to the PR's current HEAD: that could attach
		// findings generated from unknown code to a newer commit. Retain the row
		// locally but retire it from the retry queue.
		if strings.TrimSpace(rev.HeadSHA) == "" {
			if err := p.store.MarkReviewPublished(rev.ID, orphanedReviewID, "", time.Now().UTC()); err != nil {
				slog.Warn("pipeline: failed to orphan pending review with empty HeadSHA, will retry next tick",
					"review_id", rev.ID, "err", err)
			} else {
				slog.Info("pipeline: pending review has no HeadSHA, marking orphaned",
					"review_id", rev.ID, "repo", pr.Repo, "pr", pr.Number)
			}
			continue
		}
		// Rebuild a minimal result from stored JSON for the body. The daemon
		// always writes well-formed JSON here, so a decode failure means the
		// stored row is corrupt and the issue list is unrecoverable. Don't
		// publish a misleading review missing its findings, and don't retry a
		// deterministic failure forever — orphan it like the no-repo case above.
		var issues []executor.Issue
		if err := json.Unmarshal([]byte(rev.Issues), &issues); err != nil {
			slog.Warn("pipeline: skipping pending review with corrupt issues JSON",
				"review_id", rev.ID, "pr", pr.Number, "repo", pr.Repo, "err", err)
			if mErr := p.store.MarkReviewPublished(rev.ID, orphanedReviewID, "", time.Now().UTC()); mErr != nil {
				slog.Warn("pipeline: failed to orphan corrupt review, will retry next tick",
					"review_id", rev.ID, "err", mErr)
			}
			continue
		}
		result := &executor.ReviewResult{
			Summary:  rev.Summary,
			Issues:   issues,
			Severity: rev.Severity,
		}
		// PublishPending always uses single-mode body (individual comments were
		// already posted when the review first ran; we only retry the formal review).
		// The stored event already incorporates signal escalation and the
		// never-approve-with-issues decision from the initial review; legacy
		// rows without a stored event fall back to SeverityToEvent via
		// publishEventFor.
		retryEvent := PublishEventFor(rev)
		ghID, ghState, err := p.gh.SubmitReviewForCommit(
			pr.Repo, pr.Number,
			AnnotateBodyForEvent(BuildGitHubBody(result), retryEvent, len(result.Issues)),
			retryEvent,
			rev.HeadSHA,
		)
		if err != nil {
			// Permanent submit failures (currently HTTP 422 "lock
			// prevents review") are routed through the shared helper so
			// both the Run path and this retry path apply the same
			// orphan-marker, sentinel value and log shape. See
			// theburrowhub/heimdallm#325.
			if p.markOrphanIfPermanent(rev.ID, err, "retry publish") {
				continue
			}
			slog.Warn("pipeline: retry publish failed", "review_id", rev.ID, "err", err)
			continue
		}
		// Stamp the retry's PublishedAt so dedup anchors on the actual
		// post-to-GitHub time (not the original CreatedAt), matching the
		// Run() path. See theburrowhub/heimdallm#243.
		//
		// Surface MarkReviewPublished errors: losing this write leaves the
		// dedup with no anchor for the retry, so the next poll cycle could
		// re-review the same commit. Operators need the log line to
		// diagnose that class of regression.
		if err := p.store.MarkReviewPublished(rev.ID, ghID, ghState, time.Now().UTC()); err != nil {
			slog.Warn("pipeline: failed to mark pending review published, dedup anchor missing",
				"review_id", rev.ID, "err", err)
		}
		slog.Info("pipeline: pending review published",
			"review_id", rev.ID,
			"github_review_id", ghID,
			"github_review_state", ghState)
	}
}

// buildIssueComment formats a single issue as a standalone PR comment (multi-feedback mode).
func buildIssueComment(issue executor.Issue) string {
	icon := "⚠️"
	sev := "MEDIUM"
	switch issue.Severity {
	case "high":
		icon = "🔴"
		sev = "HIGH"
	case "low":
		icon = "🟡"
		sev = "LOW"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## %s %s Issue\n\n", icon, sev))
	sb.WriteString(issue.Description)
	if issue.File != "" {
		sb.WriteString("\n\n**Location:** `")
		sb.WriteString(issue.File)
		sb.WriteString("`")
		if issue.Line > 0 {
			sb.WriteString(fmt.Sprintf(" line %d", issue.Line))
		}
	}
	sb.WriteString("\n\n---\n*Posted by Heimdallm AI Review*")
	return sb.String()
}

// buildMultiSummaryBody formats the final summary review body used in multi-feedback mode.
// Individual issues are already posted as separate comments; this is the formal review summary.
func buildMultiSummaryBody(r *executor.ReviewResult) string {
	var sb strings.Builder
	sb.WriteString("## 🤖 Heimdallm AI Review — Summary\n\n")
	sb.WriteString(r.Summary)
	sb.WriteString("\n\n")
	if len(r.Issues) > 0 {
		sb.WriteString(fmt.Sprintf("**%d issue(s) found** — see individual comments above for details.\n\n", len(r.Issues)))
	}
	sb.WriteString(fmt.Sprintf("---\n*Severity: **%s** · Reviewed by Heimdallm*",
		strings.ToUpper(r.Severity)))
	return sb.String()
}

// BuildGitHubBody formats the AI review as a GitHub-flavored markdown review body.
func BuildGitHubBody(r *executor.ReviewResult) string {
	var sb strings.Builder
	sb.WriteString("## 🤖 Heimdallm AI Review\n\n")
	sb.WriteString(r.Summary)
	sb.WriteString("\n\n")

	if len(r.Issues) > 0 {
		sb.WriteString("### Issues\n\n")
		for _, issue := range r.Issues {
			icon := "⚠️"
			if issue.Severity == "high" {
				icon = "🔴"
			} else if issue.Severity == "low" {
				icon = "🟡"
			}
			sb.WriteString(fmt.Sprintf("%s **%s:%d** — %s\n",
				icon, issue.File, issue.Line, issue.Description))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("---\n*Severity: **%s** · Reviewed by %s*",
		strings.ToUpper(r.Severity), "Heimdallm"))
	return sb.String()
}

// severityRank maps a severity string to a numeric rank for comparison.
func severityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high":
		return 3
	case "medium":
		return 2
	default:
		return 1
	}
}

// isCanonicalSeverity reports whether s is one of the severities the agent
// prompt mandates (low|medium|high), case-insensitive.
func isCanonicalSeverity(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

// rankToSeverity converts a numeric rank back to a severity string.
func rankToSeverity(r int) string {
	switch r {
	case 3:
		return "high"
	case 2:
		return "medium"
	default:
		return "low"
	}
}

// ReconcileSeverity ensures the top-level severity is at least as high as
// the maximum severity found in individual issues. This guards against LLM
// inconsistencies where issues are flagged high but the global field is set
// low (prompt injection, model error, hallucination).
//
// logAttrs are appended verbatim to any warn line this function emits, so the
// caller can thread a correlation id (e.g. "repo", pr.Repo, "pr", pr.Number)
// to tie a reconciliation warning back to the specific review that triggered
// it. They are key/value pairs in slog's variadic form; pass none to omit.
func ReconcileSeverity(result *executor.ReviewResult, logAttrs ...any) string {
	// Fail-safe on the top-level severity. The agent prompt mandates one of
	// low|medium|high; a non-canonical value ("critical", "blocker", or model
	// garbage) would otherwise fall through severityRank's default to rank 1
	// ("low") and silently APPROVE a PR the model meant to block. Treat any
	// unrecognized top-level severity as "high" so a human reviews it. Per-issue
	// severities stay tolerant (unknown → low) so a stray "nit"/"info" label
	// cannot escalate the whole review.
	//
	// An empty top-level severity is intentionally NOT failed-safe: parseResult
	// coerces a missing severity to "low" before this runs, and an omitted
	// severity is the documented "no issues" default — not a blocked escalation
	// (the #547 failure mode). Escalating empty would over-block clean reviews.
	nonCanonical := result.Severity != "" && !isCanonicalSeverity(result.Severity)
	var maxRank int
	if nonCanonical {
		slog.Warn("pipeline: non-canonical top-level severity from agent; failing safe to high",
			append([]any{"ai_severity", result.Severity}, logAttrs...)...)
		maxRank = severityRank("high")
	} else {
		maxRank = severityRank(result.Severity)
	}
	for _, iss := range result.Issues {
		if r := severityRank(iss.Severity); r > maxRank {
			maxRank = r
		}
	}
	reconciled := rankToSeverity(maxRank)
	// Only emit the "AI inconsistency" warning for the case it actually
	// describes — a canonical top-level raised by a higher per-issue severity.
	// The non-canonical path already logged its own accurate warning above, so
	// guarding here avoids a misleading double log on the same call.
	if !nonCanonical && reconciled != result.Severity {
		slog.Warn("pipeline: severity reconciled (AI inconsistency)",
			append([]any{
				"ai_severity", result.Severity,
				"reconciled", reconciled,
				"issue_count", len(result.Issues),
			}, logAttrs...)...)
	}
	return reconciled
}

// ApplySignalEscalation elevates severity based on comment signals.
// A "medium" severity is escalated to "high" when blocker signals are detected.
// This ensures the escalated severity is persisted, so retry paths reproduce
// the same decision without re-extracting signals.
func ApplySignalEscalation(severity string, signals CommentSignals) string {
	if severity == "medium" && signals.Urgency >= 3 {
		slog.Info("pipeline: severity escalated by comment signals",
			"original", severity, "escalated", "high",
			"has_blocker_keywords", signals.HasBlockerKeywords,
			"unresolved_concerns", signals.UnresolvedConcerns)
		return "high"
	}
	return severity
}

// SeverityToEvent maps severity to a GitHub review event type.
// Only high-severity issues block a PR — Heimdallm must not be a blocker
// for medium/low issues. Signal-driven escalation is applied upstream via
// ApplySignalEscalation before the severity reaches this function.
func SeverityToEvent(severity string) string {
	if severity == "high" {
		return "REQUEST_CHANGES"
	}
	return "APPROVE"
}

// MaxIssueSeverity returns the highest severity among the review's findings,
// or "" when the review found none. Non-canonical severities rank as "low",
// mirroring severityRank's default.
func MaxIssueSeverity(issues []executor.Issue) string {
	if len(issues) == 0 {
		return ""
	}
	maxRank := severityRank("low")
	for _, iss := range issues {
		if r := severityRank(iss.Severity); r > maxRank {
			maxRank = r
		}
	}
	return rankToSeverity(maxRank)
}

// ReviewEvent decides the GitHub review event, honoring the
// never-approve-with-issues setting. It builds on SeverityToEvent: when the
// base decision would be APPROVE, the setting is on, and the review found at
// least one issue of severity >= minSeverity, it downgrades APPROVE to
// COMMENT. REQUEST_CHANGES is never altered, and a clean review (no issues)
// still approves.
//
// maxIssueSeverity is MaxIssueSeverity(issues): "" means no findings.
// minSeverity is the never_approve_min_severity setting; empty means "low"
// (any finding downgrades — the pre-#597 behavior), matching severityRank's
// default rank for unknown values.
func ReviewEvent(finalSeverity, maxIssueSeverity string, neverApproveWithIssues bool, minSeverity string) string {
	event := SeverityToEvent(finalSeverity)
	if event == "APPROVE" && neverApproveWithIssues && maxIssueSeverity != "" &&
		severityRank(maxIssueSeverity) >= severityRank(minSeverity) {
		return "COMMENT"
	}
	return event
}

// PublishEventFor returns the GitHub event to submit for a stored review:
// the decided event persisted at review time, or — for legacy rows written
// before the event column existed — the severity-derived fallback. Exported so
// every publish path (Run, PublishPending, the NATS publish-worker) reproduces
// the persisted decision with the same legacy fallback.
func PublishEventFor(rev *store.Review) string {
	if rev.Event != "" {
		return rev.Event
	}
	return SeverityToEvent(rev.Severity)
}

// downgradeNoteFor is appended to a review body when the event was downgraded
// to COMMENT, so PR authors understand why Heimdallm commented instead of
// approving. COMMENT is only ever produced by ReviewEvent's
// never-approve-with-issues downgrade, so keying on the event is sufficient.
// It deliberately says "review finding(s)", not "issues": "issues were found"
// was misread as "no GitHub issue is linked to this PR" (#597).
//
// findingCount is the TOTAL number of findings listed above the note, not
// just those at or above never_approve_min_severity. The retry publish paths
// (PublishPending, the NATS publish-worker) rebuild the note from the stored
// review, which persists the decided event but not the threshold in effect
// at decision time — re-reading the live config there could drift from the
// original decision. The total matches the visible list, so it is always
// accurate; "the blocking findings" carries the threshold nuance instead.
func downgradeNoteFor(findingCount int) string {
	findings := "findings"
	if findingCount == 1 {
		findings = "finding"
	}
	return fmt.Sprintf("\n\n---\n_Not approving: this review raised %d %s "+
		"above, and `never_approve_with_issues` is enabled for this repo, so "+
		"it is posted as a comment instead of an approval. Address or dispute "+
		"the blocking findings and re-request a review to get an approval._",
		findingCount, findings)
}

// AnnotateBodyForEvent appends an explanatory note to the review body when the
// event is COMMENT (the never-approve-with-issues downgrade); otherwise the
// body is returned unchanged. findingCount is the number of findings the
// review raised, quoted in the note.
func AnnotateBodyForEvent(body, event string, findingCount int) string {
	if event == "COMMENT" {
		return body + downgradeNoteFor(findingCount)
	}
	return body
}

// maxCommentsBytes limits the total formatted PR comments included in the prompt.
const maxCommentsBytes = 16 * 1024 // 16KB

// filterBotComments returns a new slice excluding comments authored by the bot.
// This prevents Heimdallm's own prior review bodies (which contain keywords like
// "security issue", "must fix", etc.) from self-triggering blocker detection.
func filterBotComments(comments []github.Comment, botLogin string) []github.Comment {
	if botLogin == "" {
		return comments
	}
	filtered := make([]github.Comment, 0, len(comments))
	for _, c := range comments {
		if !strings.EqualFold(c.Author, botLogin) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// formatComments formats a slice of GitHub comments into a prompt section string.
// Returns empty string if comments is nil or empty.
// If total formatted text exceeds maxCommentsBytes, trims comments before the
// PR author's last message. If still too large, hard-truncates with a note.
func formatComments(comments []github.Comment, prAuthor string) string {
	if len(comments) == 0 {
		return ""
	}

	lines := make([]string, len(comments))
	for i, c := range comments {
		if c.File != "" {
			lines[i] = fmt.Sprintf("@%s (%s:%d): %s", c.Author, c.File, c.Line, c.Body)
		} else {
			lines[i] = fmt.Sprintf("@%s: %s", c.Author, c.Body)
		}
	}

	formatted := strings.Join(lines, "\n---\n")
	if len(formatted) <= maxCommentsBytes {
		return wrapCommentsSection(formatted)
	}

	// Find the last comment by the PR author and trim everything before it
	lastAuthorIdx := -1
	for i := len(comments) - 1; i >= 0; i-- {
		if comments[i].Author == prAuthor {
			lastAuthorIdx = i
			break
		}
	}

	start := 0
	if lastAuthorIdx > 0 {
		start = lastAuthorIdx
	}

	trimmed := strings.Join(lines[start:], "\n---\n")
	if len(trimmed) <= maxCommentsBytes {
		return wrapCommentsSection(trimmed)
	}

	return wrapCommentsSection(trimmed[:maxCommentsBytes] + "\n... (truncated)")
}

func wrapCommentsSection(text string) string {
	return "Existing PR discussion:\n<user_content>\n" + text + "\n</user_content>"
}
