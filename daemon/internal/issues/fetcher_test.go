package issues_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/store"
)

// ── fetcher-only fakes ───────────────────────────────────────────────────────

type fakeClient struct {
	issues []*github.Issue
	err    error
}

func (c *fakeClient) FetchIssues(repo string, cfg config.IssueTrackingConfig, authenticatedUser string) ([]*github.Issue, error) {
	return c.issues, c.err
}

type dedupEntry struct {
	row     *store.Issue
	review  *store.IssueReview
	rowErr  error
	revErr  error
}

type fakeDedup struct {
	byGithubID map[int64]dedupEntry
}

func (d *fakeDedup) GetIssueByGithubID(githubID int64) (*store.Issue, error) {
	e, ok := d.byGithubID[githubID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	if e.rowErr != nil {
		return nil, e.rowErr
	}
	return e.row, nil
}

func (d *fakeDedup) LatestIssueReview(issueID int64) (*store.IssueReview, error) {
	for _, e := range d.byGithubID {
		if e.row != nil && e.row.ID == issueID {
			if e.revErr != nil {
				return nil, e.revErr
			}
			if e.review == nil {
				return nil, sql.ErrNoRows
			}
			return e.review, nil
		}
	}
	return nil, sql.ErrNoRows
}

type fakePipeline struct {
	calls  []int // issue.Number for each Run call
	runErr error
}

func (f *fakePipeline) Run(ctx context.Context, issue *github.Issue, opts issues.RunOptions) (*store.IssueReview, error) {
	f.calls = append(f.calls, issue.Number)
	if f.runErr != nil {
		return nil, f.runErr
	}
	return &store.IssueReview{IssueID: int64(issue.Number), ActionTaken: string(config.IssueModeReviewOnly)}, nil
}

func noOpts(_ *github.Issue) issues.RunOptions { return issues.RunOptions{} }

func fixture(number int, updated time.Time) *github.Issue {
	return &github.Issue{
		ID:        int64(1000 + number),
		Number:    number,
		Repo:      "org/repo",
		UpdatedAt: updated,
		Mode:      config.IssueModeReviewOnly,
	}
}

func enabledCfg() config.IssueTrackingConfig {
	return config.IssueTrackingConfig{Enabled: true}
}

// ── behaviour ────────────────────────────────────────────────────────────────

func TestFetcher_NoOpWhenDisabled(t *testing.T) {
	client := &fakeClient{issues: []*github.Issue{fixture(1, time.Now())}}
	p := &fakePipeline{}
	f := issues.NewFetcher(client, &fakeDedup{}, p)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", config.IssueTrackingConfig{Enabled: false}, "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 0 || len(p.calls) != 0 {
		t.Errorf("expected no-op when disabled, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_NilOptionsFnIsError(t *testing.T) {
	f := issues.NewFetcher(&fakeClient{}, &fakeDedup{}, &fakePipeline{})
	_, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", nil)
	if err == nil {
		t.Fatal("expected error for nil OptionsFn")
	}
}

func TestFetcher_FetchErrorIsFatalForThisRun(t *testing.T) {
	client := &fakeClient{err: errors.New("github down")}
	f := issues.NewFetcher(client, &fakeDedup{}, &fakePipeline{})

	_, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if err == nil {
		t.Fatal("expected fetch error to surface")
	}
}

func TestFetcher_DispatchesUnprocessedIssues(t *testing.T) {
	client := &fakeClient{issues: []*github.Issue{fixture(1, time.Now()), fixture(2, time.Now())}}
	p := &fakePipeline{}
	f := issues.NewFetcher(client, &fakeDedup{byGithubID: map[int64]dedupEntry{}}, p)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 2 || len(p.calls) != 2 {
		t.Errorf("expected 2 dispatches, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_SkipsDismissedIssues(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {row: &store.Issue{ID: 10, GithubID: issue.ID, Dismissed: true}},
	}}
	client := &fakeClient{issues: []*github.Issue{issue}}
	p := &fakePipeline{}
	f := issues.NewFetcher(client, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 0 || len(p.calls) != 0 {
		t.Errorf("dismissed issue should be skipped, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_SkipsIssueAlreadyReviewedWithoutNewActivity(t *testing.T) {
	reviewedAt := time.Now().Add(-1 * time.Hour)
	issue := fixture(1, reviewedAt.Add(5*time.Second)) // within 30s grace
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:    &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{IssueID: 10, CreatedAt: reviewedAt},
		},
	}}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, dedup, &fakePipeline{})

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 0 {
		t.Errorf("issue within grace window should be skipped, got processed=%d", processed)
	}
}

func TestFetcher_RerunsIssueWithNewActivityAfterGrace(t *testing.T) {
	reviewedAt := time.Now().Add(-1 * time.Hour)
	issue := fixture(1, reviewedAt.Add(5*time.Minute)) // well past the 30s grace
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:    &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{IssueID: 10, CreatedAt: reviewedAt},
		},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("new activity past grace should re-run, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_RunsFirstTimeIssueKnownButNeverReviewed(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {row: &store.Issue{ID: 10, GithubID: issue.ID}}, // no review yet
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("issue known but never reviewed must run, got processed=%d", processed)
	}
}

func TestFetcher_PipelineErrorIsLoggedNotPropagated(t *testing.T) {
	// One issue makes Run return an error; the second should still run so a
	// single flaky issue does not block the rest of the repo.
	issue1 := fixture(1, time.Now())
	issue2 := fixture(2, time.Now())
	client := &fakeClient{issues: []*github.Issue{issue1, issue2}}
	p := &fakePipeline{runErr: errors.New("LLM timeout")}
	f := issues.NewFetcher(client, &fakeDedup{byGithubID: map[int64]dedupEntry{}}, p)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("per-issue failure must not abort ProcessRepo, got %v", err)
	}
	if processed != 0 {
		t.Errorf("expected 0 successes when every run fails, got %d", processed)
	}
	if len(p.calls) != 2 {
		t.Errorf("both issues must be attempted, got %v", p.calls)
	}
}

func TestFetcher_DedupLookupErrorTreatedAsUnprocessed(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {rowErr: errors.New("store unavailable")},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("flaky store must not block the pipeline, got processed=%d", processed)
	}
}
