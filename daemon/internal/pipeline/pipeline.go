package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

// DiffFetcher retrieves the diff for a pull request.
type DiffFetcher interface {
	FetchDiff(repo string, number int) (string, error)
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
	SubmitReview(repo string, number int, body, event string) (int64, string, error)
	// PostComment posts a general PR comment (used in multi-feedback mode).
	PostComment(repo string, number int, body string) (time.Time, error)
}

// CommentFetcher retrieves PR comments for context injection into the AI prompt.
type CommentFetcher interface {
	FetchComments(repo string, number int) ([]github.Comment, error)
}

// Pipeline orchestrates the full PR review flow.
type Pipeline struct {
	store    *store.Store
	gh       interface {
		DiffFetcher
		GitHubReviewer
		CommentFetcher
		HeadSHAResolver
	}
	executor CLIExecutor
	notify   Notifier
	botLogin string
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

	// Defense-in-depth: refuse to run the CLI if the gate rejects this PR.
	// Callers publish the skip event themselves — we only log here so a
	// missed caller-side check is visible in daemon logs.
	if reason := Evaluate(PRGate{
		State:  pr.State,
		Draft:  pr.Draft,
		Author: pr.User.Login,
	}, opts.Guards); reason != SkipReasonNone {
		slog.Warn("pipeline: gate skip (caller did not filter)",
			"repo", pr.Repo, "pr", pr.Number, "reason", string(reason))
		return nil, nil
	}

	p.notify.Notify("PR Review Started", fmt.Sprintf("%s #%d", pr.Repo, pr.Number))

	// 2. Fetch diff
	diff, err := p.gh.FetchDiff(pr.Repo, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("pipeline: fetch diff: %w", err)
	}

	// 2a. Authoritative dedup by HEAD commit SHA. The Tier 2/3 dedup uses
	// updated_at — but any peer reviewer submitting a review (human or another
	// heimdallm instance) bumps updated_at, which would otherwise cause us to
	// re-review the same commit indefinitely (see theburrowhub/heimdallm#139).
	// If the last stored review is for the same HEAD SHA, return it unchanged.
	//
	// The Search Issues API used by Tier 2 does not populate head.sha, so we
	// resolve it on-demand. We must NOT proceed to Execute when we cannot
	// confirm the SHA, because a transient API failure would otherwise bypass
	// the cross-instance dedup and let every peer bot run the review on top
	// of the same commit. See theburrowhub/heimdallm#243.
	if pr.Head.SHA == "" {
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
		pr.Head.SHA = sha
	}
	prevReview, _ := p.store.LatestReviewForPR(prID)
	// Legacy rows (before the head_sha column was populated) have empty
	// HeadSHA and would otherwise bypass the guard because "" never equals a
	// real SHA. Treat as "cannot confirm safe" — backfill the column from the
	// current snapshot and skip. The user can trigger a re-review manually if
	// they want one, but we never spend Claude credits on a legacy row whose
	// dedup state is ambiguous.
	if prevReview != nil && prevReview.HeadSHA == "" && pr.Head.SHA != "" {
		slog.Info("pipeline: backfilling empty HeadSHA on legacy review row, skipping re-review",
			"repo", pr.Repo, "pr", pr.Number, "review_id", prevReview.ID, "head_sha", pr.Head.SHA)
		if err := p.store.UpdateReviewHeadSHA(prevReview.ID, pr.Head.SHA); err != nil {
			slog.Warn("pipeline: failed to backfill HeadSHA",
				"review_id", prevReview.ID, "err", err)
		} else {
			prevReview.HeadSHA = pr.Head.SHA
		}
		return prevReview, nil
	}
	if prevReview != nil && pr.Head.SHA != "" && prevReview.HeadSHA == pr.Head.SHA {
		slog.Info("pipeline: skipping re-review, HEAD SHA unchanged",
			"repo", pr.Repo, "pr", pr.Number, "head_sha", pr.Head.SHA)
		return prevReview, nil
	}

	// 2b. Fetch PR comments for context (non-fatal: proceed without if unavailable)
	prComments, err := p.gh.FetchComments(pr.Repo, pr.Number)
	if err != nil {
		slog.Warn("pipeline: failed to fetch PR comments, proceeding without", "err", err)
		prComments = nil
	}
	commentsSection := formatComments(prComments, pr.User.Login)

	// 2c. Build re-review context if a previous review exists for this PR.
	var reviewCtx string
	if prevReview != nil {
		reviewCtx = buildReviewContext(
			prevReview.Issues,
			prevReview.Severity,
			prevReview.CreatedAt,
			prComments,
			p.botLogin,
		)
	}

	// 3. Build prompt:
	//    Priority: repo override > agent-level prompt > globally active default > built-in default
	promptTemplate := executor.DefaultTemplate()
	var cliFlags string
	p.applyPrompt(promptOverride, opts.AgentPromptID, &promptTemplate, &cliFlags)
	prompt := executor.BuildPromptFromTemplate(promptTemplate, executor.PRContext{
		Title:         pr.Title,
		Number:        pr.Number,
		Repo:          pr.Repo,
		Author:        pr.User.Login,
		Link:          pr.HTMLURL,
		Diff:          diff,
		Comments:      commentsSection,
		ReviewContext: reviewCtx,
	})

	// 4. Select CLI (profile can override the global primary/fallback)
	cli, err := p.executor.Detect(primary, fallback)
	_ = cliFlags // passed to Execute below
	if err != nil {
		return nil, fmt.Errorf("pipeline: detect CLI: %w", err)
	}
	slog.Info("pipeline: using CLI", "cli", cli)

	// 5. Execute review (merge cliFlags from prompt into ExecOptions.ExtraFlags)
	// Validate cliFlags from the prompt profile against the same denylist as
	// ExtraFlags — a stored prompt can otherwise carry forbidden flags like
	// --dangerously-skip-permissions that bypass the CLI agent config guards.
	execOpts := opts.ExecOpts
	if cliFlags != "" && execOpts.ExtraFlags == "" {
		if err := executor.ValidateExtraFlags(cliFlags); err != nil {
			slog.Warn("pipeline: prompt cli_flags rejected by denylist, ignoring", "err", err)
			// Don't abort the review — just skip the unsafe flags
		} else {
			execOpts.ExtraFlags = cliFlags
		}
	}
	result, err := p.executor.Execute(cli, prompt, execOpts)
	if err != nil {
		return nil, fmt.Errorf("pipeline: execute %s: %w", cli, err)
	}

	// 6. Marshal issues and suggestions to JSON for storage
	issuesJSON, err := json.Marshal(result.Issues)
	if err != nil {
		return nil, fmt.Errorf("pipeline: marshal issues: %w", err)
	}
	suggestionsJSON, err := json.Marshal(result.Suggestions)
	if err != nil {
		return nil, fmt.Errorf("pipeline: marshal suggestions: %w", err)
	}

	// 7. Store review in SQLite first (backup before publishing)
	rev := &store.Review{
		PRID:           prID,
		CLIUsed:        cli,
		Summary:        result.Summary,
		Issues:         string(issuesJSON),
		Suggestions:    string(suggestionsJSON),
		Severity:       result.Severity,
		CreatedAt:      time.Now().UTC(),
		GitHubReviewID: 0, // will be set after GitHub publish
		HeadSHA:        pr.Head.SHA,
	}
	rev.ID, err = p.store.InsertReview(rev)
	if err != nil {
		return nil, fmt.Errorf("pipeline: store review: %w", err)
	}
	slog.Info("pipeline: review stored locally", "review_id", rev.ID)

	// 8. Publish review to GitHub
	var reviewBody string
	if reviewMode == "multi" && len(result.Issues) > 0 {
		// Post one comment per issue (best-effort — failures are logged but don't abort)
		for _, issue := range result.Issues {
			if _, err := p.gh.PostComment(pr.Repo, pr.Number, buildIssueComment(issue)); err != nil {
				slog.Warn("pipeline: failed to post issue comment", "pr", pr.Number, "err", err)
			}
		}
		reviewBody = buildMultiSummaryBody(result)
	} else {
		reviewBody = buildGitHubBody(result)
	}

	ghReviewID, ghReviewState, publishErr := p.gh.SubmitReview(
		pr.Repo, pr.Number,
		reviewBody,
		severityToEvent(result.Severity, len(result.Issues)),
	)
	if publishErr != nil {
		// Review saved locally; will retry on next poll (GitHubReviewID == 0 check)
		slog.Warn("pipeline: failed to publish to GitHub, will retry",
			"pr", pr.Number, "err", publishErr)
	} else {
		_ = p.store.MarkReviewPublished(rev.ID, ghReviewID, ghReviewState)
		rev.GitHubReviewID = ghReviewID
		rev.GitHubReviewState = ghReviewState
		slog.Info("pipeline: review published to GitHub",
			"pr", pr.Number,
			"github_review_id", ghReviewID,
			"github_review_state", ghReviewState)
	}

	p.notify.Notify("PR Review Complete",
		fmt.Sprintf("%s #%d — severity: %s", pr.Repo, pr.Number, result.Severity))

	slog.Info("pipeline: review complete", "pr", pr.Number, "severity", result.Severity)
	return rev, nil
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
		// Mark them as permanently published (fake ID -1, empty state) to stop retry noise.
		if pr.Repo == "" {
			_ = p.store.MarkReviewPublished(rev.ID, -1, "")
			slog.Info("pipeline: skipping pending review for PR with no repo", "review_id", rev.ID)
			continue
		}
		// Rebuild a minimal result from stored JSON for the body
		var issues []executor.Issue
		json.Unmarshal([]byte(rev.Issues), &issues)
		result := &executor.ReviewResult{
			Summary:  rev.Summary,
			Issues:   issues,
			Severity: rev.Severity,
		}
		// PublishPending always uses single-mode body (individual comments were
		// already posted when the review first ran; we only retry the formal review).
		ghID, ghState, err := p.gh.SubmitReview(
			pr.Repo, pr.Number,
			buildGitHubBody(result),
			severityToEvent(rev.Severity, len(issues)),
		)
		if err != nil {
			slog.Warn("pipeline: retry publish failed", "review_id", rev.ID, "err", err)
			continue
		}
		_ = p.store.MarkReviewPublished(rev.ID, ghID, ghState)
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
	if len(r.Suggestions) > 0 {
		sb.WriteString("### Suggestions\n\n")
		for _, s := range r.Suggestions {
			sb.WriteString("- " + s + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("---\n*Severity: **%s** · Reviewed by Heimdallm*",
		strings.ToUpper(r.Severity)))
	return sb.String()
}

// buildGitHubBody formats the AI review as a GitHub-flavored markdown review body.
func buildGitHubBody(r *executor.ReviewResult) string {
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

	if len(r.Suggestions) > 0 {
		sb.WriteString("### Suggestions\n\n")
		for _, s := range r.Suggestions {
			sb.WriteString("- " + s + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("---\n*Severity: **%s** · Reviewed by %s*",
		strings.ToUpper(r.Severity), "Heimdallm"))
	return sb.String()
}

// severityToEvent maps severity to a GitHub review event type.
// Only high-severity issues block a PR — Heimdallm must not be a blocker
// for medium/low issues. Those are left as informational comments with an APPROVE.
func severityToEvent(severity string, _ int) string {
	if severity == "high" {
		return "REQUEST_CHANGES"
	}
	return "APPROVE"
}

// maxCommentsBytes limits the total formatted PR comments included in the prompt.
const maxCommentsBytes = 16 * 1024 // 16KB

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
