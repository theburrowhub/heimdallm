package issues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

// RecomputeGrace absorbs the small updated_at bump GitHub applies when the
// daemon posts its own comment. Without it, every triage would immediately
// re-trigger itself on the next poll.
//
// The PR pipeline in main.go currently duplicates this 30 s window inline.
// When #28 wires issues into the poll cycle it should import this constant
// and route the PR check through it too, so the two grace windows can't
// drift apart unnoticed.
const RecomputeGrace = 30 * time.Second

// IssuesFetcher is the subset of github.Client that fetches classified
// issues. Kept as an interface so the fetcher can be tested without an HTTP
// server standing in.
type IssuesFetcher interface {
	FetchIssues(repo string, cfg config.IssueTrackingConfig, authenticatedUser string) ([]*github.Issue, error)
}

// PipelineRunner is the subset of *Pipeline the fetcher uses. Takes a
// context so shutdown cancellation propagates through the whole dispatch
// path down to the git / HTTP calls inside the pipeline.
type PipelineRunner interface {
	Run(ctx context.Context, issue *github.Issue, opts RunOptions) (*store.IssueReview, error)
}

// issueDedupStore is the store slice needed to decide whether an issue has
// already been processed with no new activity since.
type issueDedupStore interface {
	GetIssueByGithubID(githubID int64) (*store.Issue, error)
	LatestIssueReview(issueID int64) (*store.IssueReview, error)
}

// OptionsFn lets the caller map each classified issue to its RunOptions.
// In production main.go resolves per-repo AI config here; tests can return a
// constant.
type OptionsFn func(issue *github.Issue) RunOptions

// Fetcher orchestrates: fetch issues for a repo, skip those already processed
// without new activity, dispatch the rest to the pipeline.
type Fetcher struct {
	client   IssuesFetcher
	store    issueDedupStore
	pipeline PipelineRunner
}

// NewFetcher wires the orchestrator. All dependencies are interfaces so
// tests inject lightweight fakes.
func NewFetcher(client IssuesFetcher, s issueDedupStore, p PipelineRunner) *Fetcher {
	return &Fetcher{client: client, store: s, pipeline: p}
}

// ProcessRepo fetches every eligible issue for one repo and dispatches it to
// the pipeline. Returns the number of issues actually handed off and a
// non-nil error only when the fetch itself failed — per-issue pipeline
// failures are logged and counted but do not abort the run.
//
// When cfg.Enabled is false this is a no-op; the caller does not have to
// guard the call. ctx is passed through to pipeline.Run so a daemon
// shutdown cancels whatever issue is currently being processed.
func (f *Fetcher) ProcessRepo(ctx context.Context, repo string, cfg config.IssueTrackingConfig, authUser string, optsFor OptionsFn) (int, error) {
	if !cfg.Enabled {
		return 0, nil
	}
	if optsFor == nil {
		return 0, fmt.Errorf("issues fetcher: nil OptionsFn")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	issues, err := f.client.FetchIssues(repo, cfg, authUser)
	if err != nil {
		return 0, fmt.Errorf("issues fetcher: fetch %s: %w", repo, err)
	}

	processed := 0
	for _, issue := range issues {
		// Abort the loop cleanly on cancellation so a shutdown does not get
		// stuck waiting for remaining issues when the caller already gave up.
		if err := ctx.Err(); err != nil {
			return processed, fmt.Errorf("issues fetcher: %s cancelled after %d processed: %w", repo, processed, err)
		}

		// A dedup lookup error intentionally falls through to "treat as
		// unprocessed" so a flaky store never stops the pipeline from running;
		// the explicit if / else if makes that control flow obvious.
		skip, reason, err := f.alreadyProcessed(issue)
		if err != nil {
			slog.Warn("issues fetcher: dedup check failed, treating as unprocessed",
				"repo", repo, "number", issue.Number, "err", err)
		} else if skip {
			slog.Debug("issues fetcher: skipping issue",
				"repo", repo, "number", issue.Number, "reason", reason)
			continue
		}

		if _, runErr := f.pipeline.Run(ctx, issue, optsFor(issue)); runErr != nil {
			slog.Error("issues fetcher: pipeline run failed",
				"repo", repo, "number", issue.Number, "err", runErr)
			continue
		}
		processed++
	}
	return processed, nil
}

// alreadyProcessed reports whether the issue can be skipped because:
//   - it was dismissed by the user, or
//   - it was already reviewed and has no new activity (UpdatedAt ≤ last
//     review + grace window).
//
// The err return signals a lookup failure — the caller logs it and proceeds
// as if the issue were unprocessed, so a flaky store never stops the
// pipeline from running.
func (f *Fetcher) alreadyProcessed(issue *github.Issue) (bool, string, error) {
	row, err := f.store.GetIssueByGithubID(issue.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// First time we see this issue — process it.
			return false, "", nil
		}
		return false, "", err
	}
	if row.Dismissed {
		return true, "dismissed", nil
	}

	latest, err := f.store.LatestIssueReview(row.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Known issue, never reviewed — process it.
			return false, "", nil
		}
		return false, "", err
	}

	// If a previous run already created a PR via auto_implement, skip
	// unconditionally — re-running would fail with non-fast-forward or
	// create a duplicate PR.  The user should close the issue or dismiss
	// it to stop the pipeline from picking it up again.
	if latest.ActionTaken == "auto_implement" && latest.PRCreated > 0 {
		return true, "already implemented (PR created)", nil
	}

	ref := latest.CommentedAt
	if ref.IsZero() {
		ref = latest.CreatedAt
	}
	cutoff := ref.Add(RecomputeGrace)
	if !issue.UpdatedAt.After(cutoff) {
		return true, "no new activity since last review", nil
	}
	return false, "", nil
}
