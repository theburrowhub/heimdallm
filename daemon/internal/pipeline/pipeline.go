package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

// Pipeline orchestrates the full PR review flow.
type Pipeline struct {
	store    *store.Store
	gh       DiffFetcher
	executor CLIExecutor
	notify   Notifier
}

// New creates a new Pipeline with the provided dependencies.
func New(s *store.Store, gh DiffFetcher, exec CLIExecutor, n Notifier) *Pipeline {
	return &Pipeline{store: s, gh: gh, executor: exec, notify: n}
}

// Run executes the full review pipeline for one PR and returns the stored review.
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

	// 3. Build prompt (diff is truncated inside BuildPrompt if needed)
	prompt := executor.BuildPrompt(pr.Title, pr.User.Login, diff)

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

	// 7. Store review
	rev := &store.Review{
		PRID:        prID,
		CLIUsed:     cli,
		Summary:     result.Summary,
		Issues:      string(issuesJSON),
		Suggestions: string(suggestionsJSON),
		Severity:    result.Severity,
		CreatedAt:   time.Now().UTC(),
	}
	rev.ID, err = p.store.InsertReview(rev)
	if err != nil {
		return nil, fmt.Errorf("pipeline: store review: %w", err)
	}

	p.notify.Notify("PR Review Complete",
		fmt.Sprintf("%s #%d — severity: %s", pr.Repo, pr.Number, result.Severity))

	slog.Info("pipeline: review complete", "pr", pr.Number, "severity", result.Severity)
	return rev, nil
}
