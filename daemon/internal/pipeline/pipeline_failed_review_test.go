package pipeline_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

type retryExec struct {
	calls     int
	err       error
	onExecute func()
}

func (f *retryExec) Detect(_, _ string) (string, error) { return "codex", nil }

func (f *retryExec) Execute(_, _ string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	f.calls++
	if f.onExecute != nil {
		f.onExecute()
	}
	if f.err != nil {
		return nil, f.err
	}
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

type failedReviewGH struct {
	sha       string
	submitted bool
}

func (f *failedReviewGH) GetPRHeadSHA(_ string, _ int) (string, error) { return f.sha, nil }
func (f *failedReviewGH) FetchDiff(_ string, _ int) (string, error)    { return "+line", nil }
func (f *failedReviewGH) SubmitReview(_ string, _ int, _, _ string) (int64, string, error) {
	f.submitted = true
	return 123, "APPROVED", nil
}
func (f *failedReviewGH) PostComment(_ string, _ int, _ string) (time.Time, error) {
	return time.Now().UTC(), nil
}
func (f *failedReviewGH) FetchComments(_ string, _ int) ([]gh.Comment, error) {
	return nil, nil
}

func retryPR(sha string) *gh.PullRequest {
	pr := &gh.PullRequest{
		ID: 73, Number: 73, Title: "remove last user turn", Repo: "org/repo",
		User: gh.User{Login: "alice"}, State: "open", UpdatedAt: time.Now(),
		HTMLURL: "https://github.com/org/repo/pull/73",
	}
	pr.Head.SHA = sha
	return pr
}

func retryPRFor(id int64, repo, sha string) *gh.PullRequest {
	pr := retryPR(sha)
	pr.ID = id
	pr.Number = int(id)
	pr.Repo = repo
	pr.HTMLURL = "https://github.com/" + repo + "/pull/test"
	return pr
}

// TestRun_FailedExecutionsBackOffWithoutConsumingReviewQuota covers both sides
// of the regression: terminated executions never trip the review breaker, but
// an immediate automatic retry also cannot run the full-price CLI forever.
func TestRun_FailedExecutionsBackOffWithoutConsumingReviewQuota(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	const sha = "31615409a9152613021ddccfec2c6e2001d4575d"
	fexec := &retryExec{err: errors.New("executor: run codex: signal: terminated")}
	fgh := &failedReviewGH{sha: sha}
	pub := &fakePublisher{}
	p := pipeline.New(s, fgh, fexec, &fakeNotify{})
	p.SetPublisher(pub)
	p.SetBotLogin("heimdallm-bot")
	p.SetCircuitBreakerLimits(&store.CircuitBreakerLimits{PerPR24h: 1, PerRepoHr: 1})
	pr := retryPR(sha)

	_, runErr := p.Run(pr, pipeline.RunOptions{Primary: "codex", Fallback: "claude"})
	if runErr == nil || !strings.Contains(runErr.Error(), "signal: terminated") {
		t.Fatalf("first run error = %v, want terminated executor error", runErr)
	}
	var breakerErr *pipeline.CircuitBreakerError
	if errors.As(runErr, &breakerErr) || errors.Is(runErr, pipeline.ErrCircuitBreakerTripped) {
		t.Fatalf("failed execution returned a breaker error: %v", runErr)
	}
	if fexec.calls != 1 {
		t.Fatalf("Execute calls after first run = %d, want 1", fexec.calls)
	}

	rev, runErr := p.Run(pr, pipeline.RunOptions{Primary: "codex", Fallback: "claude"})
	if runErr != nil || rev != nil {
		t.Fatalf("cooldown retry = review %#v, error %v; want a deferred nil result", rev, runErr)
	}
	if fexec.calls != 1 {
		t.Fatalf("immediate automatic retry called Execute; calls = %d", fexec.calls)
	}
	if got := pub.types(); !equalStringSlices(got, []string{
		sse.EventPRDetected, sse.EventReviewStarted, sse.EventReviewSkipped,
	}) {
		t.Fatalf("events = %v, want started lifecycle then one retry skip", got)
	}
	skip, ok := pub.firstOf(sse.EventReviewSkipped)
	if !ok || !strings.Contains(skip.Data, `"reason":"retry_cooldown"`) {
		t.Fatalf("retry skip event = %#v, want retry_cooldown", skip)
	}

	// Force is explicit operator intent: it bypasses the wait, but another
	// failure extends the state protecting the next automatic tick.
	_, runErr = p.Run(pr, pipeline.RunOptions{Primary: "codex", Force: true})
	if runErr == nil || !strings.Contains(runErr.Error(), "signal: terminated") {
		t.Fatalf("forced retry error = %v, want terminated executor error", runErr)
	}
	if fexec.calls != 2 {
		t.Fatalf("forced retry Execute calls = %d, want 2", fexec.calls)
	}
	if _, runErr = p.Run(pr, pipeline.RunOptions{Primary: "codex"}); runErr != nil {
		t.Fatalf("automatic retry after failed Force returned error: %v", runErr)
	}
	if fexec.calls != 2 {
		t.Fatalf("automatic retry after failed Force called Execute; calls = %d", fexec.calls)
	}

	storedPR, err := s.GetPRByGithubID(pr.ID)
	if err != nil {
		t.Fatalf("get pr: %v", err)
	}
	if review, latestErr := s.LatestReviewForPR(storedPR.ID); review != nil ||
		latestErr == nil || !strings.Contains(latestErr.Error(), "no rows") {
		t.Fatalf("latest review = %#v, err = %v; failures must not create reviews", review, latestErr)
	}
	tripped, reason, err := s.CheckCircuitBreaker(storedPR.ID, pr.Repo, sha,
		store.CircuitBreakerLimits{PerPR24h: 1, PerRepoHr: 1})
	if err != nil {
		t.Fatalf("check breaker: %v", err)
	}
	if tripped {
		t.Fatalf("breaker tripped without a completed review: %s", reason)
	}
	blocked, _, attempts, err := s.CheckReviewRetryBackoff(storedPR.ID, sha, time.Now())
	if err != nil {
		t.Fatalf("check retry backoff: %v", err)
	}
	if !blocked || attempts != 2 {
		t.Fatalf("retry state = blocked %v, attempts %d; want two failed executions", blocked, attempts)
	}
}

func TestRun_PersistedReviewClearsRetryBackoff(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	const sha = "deadbeef"
	exec := &retryExec{err: errors.New("executor: run codex: signal: terminated")}
	ghClient := &failedReviewGH{sha: sha}
	p := pipeline.New(s, ghClient, exec, &fakeNotify{})
	pr := retryPR(sha)

	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "codex"}); err == nil {
		t.Fatal("first run unexpectedly succeeded")
	}
	exec.err = nil
	review, err := p.Run(pr, pipeline.RunOptions{Primary: "codex", Force: true})
	if err != nil || review == nil {
		t.Fatalf("successful forced retry = review %#v, error %v", review, err)
	}
	storedPR, err := s.GetPRByGithubID(pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocked, _, attempts, err := s.CheckReviewRetryBackoff(storedPR.ID, sha, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if blocked || attempts != 0 {
		t.Fatalf("successful persisted review left retry state: blocked %v, attempts %d", blocked, attempts)
	}
}

func TestRun_IneligibleRepoDoesNotArmRetryBackoff(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	const sha = "cafebabe"
	exec := &retryExec{err: errors.New("must not execute")}
	p := pipeline.New(s, &failedReviewGH{sha: sha}, exec, &fakeNotify{})
	pr := retryPR(sha)
	checks := 0

	review, err := p.Run(pr, pipeline.RunOptions{
		Primary: "codex",
		RepoEligible: func(string) bool {
			checks++
			// Pass the early guard, then become ineligible at the final
			// boundary immediately before retry state and Execute.
			return checks < 2
		},
	})
	if err != nil || review != nil || exec.calls != 0 {
		t.Fatalf("ineligible run = review %#v, error %v, Execute calls %d", review, err, exec.calls)
	}
	storedPR, err := s.GetPRByGithubID(pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocked, _, attempts, err := s.CheckReviewRetryBackoff(storedPR.ID, sha, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if blocked || attempts != 0 {
		t.Fatalf("zero-spend exit armed retry state: blocked %v, attempts %d", blocked, attempts)
	}
}

func TestRun_RepoFailureLimitBoundsDifferentPRsWithoutTrippingBreaker(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	exec := &retryExec{err: errors.New("provider unavailable")}
	ghClient := &failedReviewGH{}
	pub := &fakePublisher{}
	p := pipeline.New(s, ghClient, exec, &fakeNotify{})
	p.SetPublisher(pub)
	p.SetCircuitBreakerLimits(&store.CircuitBreakerLimits{PerPR24h: 1, PerRepoHr: 1})
	p.SetReviewRetryRepoHourlyLimit(2)

	for _, run := range []struct {
		id  int64
		sha string
	}{{301, "sha-1"}, {302, "sha-2"}} {
		_, runErr := p.Run(retryPRFor(run.id, "org/repo", run.sha), pipeline.RunOptions{Primary: "codex"})
		if runErr == nil || !strings.Contains(runErr.Error(), "provider unavailable") {
			t.Fatalf("failed run %d error = %v", run.id, runErr)
		}
	}
	if exec.calls != 2 {
		t.Fatalf("Execute calls before cap = %d, want 2", exec.calls)
	}

	blockedPR := retryPRFor(303, "org/repo", "sha-3")
	review, runErr := p.Run(blockedPR, pipeline.RunOptions{Primary: "codex"})
	if runErr != nil || review != nil {
		t.Fatalf("repo-limit run = review %#v, error %v", review, runErr)
	}
	if exec.calls != 2 {
		t.Fatalf("repo-limit run called Execute; calls = %d", exec.calls)
	}
	var breakerErr *pipeline.CircuitBreakerError
	if errors.As(runErr, &breakerErr) || errors.Is(runErr, pipeline.ErrCircuitBreakerTripped) {
		t.Fatalf("retry repo limit surfaced as circuit breaker: %v", runErr)
	}
	skip, ok := pub.firstOf(sse.EventReviewSkipped)
	if !ok || !strings.Contains(skip.Data, `"reason":"retry_repo_limit"`) {
		t.Fatalf("repo-limit skip event = %#v", skip)
	}
	storedPR, err := s.GetPRByGithubID(blockedPR.ID)
	if err != nil {
		t.Fatal(err)
	}
	tripped, reason, err := s.CheckCircuitBreaker(storedPR.ID, blockedPR.Repo, blockedPR.Head.SHA,
		store.CircuitBreakerLimits{PerPR24h: 1, PerRepoHr: 1})
	if err != nil {
		t.Fatal(err)
	}
	if tripped {
		t.Fatalf("completed-review breaker tripped on failures: %s", reason)
	}

	// Repository scope and explicit operator intent remain independent. A
	// different repo proceeds automatically; Force proceeds in the capped repo
	// and its failure remains charged to later automatic retries.
	_, runErr = p.Run(retryPRFor(304, "org/other", "sha-4"), pipeline.RunOptions{Primary: "codex"})
	if runErr == nil || exec.calls != 3 {
		t.Fatalf("other repo run error = %v, calls = %d", runErr, exec.calls)
	}
	_, runErr = p.Run(retryPRFor(305, "org/repo", "sha-5"), pipeline.RunOptions{Primary: "codex", Force: true})
	if runErr == nil || exec.calls != 4 {
		t.Fatalf("forced capped-repo run error = %v, calls = %d", runErr, exec.calls)
	}
}

func TestRun_SuccessfulReviewsDoNotConsumeRepoFailureLimit(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	exec := &retryExec{}
	p := pipeline.New(s, &failedReviewGH{}, exec, &fakeNotify{})
	p.SetReviewRetryRepoHourlyLimit(1)

	for _, run := range []struct {
		id  int64
		sha string
	}{{401, "sha-1"}, {402, "sha-2"}} {
		review, runErr := p.Run(retryPRFor(run.id, "org/repo", run.sha), pipeline.RunOptions{
			Primary:                      "codex",
			ReviewFailureRepoHourlyLimit: 1,
		})
		if runErr != nil || review == nil {
			t.Fatalf("successful run %d = review %#v, error %v", run.id, review, runErr)
		}
	}
	if exec.calls != 2 {
		t.Fatalf("second successful PR was aggregate-limited; Execute calls = %d", exec.calls)
	}
}

func TestRun_RetryStateStorageFailuresRemainFailOpen(t *testing.T) {
	tests := []struct {
		name       string
		beforeRun  func(*testing.T, *store.Store)
		duringExec func(*testing.T, *store.Store)
	}{
		{
			name: "aggregate reservation",
			beforeRun: func(t *testing.T, s *store.Store) {
				if _, err := s.DB().Exec("DROP TABLE review_retry_attempts"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "per-head cooldown",
			beforeRun: func(t *testing.T, s *store.Store) {
				if _, err := s.DB().Exec("DROP TABLE review_retry_backoff"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "successful reservation cleanup",
			duringExec: func(t *testing.T, s *store.Store) {
				if _, err := s.DB().Exec("DROP TABLE review_retry_attempts"); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			if tc.beforeRun != nil {
				tc.beforeRun(t, s)
			}
			exec := &retryExec{}
			if tc.duringExec != nil {
				exec.onExecute = func() { tc.duringExec(t, s) }
			}
			p := pipeline.New(s, &failedReviewGH{}, exec, &fakeNotify{})
			review, runErr := p.Run(
				retryPRFor(int64(501+i), "org/repo", "sha"),
				pipeline.RunOptions{Primary: "codex"},
			)
			if runErr != nil || review == nil || exec.calls != 1 {
				t.Fatalf("storage failure discarded review: review %#v, error %v, calls %d", review, runErr, exec.calls)
			}
		})
	}
}
