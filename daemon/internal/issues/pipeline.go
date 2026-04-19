// Package issues hosts the fase-2 issue-tracking pipeline — one step per
// issue triage plus a fetcher that orchestrates batches of issues from a
// repo. This pipeline runs the review_only mode (LLM triage + GitHub
// comment). The auto_implement mode lives in a sibling file shipped with
// issue #27.
package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

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
	PostComment(repo string, number int, body string) error
}

// IssueCommentFetcher fetches the existing discussion for an issue so the
// triage LLM can take prior context into account.
type IssueCommentFetcher interface {
	FetchComments(repo string, number int) ([]github.Comment, error)
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
	CreatePR(repo, title, body, head, base string, draft bool) (int, error)
}

// PRMetadataApplier sets reviewers, labels, and assignees on a PR after creation.
type PRMetadataApplier interface {
	SetPRReviewers(repo string, prNumber int, reviewers []string) error
	AddLabels(repo string, number int, labels []string) error
	SetAssignees(repo string, number int, assignees []string) error
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
	Severity          string `json:"severity"`
	Category          string `json:"category"`
	SuggestedAssignee string `json:"suggested_assignee"`
}

// IssueReviewResult is the parsed LLM output for a triage run. Mirrors the
// schema advertised in the prompt template.
type IssueReviewResult struct {
	Summary     string   `json:"summary"`
	Triage      Triage   `json:"triage"`
	Suggestions []string `json:"suggestions"`
	Severity    string   `json:"severity"`
}

// RunOptions carries per-execution settings derived from global + repo +
// agent config by the caller.
//
// The working directory — the repo-level `local_dir` in config.toml — is
// passed as `ExecOpts.WorkDir`. That single field drives both the mode
// downgrade (develop → review_only when absent) and the prompt context, so
// they can never disagree. Callers mapping from `config.RepoAI.LocalDir`
// assign it directly to `ExecOpts.WorkDir`; do not add a separate field
// here (we had one in PR #44 review drafts — it caused exactly the
// inconsistency the reviewers flagged).
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
}

// Pipeline runs a single issue triage or implementation end-to-end.
type Pipeline struct {
	store    issueStore
	gh       issueGitHub
	executor CLIExecutor
	git      GitOps
	broker   Publisher
	notify   Notifier
}

// issueStore is the subset of *store.Store the pipeline needs. Kept narrow
// so tests can substitute a fake without bringing in SQLite.
type issueStore interface {
	UpsertIssue(i *store.Issue) (int64, error)
	InsertIssueReview(r *store.IssueReview) (int64, error)
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
	if issue == nil {
		return nil, fmt.Errorf("issues pipeline: nil issue")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Determine the effective mode. `ExecOpts.WorkDir` is the single source
	// of truth for "is there a local checkout"; Run does not consult any
	// other field.
	workDir := strings.TrimSpace(opts.ExecOpts.WorkDir)
	effective := issue.Mode
	if effective == config.IssueModeDevelop && workDir == "" {
		slog.Warn("issues pipeline: develop mode requires local_dir, downgrading to review_only",
			"repo", issue.Repo, "issue", issue.Number)
		effective = config.IssueModeReviewOnly
	}
	if effective == config.IssueModeIgnore {
		return nil, fmt.Errorf("issues pipeline: refusing an ignore-classified issue (fetcher should have filtered it out)")
	}

	// Upsert + initial SSE events are common to both flows so we do them
	// here. issue_detected fires before the flow forks, issue_review_started
	// fires after so the UI can show the correct "triaging" vs "implementing"
	// copy — the runner sets the exact flavour it wants.
	storeIssue, err := issueToStore(issue)
	if err != nil {
		return nil, err
	}
	issueID, err := p.store.UpsertIssue(storeIssue)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: upsert issue: %w", err)
	}
	p.publish(sse.EventIssueDetected, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo,
	})

	switch effective {
	case config.IssueModeReviewOnly:
		return p.runReviewOnly(ctx, issue, issueID, workDir, opts)
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
	comments, err := p.gh.FetchComments(issue.Repo, issue.Number)
	if err != nil {
		slog.Warn("issues pipeline: failed to fetch comments, proceeding without", "err", err)
		comments = nil
	}

	// Build prompt + run the CLI. HasLocalDir mirrors workDir above so the
	// LLM hears the same story as the mode-selection logic.
	// Agent profile customization: IssuePromptOverride replaces the entire
	// template; IssueInstructions injects into the default template.
	promptCtx := PromptContext{
		Repo:        issue.Repo,
		Number:      issue.Number,
		Title:       issue.Title,
		Author:      issue.User.Login,
		Labels:      issue.LabelNames(),
		Assignees:   issue.AssigneeLogins(),
		Body:        issue.Body,
		Comments:    comments,
		HasLocalDir: workDir != "",
	}
	prompt := BuildPromptWithProfile(promptCtx, opts.IssuePromptOverride, opts.IssueInstructions)

	cli, err := p.executor.Detect(opts.Primary, opts.Fallback)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("detect CLI: %w", err))
		return nil, fmt.Errorf("issues pipeline: detect CLI: %w", err)
	}
	raw, err := p.executor.ExecuteRaw(cli, prompt, opts.ExecOpts)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("execute %s: %w", cli, err))
		return nil, fmt.Errorf("issues pipeline: execute %s: %w", cli, err)
	}
	result, err := parseIssueResult(raw)
	if err != nil {
		p.publishError(issueID, issue, err)
		return nil, fmt.Errorf("issues pipeline: parse result: %w", err)
	}

	// Build + post the Markdown comment. PostComment failure is not fatal —
	// the review is still persisted locally with a zero pr_created so
	// operators can re-drive it manually without losing the LLM output.
	body := BuildMarkdownComment(result)
	postErr := p.gh.PostComment(issue.Repo, issue.Number, body)
	if postErr != nil {
		slog.Warn("issues pipeline: PostComment failed, review will be stored locally only",
			"repo", issue.Repo, "number", issue.Number, "err", postErr)
	}

	triageJSON, err := json.Marshal(result.Triage)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: marshal triage: %w", err)
	}
	suggestionsJSON, err := json.Marshal(result.Suggestions)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: marshal suggestions: %w", err)
	}
	rev := &store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     cli,
		Summary:     result.Summary,
		Triage:      string(triageJSON),
		Suggestions: string(suggestionsJSON),
		ActionTaken: string(config.IssueModeReviewOnly),
		CreatedAt:   time.Now().UTC(),
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
// When the agent produces no changes the run silently degrades to
// review_only with an explanatory comment rather than opening an empty PR.
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
	if err := p.git.CheckoutNewBranch(ctx, workDir, branch, base); err != nil {
		p.publishError(issueID, issue, fmt.Errorf("checkout: %w", err))
		return nil, fmt.Errorf("issues pipeline: checkout: %w", err)
	}

	// Fetch comments once so the implement prompt carries the same context
	// the triage path would see. Best-effort as before.
	comments, err := p.gh.FetchComments(issue.Repo, issue.Number)
	if err != nil {
		slog.Warn("issues pipeline: failed to fetch comments, proceeding without", "err", err)
		comments = nil
	}

	cli, err := p.executor.Detect(opts.Primary, opts.Fallback)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("detect CLI: %w", err))
		return nil, fmt.Errorf("issues pipeline: detect CLI: %w", err)
	}

	// Agent profile customization: ImplementPromptOverride replaces the entire
	// template; ImplementInstructions injects into the default template.
	prompt := BuildImplementPromptWithProfile(
		PromptContext{
			Repo: issue.Repo, Number: issue.Number,
			Title: issue.Title, Author: issue.User.Login,
			Labels: issue.LabelNames(), Assignees: issue.AssigneeLogins(),
			Body: issue.Body, Comments: comments, HasLocalDir: true,
		},
		opts.ImplementPromptOverride,
		opts.ImplementInstructions,
	)
	if _, err := p.executor.ExecuteRaw(cli, prompt, opts.ExecOpts); err != nil {
		p.publishError(issueID, issue, fmt.Errorf("execute %s: %w", cli, err))
		return nil, fmt.Errorf("issues pipeline: execute %s: %w", cli, err)
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
	if err := p.git.Push(ctx, workDir, issue.Repo, branch, opts.GitHubToken); err != nil {
		p.publishError(issueID, issue, fmt.Errorf("push: %w", err))
		return nil, fmt.Errorf("issues pipeline: push: %w", err)
	}

	prTitle := fmt.Sprintf("feat: implement #%d — %s", issue.Number, safeTitle)
	prBody := fmt.Sprintf("Auto-generated by Heimdallm in response to #%d.\n\nCloses #%d",
		issue.Number, issue.Number)
	prNumber, err := p.gh.CreatePR(issue.Repo, prTitle, prBody, branch, base, opts.PRDraft)
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

	// Apply PR metadata (reviewers, labels, assignees). All best-effort —
	// a metadata failure does not roll back the PR, which is already public.
	applyPRMetadata(p.gh, issue.Repo, prNumber, opts)

	// Post a short "Implementation PR: #N" comment on the issue so watchers
	// of the issue (who might not watch the repo) see the PR land. Non-fatal
	// on failure — the PR is already public and the review row carries the
	// number, so a missed comment does not lose information.
	linkBackBody := fmt.Sprintf("Implementation PR: #%d", prNumber)
	if err := p.gh.PostComment(issue.Repo, issue.Number, linkBackBody); err != nil {
		slog.Warn("issues pipeline: link-back comment failed",
			"repo", issue.Repo, "number", issue.Number, "err", err)
	}

	rev := &store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     cli,
		Summary:     fmt.Sprintf("Auto-implementation landed as PR #%d on branch %s.", prNumber, branch),
		Triage:      "{}", // no triage block for implement runs
		Suggestions: "[]",
		ActionTaken: string(config.IssueModeDevelop),
		PRCreated:   prNumber,
		CreatedAt:   time.Now().UTC(),
	}
	revID, err := p.store.InsertIssueReview(rev)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: store review: %w", err)
	}
	rev.ID = revID

	p.publish(sse.EventIssueImplemented, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo,
		"pr_created": prNumber, "branch": branch,
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
	body := fmt.Sprintf(
		"## ⚠️ Heimdallm auto-implement skipped\n\n"+
			"The agent looked at #%d but left the working tree unchanged — it likely needs a human decision or more context than the issue alone provides.\n\n"+
			"Rerun manually with more details in the issue body, or remove the develop label to stop retries.\n\n"+
			"---\n*auto_implement → review_only fallback · Heimdallm*",
		issue.Number,
	)
	postErr := p.gh.PostComment(issue.Repo, issue.Number, body)
	if postErr != nil {
		slog.Warn("issues pipeline: auto_implement fallback PostComment failed",
			"repo", issue.Repo, "number", issue.Number, "err", postErr)
	}

	rev := &store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     cli,
		Summary:     "auto_implement produced no changes; downgraded to review_only",
		Triage:      "{}",
		Suggestions: "[]",
		// ActionTaken reflects what actually ran — keeps the audit trail
		// honest per the same rule we established in #26 for the
		// develop-without-local_dir fallback.
		ActionTaken: string(config.IssueModeReviewOnly),
		CreatedAt:   time.Now().UTC(),
	}
	revID, err := p.store.InsertIssueReview(rev)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: store fallback review: %w", err)
	}
	rev.ID = revID

	p.publish(sse.EventIssueReviewCompleted, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo,
		"mode": "auto_implement_no_changes", "post_ok": postErr == nil,
	})
	slog.Info("issues pipeline: auto_implement had no changes, posted fallback comment",
		"repo", issue.Repo, "number", issue.Number, "posted", postErr == nil)
	return rev, nil
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

// issueToStore converts the github.Issue wire shape into the store row. The
// store keeps assignees and labels as JSON arrays (`[]` when empty), matching
// the schema introduced in #24.
//
// The issue's processing mode (review_only vs develop) is intentionally not
// part of store.Issue — the issues table captures the issue itself, while
// the mode of *each triage run* lives on issue_reviews.action_taken. That
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

func issueToStore(i *github.Issue) (*store.Issue, error) {
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
	if r.Triage.SuggestedAssignee != "" {
		// Strip any leading '@' the LLM may have included so the template
		// does not render a double '@@alice' that pings nobody.
		assignee := strings.TrimLeft(r.Triage.SuggestedAssignee, "@")
		sb.WriteString(fmt.Sprintf("- **Suggested assignee:** @%s\n", assignee))
	}
	sb.WriteString("\n")

	if len(r.Suggestions) > 0 {
		sb.WriteString("### Next steps\n\n")
		for _, s := range r.Suggestions {
			sb.WriteString("- " + s + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n*review_only mode · reviewed by Heimdallm*")
	return sb.String()
}
