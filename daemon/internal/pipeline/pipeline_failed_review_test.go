package pipeline_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

type terminatedExec struct {
	calls int
}

func (f *terminatedExec) Detect(_, _ string) (string, error) { return "codex", nil }

func (f *terminatedExec) Execute(_, _ string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	f.calls++
	return nil, errors.New("executor: run codex: signal: terminated")
}

type failedReviewGH struct {
	sha       string
	submitted bool
}

func (f *failedReviewGH) GetPRHeadSHA(_ string, _ int) (string, error) { return f.sha, nil }
func (f *failedReviewGH) FetchDiff(_ string, _ int) (string, error)    { return "+line", nil }
func (f *failedReviewGH) SubmitReview(_ string, _ int, _, _ string) (int64, string, error) {
	f.submitted = true
	return 0, "", nil
}
func (f *failedReviewGH) PostComment(_ string, _ int, _ string) (time.Time, error) {
	return time.Now().UTC(), nil
}
func (f *failedReviewGH) FetchComments(_ string, _ int) ([]gh.Comment, error) {
	return nil, nil
}

// TestRun_FailedExecutionsDoNotConsumeReviewQuota covers the regression where
// every invocation ended with `signal: terminated`, yet the attempts exhausted
// the review circuit breaker despite never producing a review.
func TestRun_FailedExecutionsDoNotConsumeReviewQuota(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	const sha = "31615409a9152613021ddccfec2c6e2001d4575d"
	fexec := &terminatedExec{}
	fgh := &failedReviewGH{sha: sha}
	p := pipeline.New(s, fgh, fexec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")
	p.SetCircuitBreakerLimits(&store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 3})

	pr := &gh.PullRequest{
		ID: 73, Number: 73, Title: "remove last user turn", Repo: "org/repo",
		User: gh.User{Login: "alice"}, State: "open", UpdatedAt: time.Now(),
		HTMLURL: "https://github.com/org/repo/pull/73",
	}
	pr.Head.SHA = sha

	// Exercise both automatic runs and forced retries. Force bypasses the
	// breaker check, but failed forced runs must not inflate the count observed
	// by the next automatic run.
	for i, force := range []bool{false, false, false, true, true, false} {
		_, runErr := p.Run(pr, pipeline.RunOptions{
			Primary: "codex", Fallback: "claude", Force: force,
		})
		if runErr == nil {
			t.Fatalf("run %d: expected executor failure", i+1)
		}
		var breakerErr *pipeline.CircuitBreakerError
		if errors.As(runErr, &breakerErr) || errors.Is(runErr, pipeline.ErrCircuitBreakerTripped) {
			t.Fatalf("run %d: failed execution consumed review quota: %v", i+1, runErr)
		}
		if !strings.Contains(runErr.Error(), "signal: terminated") {
			t.Fatalf("run %d: error = %v, want terminated executor error", i+1, runErr)
		}
	}

	if fexec.calls != 6 {
		t.Fatalf("Execute calls = %d, want 6; no review exists to trip the cap", fexec.calls)
	}
	if fgh.submitted {
		t.Fatal("SubmitReview called even though every execution failed")
	}

	storedPR, err := s.GetPRByGithubID(pr.ID)
	if err != nil {
		t.Fatalf("get pr: %v", err)
	}
	rev, latestErr := s.LatestReviewForPR(storedPR.ID)
	if rev != nil || latestErr == nil || !strings.Contains(latestErr.Error(), "no rows") {
		t.Fatalf("latest review = %#v, err = %v; failed executions must not create reviews", rev, latestErr)
	}
	tripped, reason, err := s.CheckCircuitBreaker(storedPR.ID, pr.Repo, sha,
		store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 3})
	if err != nil {
		t.Fatalf("check breaker: %v", err)
	}
	if tripped {
		t.Fatalf("breaker tripped without a completed review: %s", reason)
	}
}
