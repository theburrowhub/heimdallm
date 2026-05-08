package issues_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

func (f *fakeMarkerFetcher) FetchIssueCommentsOnly(repo string, number int) ([]github.Comment, error) {
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

type fakeIssuePublisher struct {
	triage     []int
	refinement []int
	implement  []int
}

type fakeStageTransitionClient struct {
	created  []string
	added    []string
	removed  []string
	comments []string
}

func (f *fakeStageTransitionClient) CreateLabel(repo, name, color, description string) error {
	f.created = append(f.created, name)
	return nil
}

func (f *fakeStageTransitionClient) AddLabels(repo string, number int, labels []string) error {
	f.added = append(f.added, strings.Join(labels, ","))
	return nil
}

func (f *fakeStageTransitionClient) RemoveLabels(repo string, number int, labels []string) error {
	f.removed = append(f.removed, strings.Join(labels, ","))
	return nil
}

func (f *fakeStageTransitionClient) PostComment(repo string, number int, body string) (time.Time, error) {
	f.comments = append(f.comments, body)
	return time.Now().UTC(), nil
}

func (f *fakeIssuePublisher) PublishIssueTriage(ctx context.Context, repo string, number int, githubID int64) error {
	f.triage = append(f.triage, number)
	return nil
}

func (f *fakeIssuePublisher) PublishIssueRefinement(ctx context.Context, repo string, number int, githubID int64) error {
	f.refinement = append(f.refinement, number)
	return nil
}

func (f *fakeIssuePublisher) PublishIssueImplement(ctx context.Context, repo string, number int, githubID int64) error {
	f.implement = append(f.implement, number)
	return nil
}

func noOpts(_ *github.Issue) (issues.RunOptions, bool) { return issues.RunOptions{}, true }

func fixture(number int, updated time.Time) *github.Issue {
	return &github.Issue{
		ID:        int64(1000 + number),
		Number:    number,
		Repo:      "org/repo",
		UpdatedAt: updated,
		Mode:      config.IssueModeReviewOnly,
	}
}

func issueLabels(labels ...string) []github.Label {
	out := make([]github.Label, 0, len(labels))
	for _, label := range labels {
		out = append(out, github.Label{Name: label})
	}
	return out
}

func stagePipelineCfg() config.IssueTrackingConfig {
	return config.IssueTrackingConfig{
		Enabled:          true,
		ReviewOnlyLabels: []string{"triage"},
		RefinementLabels: []string{"refine"},
		DevelopLabels:    []string{"develop"},
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

func TestFetcher_SkipsWhenOptionsFnReturnsFalse(t *testing.T) {
	client := &fakeClient{issues: []*github.Issue{fixture(1, time.Now()), fixture(2, time.Now())}}
	p := &fakePipeline{}
	f := issues.NewFetcher(client, nil, &fakeDedup{byGithubID: map[int64]dedupEntry{}}, p)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice",
		func(issue *github.Issue) (issues.RunOptions, bool) {
			if issue.Number == 1 {
				return issues.RunOptions{}, false
			}
			return issues.RunOptions{}, true
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 || len(p.calls) != 1 || p.calls[0] != 2 {
		t.Errorf("expected only issue 2 dispatched, got processed=%d calls=%v", processed, p.calls)
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

// TestFetcher_BotCommentSkipsReprocess verifies that when the most recent
// comment on an issue is from the bot itself, the fetcher skips reprocessing
// — breaking the re-triage loop described in #362.
func TestFetcher_BotCommentSkipsReprocess(t *testing.T) {
	now := time.Now()
	issue := fixture(1, now)

	// The issue has a previous review_only review with CommentedAt 2 minutes ago.
	// updated_at (now) is well past the 30s grace window, so without the
	// bot-comment check it would be reprocessed.
	commentedAt := now.Add(-2 * time.Minute)
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:    &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{CommentedAt: commentedAt, ActionTaken: "review_only"},
		},
	}}

	// The latest comment is from the bot.
	mf := &fakeMarkerFetcher{
		commentsByKey: map[string][]github.Comment{
			"org/repo#1": {
				{Author: "some-user", Body: "please triage this"},
				{Author: "heimdallm-bot", Body: "## Triage\n..."},
			},
		},
	}

	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, p)
	f.SetBotLogin("heimdallm-bot")

	processed, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 0 {
		t.Errorf("bot's own comment should prevent reprocessing, got processed=%d", processed)
	}
	if len(p.calls) != 0 {
		t.Errorf("pipeline should not have been called, got %d calls", len(p.calls))
	}
}

// TestFetcher_HumanCommentAfterBotAllowsReprocess verifies that when a
// human comments after the bot, the issue IS reprocessed.
func TestFetcher_HumanCommentAfterBotAllowsReprocess(t *testing.T) {
	now := time.Now()
	issue := fixture(1, now)

	commentedAt := now.Add(-2 * time.Minute)
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:    &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{CommentedAt: commentedAt},
		},
	}}

	// Bot commented, then a human replied — should reprocess.
	mf := &fakeMarkerFetcher{
		commentsByKey: map[string][]github.Comment{
			"org/repo#1": {
				{Author: "heimdallm-bot", Body: "## Triage\n..."},
				{Author: "some-user", Body: "I disagree with this analysis"},
			},
		},
	}

	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, p)
	f.SetBotLogin("heimdallm-bot")

	processed, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 {
		t.Errorf("human comment after bot should allow reprocessing, got processed=%d", processed)
	}
}

// TestFetcher_ModeChangeBypassesBotCommentCheck verifies that when the issue
// mode changed (e.g. label swapped from heimdallm-triage to heimdallm-develop),
// the bot-comment check does NOT block reprocessing — even if the last comment
// is from the bot (#362).
func TestFetcher_ModeChangeBypassesBotCommentCheck(t *testing.T) {
	now := time.Now()
	// Issue now has develop mode (label changed).
	issue := &github.Issue{
		ID:        int64(1001),
		Number:    1,
		Repo:      "org/repo",
		UpdatedAt: now,
		Mode:      config.IssueModeDevelop,
	}

	// Previous review was review_only — mode has changed.
	commentedAt := now.Add(-2 * time.Minute)
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row:    &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{CommentedAt: commentedAt, ActionTaken: "review_only"},
		},
	}}

	// Last comment is from the bot (triage comment).
	mf := &fakeMarkerFetcher{
		commentsByKey: map[string][]github.Comment{
			"org/repo#1": {
				{Author: "heimdallm-bot", Body: "## Triage\n..."},
			},
		},
	}

	p := &fakePipeline{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, p)
	f.SetBotLogin("heimdallm-bot")

	processed, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 {
		t.Errorf("mode change should bypass bot-comment check, got processed=%d", processed)
	}
}

func TestFetcher_StageChangeBypassesGraceWindowToRunRefinement(t *testing.T) {
	reviewedAt := time.Now().Add(-2 * time.Minute)
	commentedAt := reviewedAt.Add(10 * time.Second)
	issue := fixture(1, commentedAt.Add(5*time.Second))
	issue.Mode = config.IssueModeRefinement
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row: &store.Issue{ID: 10, GithubID: issue.ID},
			review: &store.IssueReview{
				IssueID:     10,
				CreatedAt:   reviewedAt,
				CommentedAt: commentedAt,
				ActionTaken: string(config.IssueModeReviewOnly),
			},
		},
	}}
	pub := &fakeIssuePublisher{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, &fakePipeline{})
	f.SetPublisher(pub)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", stagePipelineCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 || len(pub.refinement) != 1 || pub.refinement[0] != 1 {
		t.Fatalf("stage change should run refinement despite grace window, processed=%d pub=%v", processed, pub.refinement)
	}
}

func TestFetcher_StageChangeBypassesGraceWindowToRunDevelop(t *testing.T) {
	reviewedAt := time.Now().Add(-2 * time.Minute)
	commentedAt := reviewedAt.Add(10 * time.Second)
	issue := fixture(2, commentedAt.Add(5*time.Second))
	issue.ID = int64(2002)
	issue.Mode = config.IssueModeDevelop
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row: &store.Issue{ID: 20, GithubID: issue.ID},
			review: &store.IssueReview{
				IssueID:     20,
				CreatedAt:   reviewedAt,
				CommentedAt: commentedAt,
				ActionTaken: string(config.IssueModeRefinement),
			},
		},
	}}
	pub := &fakeIssuePublisher{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, dedup, &fakePipeline{})
	f.SetPublisher(pub)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", stagePipelineCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 || len(pub.implement) != 1 || pub.implement[0] != 2 {
		t.Fatalf("stage change should run develop despite grace window, processed=%d pub=%v", processed, pub.implement)
	}
}

func TestFetcher_ManualStageLabelChangeAuditsAndDispatchesNewStage(t *testing.T) {
	now := time.Now()
	issue := &github.Issue{
		ID:        int64(1002),
		Number:    2,
		Repo:      "org/repo",
		Title:     "Needs plan",
		UpdatedAt: now,
		Mode:      config.IssueModeRefinement,
	}
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row: &store.Issue{ID: 11, GithubID: issue.ID},
			review: &store.IssueReview{
				IssueID:     11,
				CommentedAt: now.Add(-2 * time.Minute),
				ActionTaken: string(config.IssueModeReviewOnly),
			},
		},
	}}
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{"org/repo#2": {}}}
	pub := &fakeIssuePublisher{}
	stage := &fakeStageTransitionClient{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, &fakePipeline{})
	f.SetPublisher(pub)
	f.SetStageTransitioner(stage, nil)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", config.IssueTrackingConfig{
		Enabled:          true,
		ReviewOnlyLabels: []string{"triage"},
		RefinementLabels: []string{"refine"},
		DevelopLabels:    []string{"develop"},
	}, "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 || len(pub.refinement) != 1 || pub.refinement[0] != 2 {
		t.Fatalf("expected refinement dispatch, processed=%d pub=%v", processed, pub.refinement)
	}
	if len(stage.added) != 1 || stage.added[0] != "refine" {
		t.Fatalf("stage added = %v, want [refine]", stage.added)
	}
	if len(stage.removed) != 1 || stage.removed[0] != "triage,develop" {
		t.Fatalf("stage removed = %v, want [triage,develop]", stage.removed)
	}
	if len(stage.comments) != 1 || !strings.Contains(stage.comments[0], "manual GitHub label change") {
		t.Fatalf("stage audit comment = %q, want manual GitHub trigger", stage.comments)
	}
}

func TestFetcher_ManualStageLabelChangeSkipsAlreadyAuditedTransition(t *testing.T) {
	now := time.Now()
	issue := &github.Issue{
		ID:        int64(1003),
		Number:    3,
		Repo:      "org/repo",
		Title:     "Needs plan",
		UpdatedAt: now,
		Mode:      config.IssueModeRefinement,
		Labels:    issueLabels("refine"),
	}
	latestReviewAt := now.Add(-5 * time.Minute)
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row: &store.Issue{ID: 12, GithubID: issue.ID},
			review: &store.IssueReview{
				IssueID:     12,
				CommentedAt: latestReviewAt,
				ActionTaken: string(config.IssueModeReviewOnly),
			},
		},
	}}
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{
		"org/repo#3": {
			{
				Author:    "heimdallm-bot",
				CreatedAt: latestReviewAt.Add(time.Minute),
				Body: issues.StagePromotionAuditComment(
					issues.IssueStageTriage,
					issues.IssueStageRefinement,
					issues.StagePromotionAuto,
					latestReviewAt.Add(time.Minute),
				),
			},
		},
	}}
	pub := &fakeIssuePublisher{}
	stage := &fakeStageTransitionClient{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, &fakePipeline{})
	f.SetPublisher(pub)
	f.SetStageTransitioner(stage, nil)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", stagePipelineCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 || len(pub.refinement) != 1 || pub.refinement[0] != 3 {
		t.Fatalf("expected refinement dispatch, processed=%d pub=%v", processed, pub.refinement)
	}
	if len(stage.added) != 0 || len(stage.removed) != 0 || len(stage.comments) != 0 {
		t.Fatalf("already-audited transition should not mutate labels or comment, added=%v removed=%v comments=%v",
			stage.added, stage.removed, stage.comments)
	}
}

func TestFetcher_ManualStageLabelChangeRetriesLabelsWhenAuditExistsButLabelsAreStale(t *testing.T) {
	now := time.Now()
	issue := &github.Issue{
		ID:        int64(1004),
		Number:    4,
		Repo:      "org/repo",
		Title:     "Needs plan",
		UpdatedAt: now,
		Mode:      config.IssueModeRefinement,
		Labels:    issueLabels("triage", "refine"),
	}
	latestReviewAt := now.Add(-5 * time.Minute)
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row: &store.Issue{ID: 13, GithubID: issue.ID},
			review: &store.IssueReview{
				IssueID:     13,
				CommentedAt: latestReviewAt,
				ActionTaken: string(config.IssueModeReviewOnly),
			},
		},
	}}
	auditAt := latestReviewAt.Add(time.Minute)
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{
		"org/repo#4": {{
			Author:    "heimdallm-bot",
			CreatedAt: auditAt,
			Body: issues.StagePromotionAuditComment(
				issues.IssueStageTriage,
				issues.IssueStageRefinement,
				issues.StagePromotionAuto,
				auditAt,
			),
		}},
	}}
	pub := &fakeIssuePublisher{}
	stage := &fakeStageTransitionClient{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, &fakePipeline{})
	f.SetPublisher(pub)
	f.SetStageTransitioner(stage, nil)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", stagePipelineCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 || len(pub.refinement) != 1 || pub.refinement[0] != 4 {
		t.Fatalf("expected refinement dispatch, processed=%d pub=%v", processed, pub.refinement)
	}
	if len(stage.added) != 1 || stage.added[0] != "refine" {
		t.Fatalf("stage added = %v, want [refine]", stage.added)
	}
	if len(stage.removed) != 1 || stage.removed[0] != "triage,develop" {
		t.Fatalf("stage removed = %v, want [triage,develop]", stage.removed)
	}
	if len(stage.comments) != 0 {
		t.Fatalf("existing audit should suppress duplicate comment while labels retry, comments=%v", stage.comments)
	}
}

func TestFetcher_ManualStageLabelChangeAuditsWhenExistingAuditIsDifferentTransition(t *testing.T) {
	now := time.Now()
	issue := &github.Issue{
		ID:        int64(1005),
		Number:    5,
		Repo:      "org/repo",
		Title:     "Needs plan",
		UpdatedAt: now,
		Mode:      config.IssueModeRefinement,
		Labels:    issueLabels("refine"),
	}
	latestReviewAt := now.Add(-2 * time.Minute)
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row: &store.Issue{ID: 14, GithubID: issue.ID},
			review: &store.IssueReview{
				IssueID:     14,
				CommentedAt: latestReviewAt,
				ActionTaken: string(config.IssueModeReviewOnly),
			},
		},
	}}
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{
		"org/repo#5": {{
			Author:    "heimdallm-bot",
			CreatedAt: latestReviewAt.Add(time.Minute),
			Body: issues.StagePromotionAuditComment(
				issues.IssueStageTriage,
				issues.IssueStageDevelopment,
				issues.StagePromotionAuto,
				latestReviewAt.Add(time.Minute),
			),
		}},
	}}
	stage := &fakeStageTransitionClient{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, &fakePipeline{})
	f.SetPublisher(&fakeIssuePublisher{})
	f.SetStageTransitioner(stage, nil)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", stagePipelineCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if len(stage.comments) != 1 || !strings.Contains(stage.comments[0], "- To: `refinement`") {
		t.Fatalf("different existing audit should not suppress refinement audit, comments=%v", stage.comments)
	}
}

func TestFetcher_ManualStageLabelChangeAuditsWhenExistingAuditPredatesLatestReview(t *testing.T) {
	now := time.Now()
	issue := &github.Issue{
		ID:        int64(1006),
		Number:    6,
		Repo:      "org/repo",
		Title:     "Needs plan",
		UpdatedAt: now,
		Mode:      config.IssueModeRefinement,
		Labels:    issueLabels("refine"),
	}
	latestReviewAt := now.Add(-2 * time.Minute)
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row: &store.Issue{ID: 15, GithubID: issue.ID},
			review: &store.IssueReview{
				IssueID:     15,
				CommentedAt: latestReviewAt,
				ActionTaken: string(config.IssueModeReviewOnly),
			},
		},
	}}
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{
		"org/repo#6": {{
			Author:    "heimdallm-bot",
			CreatedAt: latestReviewAt.Add(-time.Minute),
			Body: issues.StagePromotionAuditComment(
				issues.IssueStageTriage,
				issues.IssueStageRefinement,
				issues.StagePromotionAuto,
				latestReviewAt.Add(-time.Minute),
			),
		}},
	}}
	stage := &fakeStageTransitionClient{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, &fakePipeline{})
	f.SetPublisher(&fakeIssuePublisher{})
	f.SetStageTransitioner(stage, nil)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", stagePipelineCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if len(stage.comments) != 1 || !strings.Contains(stage.comments[0], "- To: `refinement`") {
		t.Fatalf("old audit should not suppress new transition audit, comments=%v", stage.comments)
	}
}

func TestFetcher_ManualStageLabelChangeDedupFallsBackToCreatedAt(t *testing.T) {
	now := time.Now()
	issue := &github.Issue{
		ID:        int64(1007),
		Number:    7,
		Repo:      "org/repo",
		Title:     "Needs plan",
		UpdatedAt: now,
		Mode:      config.IssueModeRefinement,
		Labels:    issueLabels("refine"),
	}
	latestReviewAt := now.Add(-5 * time.Minute)
	dedup := &fakeDedup{byGithubID: map[int64]dedupEntry{
		issue.ID: {
			row: &store.Issue{ID: 16, GithubID: issue.ID},
			review: &store.IssueReview{
				IssueID:     16,
				CreatedAt:   latestReviewAt,
				ActionTaken: string(config.IssueModeReviewOnly),
			},
		},
	}}
	mf := &fakeMarkerFetcher{commentsByKey: map[string][]github.Comment{
		"org/repo#7": {{
			Author:    "heimdallm-bot",
			CreatedAt: latestReviewAt.Add(time.Minute),
			Body: issues.StagePromotionAuditComment(
				issues.IssueStageTriage,
				issues.IssueStageRefinement,
				issues.StagePromotionAuto,
				latestReviewAt.Add(time.Minute),
			),
		}},
	}}
	stage := &fakeStageTransitionClient{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, mf, dedup, &fakePipeline{})
	f.SetPublisher(&fakeIssuePublisher{})
	f.SetStageTransitioner(stage, nil)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", stagePipelineCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if len(stage.added) != 0 || len(stage.removed) != 0 || len(stage.comments) != 0 {
		t.Fatalf("CreatedAt fallback should dedup applied transition, added=%v removed=%v comments=%v",
			stage.added, stage.removed, stage.comments)
	}
}

func TestFetcher_PublishesRefinementMode(t *testing.T) {
	now := time.Now()
	issue := fixture(9, now)
	issue.Mode = config.IssueModeRefinement
	pub := &fakeIssuePublisher{}
	f := issues.NewFetcher(&fakeClient{issues: []*github.Issue{issue}}, nil, &fakeDedup{}, &fakePipeline{})
	f.SetPublisher(pub)

	processed, err := f.ProcessRepo(context.Background(), "org/repo", enabledCfg(), "alice", noOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if len(pub.refinement) != 1 || pub.refinement[0] != 9 {
		t.Fatalf("refinement publishes = %v, want [9]", pub.refinement)
	}
	if len(pub.triage) != 0 || len(pub.implement) != 0 {
		t.Fatalf("unexpected publishes triage=%v implement=%v", pub.triage, pub.implement)
	}
}
