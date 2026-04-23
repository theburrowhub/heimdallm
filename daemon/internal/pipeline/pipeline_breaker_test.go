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

// fakeGHBreaker implements the pipeline's github dependency for the circuit
// breaker integration test. Tracks whether SubmitReview / Execute were called
// so the test can assert the breaker short-circuits BEFORE spending Claude
// credits (the entire point of Fix 1 in issue theburrowhub/heimdallm#243).
type fakeGHBreaker struct {
	headSHAValue string
	submitted    bool
}

func (f *fakeGHBreaker) GetPRHeadSHA(_ string, _ int) (string, error) {
	return f.headSHAValue, nil
}

func (f *fakeGHBreaker) FetchDiff(_ string, _ int) (string, error) {
	return "+line", nil
}

func (f *fakeGHBreaker) SubmitReview(_ string, _ int, _, _ string) (int64, string, error) {
	f.submitted = true
	return 0, "", nil
}

func (f *fakeGHBreaker) PostComment(_ string, _ int, _ string) (time.Time, error) {
	return time.Now().UTC(), nil
}

func (f *fakeGHBreaker) FetchComments(_ string, _ int) ([]gh.Comment, error) {
	return nil, nil
}

// fakeExecBreaker tracks whether Execute was invoked — the breaker must
// short-circuit before this runs so Claude credits are not spent.
type fakeExecBreaker struct {
	calls int
}

func (f *fakeExecBreaker) Detect(_, _ string) (string, error) { return "fake_claude", nil }
func (f *fakeExecBreaker) Execute(_, _ string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	f.calls++
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

// TestRun_CircuitBreakerTripStopsExecute verifies that when the per-PR cap
// is reached, pipeline.Run refuses to call SubmitReview / Execute. This is
// the unconditional ceiling that caps worst-case cost when every other
// dedup defense fails (see issue #243).
//
// The test drives 3 reviews through the pipeline itself (rather than
// pre-seeding the reviews table) so that the prID the pipeline sees on each
// Run matches the pr_id value stored in the reviews rows — the circuit
// breaker counts by prID, and any mismatch would silently fail the test.
func TestRun_CircuitBreakerTripStopsExecute(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	fgh := &fakeGHBreaker{}
	fexec := &fakeExecBreaker{}
	p := pipeline.New(s, fgh, fexec, &fakeNotify{})
	// Cap at 3 per PR so the 4th review is the one that must trip.
	p.SetCircuitBreakerLimits(&store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 999})

	// Drive 3 successful reviews via the pipeline — each with a distinct
	// HEAD SHA so the HEAD-SHA dedup does not short-circuit. This populates
	// the reviews table with 3 rows all pointing at the correct pr_id.
	for i, sha := range []string{"sha1", "sha2", "sha3"} {
		fgh.submitted = false
		pr := &gh.PullRequest{
			ID: 42, Number: 42, Title: "t", Repo: "org/r",
			User: gh.User{Login: "alice"}, State: "open",
			UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/r/pull/42",
			Head: gh.Branch{SHA: sha},
		}
		if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"}); err != nil {
			t.Fatalf("seed run %d: %v", i, err)
		}
	}
	// Sanity: executor should have been called 3 times so far.
	if fexec.calls != 3 {
		t.Fatalf("seed: executor calls = %d, want 3", fexec.calls)
	}

	// 4th run on a new HEAD SHA — the HEAD-SHA dedup passes (new commit),
	// so the breaker is the only defense left. It MUST trip.
	fgh.submitted = false
	before := fexec.calls
	pr := &gh.PullRequest{
		ID: 42, Number: 42, Title: "t", Repo: "org/r",
		User: gh.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/r/pull/42",
		Head: gh.Branch{SHA: "sha4"},
	}
	_, err = p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	// Verify both the typed-error contract and the sentinel: callers in main.go
	// rely on errors.As(&CircuitBreakerError) to emit the SSE event with the
	// specific reason, and anyone who just cares "was this a breaker trip?"
	// can use errors.Is against the sentinel.
	var cbErr *pipeline.CircuitBreakerError
	if err == nil || !errors.As(err, &cbErr) {
		t.Fatalf("expected *pipeline.CircuitBreakerError, got %v", err)
	}
	if !errors.Is(err, pipeline.ErrCircuitBreakerTripped) {
		t.Errorf("errors.Is(err, ErrCircuitBreakerTripped) = false, want true")
	}
	if cbErr.Reason == "" {
		t.Errorf("CircuitBreakerError.Reason is empty; callers need it for telemetry")
	}
	if fexec.calls != before {
		t.Errorf("executor must not be called when breaker trips (calls=%d before=%d)", fexec.calls, before)
	}
	if fgh.submitted {
		t.Errorf("SubmitReview must not be called when breaker trips")
	}
}
