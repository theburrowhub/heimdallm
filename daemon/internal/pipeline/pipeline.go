package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallr/daemon/internal/executor"
	"github.com/heimdallr/daemon/internal/github"
	"github.com/heimdallr/daemon/internal/store"
)

// DiffFetcher retrieves the diff for a pull request.
type DiffFetcher interface {
	FetchDiff(repo string, number int) (string, error)
}

// CLIExecutor detects and runs an AI CLI tool.
type CLIExecutor interface {
	Detect(primary, fallback string) (string, error)
	Execute(cli, prompt string) (*executor.ReviewResult, error)
}

// Notifier sends desktop or system notifications.
type Notifier interface {
	Notify(title, message string)
}

// GitHubReviewer can submit a review to GitHub.
type GitHubReviewer interface {
	SubmitReview(repo string, number int, body, event string) (int64, error)
}

// Pipeline orchestrates the full PR review flow.
type Pipeline struct {
	store    *store.Store
	gh       interface {
		DiffFetcher
		GitHubReviewer
	}
	executor CLIExecutor
	notify   Notifier
}

// New creates a new Pipeline with the provided dependencies.
func New(s *store.Store, gh interface {
	DiffFetcher
	GitHubReviewer
}, exec CLIExecutor, n Notifier) *Pipeline {
	return &Pipeline{store: s, gh: gh, executor: exec, notify: n}
}

// Run executes the full review pipeline for one PR and publishes the review to GitHub.
// SQLite is the source of truth: review is stored first, then published.
// If publishing fails, it is retried on the next call (when GitHubReviewID == 0).
func (p *Pipeline) Run(pr *github.PullRequest, primary, fallback string) (*store.Review, error) {
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

	p.notify.Notify("PR Review Started", fmt.Sprintf("%s #%d", pr.Repo, pr.Number))

	// 2. Fetch diff
	diff, err := p.gh.FetchDiff(pr.Repo, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("pipeline: fetch diff: %w", err)
	}

	// 3. Build prompt from agent template (or built-in default)
	promptTemplate := executor.DefaultTemplate()
	if agent, err := p.store.DefaultAgent(); err == nil && agent != nil && agent.Prompt != "" {
		promptTemplate = agent.Prompt
	}
	prompt := executor.BuildPromptFromTemplate(promptTemplate, executor.PRContext{
		Title:  pr.Title,
		Number: pr.Number,
		Repo:   pr.Repo,
		Author: pr.User.Login,
		Link:   pr.HTMLURL,
		Diff:   diff,
	})

	// 4. Select CLI
	cli, err := p.executor.Detect(primary, fallback)
	if err != nil {
		return nil, fmt.Errorf("pipeline: detect CLI: %w", err)
	}
	slog.Info("pipeline: using CLI", "cli", cli)

	// 5. Execute review
	result, err := p.executor.Execute(cli, prompt)
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
	}
	rev.ID, err = p.store.InsertReview(rev)
	if err != nil {
		return nil, fmt.Errorf("pipeline: store review: %w", err)
	}
	slog.Info("pipeline: review stored locally", "review_id", rev.ID)

	// 8. Publish review to GitHub
	ghReviewID, publishErr := p.gh.SubmitReview(
		pr.Repo, pr.Number,
		buildGitHubBody(result),
		severityToEvent(result.Severity, len(result.Issues)),
	)
	if publishErr != nil {
		// Review saved locally; will retry on next poll (GitHubReviewID == 0 check)
		slog.Warn("pipeline: failed to publish to GitHub, will retry",
			"pr", pr.Number, "err", publishErr)
	} else {
		_ = p.store.UpdateGitHubReviewID(rev.ID, ghReviewID)
		rev.GitHubReviewID = ghReviewID
		slog.Info("pipeline: review published to GitHub",
			"pr", pr.Number, "github_review_id", ghReviewID)
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
		// Rebuild a minimal result from stored JSON for the body
		var issues []executor.Issue
		json.Unmarshal([]byte(rev.Issues), &issues)
		result := &executor.ReviewResult{
			Summary:  rev.Summary,
			Issues:   issues,
			Severity: rev.Severity,
		}
		ghID, err := p.gh.SubmitReview(
			pr.Repo, pr.Number,
			buildGitHubBody(result),
			severityToEvent(rev.Severity, len(issues)),
		)
		if err != nil {
			slog.Warn("pipeline: retry publish failed", "review_id", rev.ID, "err", err)
			continue
		}
		_ = p.store.UpdateGitHubReviewID(rev.ID, ghID)
		slog.Info("pipeline: pending review published", "review_id", rev.ID, "github_review_id", ghID)
	}
}

// buildGitHubBody formats the AI review as a GitHub-flavored markdown review body.
func buildGitHubBody(r *executor.ReviewResult) string {
	var sb strings.Builder
	sb.WriteString("## 🤖 Heimdallr AI Review\n\n")
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
		strings.ToUpper(r.Severity), "Heimdallr"))
	return sb.String()
}

// severityToEvent maps severity to a GitHub review event type.
func severityToEvent(severity string, issueCount int) string {
	if issueCount == 0 {
		return "APPROVE"
	}
	switch severity {
	case "high", "medium":
		return "REQUEST_CHANGES"
	default:
		return "COMMENT"
	}
}
