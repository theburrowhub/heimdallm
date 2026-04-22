package issues_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

type fakeMarkerFetcher struct {
	commentsByKey map[string][]github.Comment
	err           error
}

func (f *fakeMarkerFetcher) FetchComments(repo string, number int) ([]github.Comment, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := fmt.Sprintf("%s#%d", repo, number)
	return f.commentsByKey[key], nil
}

func (c *fakeClient) FetchIssues(repo string, cfg config.IssueTrackingConfig, authenticatedUser string) ([]*github.Issue, error) {
	return c.issues, c.err
}

type dedupEntry struct {
	row               *store.Issue
	review            *store.IssueReview
	rowErr            error
	revErr            error
	failedAutoImpl    int // stub for CountFailedAutoImplement
	failedAutoImplErr error
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

func (d *fakeDedup) CountFailedAutoImplement(issueID int64) (int, error) {
	for _, e := range d.byGithubID {
		if e.row != nil && e.row.ID == issueID {
			if e.failedAutoImplErr != nil {
				return 0, e.failedAutoImplErr
			}
			return e.failedAutoImpl, nil
		}
	}
	return 0, nil
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
	f := issues.NewFetcher(client, nil, &fakeDedup{}, p)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", config.IssueTrackingConfig{Enabled: false}, "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 0 || len(p.calls) != 0 {
		t.Errorf("expected no-op when disabled, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_NilOptionsFnIsError(t *testing.T) {
	f := issues.NewFetcher(&fakeClient{}, nil, &fakeDedup{}, &fakePipeline{})
	_, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", nil)
	if err == nil {
		t.Fatal("expected error for nil OptionsFn")
	}
}

func TestFetcher_FetchErrorIsFatalForThisRun(t *testing.T) {
	client := &fakeClient{err: errors.New("github down")}
	f := issues.NewFetcher(client, nil, &fakeDedup{}, &fakePipeline{})

	_, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if err == nil {
		t.Fatal("expected fetch error to surface")
	}
}

func TestFetcher_DispatchesUnprocessedIssues(t *testing.T) {
	client := &fakeClient{issues: []*github.Issue{fixture(1, time.Now()), fixture(2, time.Now())}}
	p := &fakePipeline{}
	f := issues.NewFetcher(client, nil, &fakeDedup{byGithubID: map[int64]dedupEntry{}}, p)

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
	f := issues.NewFetcher(client, nil, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 0 || len(p.calls) != 0 {
		t.Errorf("dismissed issue should be skipped, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_SkipsIssueAlreadyReviewedWithoutNewActivity(t *testing.T) {
	reviewedAt := time.Now().Add(-1 * time.Hour)
	commentedAt := reviewedAt.Add(10 * time.Second)
	issue := fixture(1, commentedAt.Add(5*time.Second)) // within 30s grace of commentedAt
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:    &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{IssueID: 10, CreatedAt: reviewedAt, CommentedAt: commentedAt},
		},
	}}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, &fakePipeline{})

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 0 {
		t.Errorf("issue within grace window should be skipped, got processed=%d", processed)
	}
}

func TestFetcher_RerunsIssueWithNewActivityAfterGrace(t *testing.T) {
	reviewedAt := time.Now().Add(-1 * time.Hour)
	commentedAt := reviewedAt.Add(10 * time.Second)
	issue := fixture(1, commentedAt.Add(5*time.Minute)) // well past the 30s grace
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:    &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{IssueID: 10, CreatedAt: reviewedAt, CommentedAt: commentedAt},
		},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("new activity past grace should re-run, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_SlowTriageDoesNotReloop(t *testing.T) {
	createdAt := time.Now().Add(-2 * time.Minute)
	commentedAt := createdAt.Add(45 * time.Second) // triage took 45s
	// UpdatedAt is after createdAt+30s (old cutoff) but before commentedAt+30s (new cutoff).
	issue := fixture(1, commentedAt.Add(5*time.Second))
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:    &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{IssueID: 10, CreatedAt: createdAt, CommentedAt: commentedAt},
		},
	}}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, &fakePipeline{})

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 0 {
		t.Errorf("slow triage should not re-loop, got processed=%d", processed)
	}
}

func TestFetcher_FallsBackToCreatedAtWhenCommentedAtZero(t *testing.T) {
	reviewedAt := time.Now().Add(-1 * time.Hour)
	issue := fixture(1, reviewedAt.Add(5*time.Second)) // within 30s grace of createdAt
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:    &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{IssueID: 10, CreatedAt: reviewedAt}, // CommentedAt zero
		},
	}}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, &fakePipeline{})

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 0 {
		t.Errorf("zero CommentedAt should fall back to CreatedAt, got processed=%d", processed)
	}
}

func TestFetcher_RunsFirstTimeIssueKnownButNeverReviewed(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {row: &store.Issue{ID: 10, GithubID: issue.ID}}, // no review yet
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, p)

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
	f := issues.NewFetcher(client, nil, &fakeDedup{byGithubID: map[int64]dedupEntry{}}, p)

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
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("flaky store must not block the pipeline, got processed=%d", processed)
	}
}

// ── MaxAutoImplementFailures cap (#223) ──────────────────────────────────────

func TestFetcher_SkipsIssueAfterMaxAutoImplementFailures(t *testing.T) {
	// An issue that has already failed MaxAutoImplementFailures times must be
	// skipped unconditionally — retrying a structural push failure (e.g.
	// non-fast-forward) without human intervention will never succeed.
	reviewedAt := time.Now().Add(-2 * time.Hour)
	// UpdatedAt is recent so the grace-window check alone would not skip it —
	// the failure cap must be what blocks it.
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:            &store.Issue{ID: 10, GithubID: issue.ID},
			review:         &store.IssueReview{IssueID: 10, ActionTaken: "auto_implement_failed", CreatedAt: reviewedAt},
			failedAutoImpl: issues.MaxAutoImplementFailures, // exactly at the cap
		},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, p)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 0 || len(p.calls) != 0 {
		t.Errorf("issue at max failure cap should be skipped, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_DoesNotSkipBelowMaxAutoImplementFailures(t *testing.T) {
	// An issue with fewer than MaxAutoImplementFailures failures must still
	// be attempted — the cap has not been reached.
	reviewedAt := time.Now().Add(-2 * time.Hour)
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:            &store.Issue{ID: 10, GithubID: issue.ID},
			review:         &store.IssueReview{IssueID: 10, ActionTaken: "auto_implement_failed", CreatedAt: reviewedAt},
			failedAutoImpl: issues.MaxAutoImplementFailures - 1, // one below the cap
		},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("issue below failure cap must still run, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_CountFailedAutoImplErrDoesNotSkip(t *testing.T) {
	// When CountFailedAutoImplement returns an error, the fetcher must log
	// and proceed (fail-safe: never block an issue due to a flaky store).
	reviewedAt := time.Now().Add(-2 * time.Hour)
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:               &store.Issue{ID: 10, GithubID: issue.ID},
			review:            &store.IssueReview{IssueID: 10, CreatedAt: reviewedAt},
			failedAutoImplErr: errors.New("db timeout"),
		},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("count error must not block the pipeline, got processed=%d", processed)
	}
}

// ── comment-based control markers (#238) ────────────────────────────────────

func TestFetcher_SkipsDoneMarker(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {row: &store.Issue{ID: 10, GithubID: issue.ID}},
	}}
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{
		"org/repo#1": {{Body: "<!-- heimdallm:done -->\n✅ done", CreatedAt: time.Now()}},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 0 || len(p.calls) != 0 {
		t.Errorf("done marker should skip issue, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_SkipsSkipMarker(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {row: &store.Issue{ID: 10, GithubID: issue.ID}},
	}}
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{
		"org/repo#1": {{Body: "<!-- heimdallm:skip -->", CreatedAt: time.Now()}},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 0 || len(p.calls) != 0 {
		t.Errorf("skip marker should skip issue, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_RetryMarkerOverridesDone(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {row: &store.Issue{ID: 10, GithubID: issue.ID}},
	}}
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{
		"org/repo#1": {
			{Body: "<!-- heimdallm:done -->\n✅ done", CreatedAt: time.Now().Add(-10 * time.Minute)},
			{Body: "<!-- heimdallm:retry -->", CreatedAt: time.Now()},
		},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 || len(p.calls) != 1 {
		t.Errorf("retry marker should force reprocess, got processed=%d calls=%v", processed, p.calls)
	}
}

func TestFetcher_RetryMarkerOverridesDismissed(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {row: &store.Issue{ID: 10, GithubID: issue.ID, Dismissed: true}},
	}}
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{
		"org/repo#1": {{Body: "<!-- heimdallm:retry -->", CreatedAt: time.Now()}},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("retry marker should override dismiss, got processed=%d", processed)
	}
}

func TestFetcher_MarkerFetchErrorFallsThrough(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {row: &store.Issue{ID: 10, GithubID: issue.ID}},
	}}
	mf := &fakeMarkerFetcher{err: errors.New("comments API broken")}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("marker fetch error should fall through to pipeline, got processed=%d", processed)
	}
}

func TestFetcher_NilMarkerFetcherSkipsMarkerCheck(t *testing.T) {
	issue := fixture(1, time.Now())
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {row: &store.Issue{ID: 10, GithubID: issue.ID}},
	}}
	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, p)

	processed, _ := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if processed != 1 {
		t.Errorf("nil marker fetcher should skip marker check and process, got processed=%d", processed)
	}
}
