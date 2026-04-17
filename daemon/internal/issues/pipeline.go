// Package issues hosts the fase-2 issue-tracking pipeline — one step per
// issue triage plus a fetcher that orchestrates batches of issues from a
// repo. This pipeline runs the review_only mode (LLM triage + GitHub
// comment). The auto_implement mode lives in a sibling file shipped with
// issue #27.
package issues

import (
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
type RunOptions struct {
	Primary  string
	Fallback string
	ExecOpts executor.ExecOptions
}

// Pipeline runs a single issue triage end-to-end.
type Pipeline struct {
	store    issueStore
	gh       issueGitHub
	executor CLIExecutor
	broker   Publisher
	notify   Notifier
}

// issueStore is the subset of *store.Store the pipeline needs. Kept narrow
// so tests can substitute a fake without bringing in SQLite.
type issueStore interface {
	UpsertIssue(i *store.Issue) (int64, error)
	InsertIssueReview(r *store.IssueReview) (int64, error)
}

type issueGitHub interface {
	IssueCommenter
	IssueCommentFetcher
}

// New wires the pipeline. All dependencies are interfaces so tests can
// inject fakes.
func New(s issueStore, gh issueGitHub, exec CLIExecutor, broker Publisher, n Notifier) *Pipeline {
	return &Pipeline{store: s, gh: gh, executor: exec, broker: broker, notify: n}
}

// Run processes one classified issue and returns the persisted review. The
// returned IssueReview's ActionTaken reflects the mode that actually ran —
// in particular, a develop-classified issue that loses its local_dir is
// persisted as "review_only" so the audit trail matches behaviour.
func (p *Pipeline) Run(issue *github.Issue, opts RunOptions) (*store.IssueReview, error) {
	if issue == nil {
		return nil, fmt.Errorf("issues pipeline: nil issue")
	}

	// 1. Determine the effective mode. If the caller asked for develop but
	// there is no working directory to hand the CLI, degrade to review_only
	// instead of failing — safer for operators and matches the acceptance
	// criterion in #26. `ExecOpts.WorkDir` is the single source of truth for
	// "is there a local checkout"; Run does not consult any other field.
	workDir := strings.TrimSpace(opts.ExecOpts.WorkDir)
	effective := issue.Mode
	if effective == config.IssueModeDevelop && workDir == "" {
		slog.Warn("issues pipeline: develop mode requires local_dir, downgrading to review_only",
			"repo", issue.Repo, "issue", issue.Number)
		effective = config.IssueModeReviewOnly
	}
	// Reject anything that does not resolve to review_only with a specific
	// message so the log tells operators exactly why the pipeline refused.
	// `ignore` should never reach this code (the fetcher filters it out) —
	// if it somehow does, surface that as its own error rather than the
	// auto_implement one.
	if effective == config.IssueModeIgnore {
		return nil, fmt.Errorf("issues pipeline: refusing an ignore-classified issue (fetcher should have filtered it out)")
	}
	if effective != config.IssueModeReviewOnly {
		// Only possibility left: develop mode WITH a working directory,
		// which is the auto_implement path owned by #27.
		return nil, fmt.Errorf("issues pipeline: develop mode with local_dir belongs to the auto_implement path in #27")
	}

	// 2. Persist the issue row (or update an existing one). Dismiss-preserving
	// upsert semantics are already guaranteed by store.UpsertIssue.
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
	p.publish(sse.EventIssueReviewStarted, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo,
	})
	if p.notify != nil {
		p.notify.Notify("Issue Triage Started", fmt.Sprintf("%s #%d", issue.Repo, issue.Number))
	}

	// 3. Pull existing discussion as additional context. Failure is
	// non-fatal — the triage still runs with title + body alone.
	comments, err := p.gh.FetchComments(issue.Repo, issue.Number)
	if err != nil {
		slog.Warn("issues pipeline: failed to fetch comments, proceeding without", "err", err)
		comments = nil
	}

	// 4. Build prompt + run the CLI. HasLocalDir mirrors workDir above so
	// the LLM hears the same story as the mode-selection logic.
	prompt := BuildPrompt(PromptContext{
		Repo:        issue.Repo,
		Number:      issue.Number,
		Title:       issue.Title,
		Author:      issue.User.Login,
		Labels:      issue.LabelNames(),
		Body:        issue.Body,
		Comments:    comments,
		HasLocalDir: workDir != "",
	})

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

	// 5. Build + post the Markdown comment. PostComment failure is not
	// fatal — the review is still persisted locally with a zero pr_created
	// so operators can re-drive it manually without losing the LLM output.
	body := BuildMarkdownComment(result)
	postErr := p.gh.PostComment(issue.Repo, issue.Number, body)
	if postErr != nil {
		slog.Warn("issues pipeline: PostComment failed, review will be stored locally only",
			"repo", issue.Repo, "number", issue.Number, "err", postErr)
	}

	// 6. Persist the review.
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
