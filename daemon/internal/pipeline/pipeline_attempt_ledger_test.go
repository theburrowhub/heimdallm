package pipeline_test

import (
	"errors"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

// failingExec reproduces the incident's failure mode: the CLI runs, bills for
// the work, and then returns output the pipeline cannot parse. Run aborts before
// step 7, so no review row is ever written.
type failingExec struct {
	calls int
}

func (f *failingExec) Detect(_, _ string) (string, error) { return "fake_claude", nil }

func (f *failingExec) Execute(_, _ string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	f.calls++
	return nil, errors.New("executor: parse JSON result: invalid character 'e' looking for beginning of object key string")
}

// attemptGH is a minimal github dependency; nothing here should be reached once
// the breaker trips.
type attemptGH struct{ sha string }

func (a *attemptGH) GetPRHeadSHA(_ string, _ int) (string, error) { return a.sha, nil }
func (a *attemptGH) FetchDiff(_ string, _ int) (string, error)    { return "+line", nil }
func (a *attemptGH) SubmitReview(_ string, _ int, _, _ string) (int64, string, error) {
	return 0, "", nil
}
func (a *attemptGH) PostComment(_ string, _ int, _ string) (time.Time, error) {
	return time.Now().UTC(), nil
}
func (a *attemptGH) FetchComments(_ string, _ int) ([]gh.Comment, error) { return nil, nil }

// TestRun_FailedRunsCountTowardTheBreaker is the end-to-end regression test for
// theburrowhub/heimdallm#663.
//
// Every run dies inside Execute, so none of them writes a review row. The old
// breaker counted review rows, which meant its counter never moved and the same
// commit could be re-run at full price without limit — the incident showed two
// full-price runs 37s apart on 31615409, and nothing in the code would have
// stopped a third, or a thirtieth.
//
// With the attempt ledger the configured cap of 3 must be enforced against the
// failures themselves: the fourth call must trip the breaker and must NOT reach
// the CLI.
func TestRun_FailedRunsCountTowardTheBreaker(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	const sha = "31615409a9152613021ddccfec2c6e2001d4575d"
	fexec := &failingExec{}
	p := pipeline.New(s, &attemptGH{sha: sha}, fexec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")
	p.SetCircuitBreakerLimits(&store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 999})

	pr := &gh.PullRequest{
		ID: 12, Number: 12, Title: "wails frontend", Repo: "theburrowhub/thaimaturgy",
		User: gh.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/theburrowhub/thaimaturgy/pull/12",
	}
	pr.Head.SHA = sha
	opts := pipeline.RunOptions{Primary: "claude", Fallback: "gemini"}

	// Three failing runs are allowed: the cap is 3, and the breaker must not
	// fire early or it would block legitimate work.
	for i := 1; i <= 3; i++ {
		_, runErr := p.Run(pr, opts)
		if runErr == nil {
			t.Fatalf("run %d: expected the parse failure to surface", i)
		}
		var breakerErr *pipeline.CircuitBreakerError
		if errors.As(runErr, &breakerErr) {
			t.Fatalf("run %d tripped the breaker early: %v", i, runErr)
		}
	}
	if fexec.calls != 3 {
		t.Fatalf("Execute calls = %d, want 3 (nothing should have blocked the first three)", fexec.calls)
	}

	// The fourth attempt must be refused. Under the old behaviour this ran the
	// CLI again — and would have kept doing so indefinitely.
	_, runErr := p.Run(pr, opts)
	if runErr == nil {
		t.Fatal("fourth run returned no error; the breaker did not trip")
	}
	var breakerErr *pipeline.CircuitBreakerError
	if !errors.As(runErr, &breakerErr) {
		t.Fatalf("fourth run failed with %v, want a CircuitBreakerError — failed runs are "+
			"still not counted, so the retry loop remains unbounded (#663)", runErr)
	}
	if fexec.calls != 3 {
		t.Errorf("Execute calls = %d after the trip, want 3 — the breaker must short-circuit "+
			"before spending credits", fexec.calls)
	}

	// Sanity: the ledger is what carried the count, since no review row exists.
	storedPR, err := s.GetPRByGithubID(pr.ID)
	if err != nil {
		t.Fatalf("get pr: %v", err)
	}
	attempts, err := s.CountReviewAttemptsForPRHeadSHA(storedPR.ID, sha, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != 3 {
		t.Errorf("ledger attempts = %d, want 3", attempts)
	}
	rev, err := s.LatestReviewForPR(storedPR.ID)
	if err == nil && rev != nil {
		t.Errorf("a review row exists (id %d); the premise of this test is that none does", rev.ID)
	}
}

// TestRun_ForcedRunIsRecordedEvenThoughItBypassesTheCheck documents the
// asymmetry: Force skips the breaker CHECK because a human clicking "Re-review"
// is deliberate intent, but the spend is real, so the run is still ledgered.
// Leaving it unrecorded would recreate the same blind spot for whatever runs
// next on that commit.
func TestRun_ForcedRunIsRecordedEvenThoughItBypassesTheCheck(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	const sha = "deadbeef"
	fexec := &failingExec{}
	p := pipeline.New(s, &attemptGH{sha: sha}, fexec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")
	// Cap of 1: a forced run would trip it immediately if Force were checked.
	p.SetCircuitBreakerLimits(&store.CircuitBreakerLimits{PerPR24h: 1, PerRepoHr: 999})

	pr := &gh.PullRequest{
		ID: 99, Number: 99, Title: "t", Repo: "org/repo",
		User: gh.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/99",
	}
	pr.Head.SHA = sha

	if _, err := p.Run(pr, pipeline.RunOptions{
		Primary: "claude", Fallback: "gemini", Force: true,
	}); err == nil {
		t.Fatal("expected the parse failure to surface")
	}

	// Ran despite the cap of 1 already being satisfiable...
	if fexec.calls != 1 {
		t.Errorf("Execute calls = %d, want 1 — Force must bypass the check", fexec.calls)
	}
	// ...but the spend was still recorded.
	storedPR, err := s.GetPRByGithubID(pr.ID)
	if err != nil {
		t.Fatalf("get pr: %v", err)
	}
	attempts, err := s.CountReviewAttemptsForPRHeadSHA(storedPR.ID, sha, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != 1 {
		t.Errorf("ledger attempts = %d, want 1 — a forced run's cost must still be visible "+
			"to the runs that follow it", attempts)
	}
}
