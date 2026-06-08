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

// TestRun_CircuitBreakerTripStopsExecute verifies that when the per-PR HEAD
// cap is reached, pipeline.Run refuses to call SubmitReview / Execute. This
// is the unconditional ceiling that caps worst-case cost when every other
// dedup defense fails (see issue #243).
func TestRun_CircuitBreakerTripStopsExecute(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	fgh := &fakeGHBreaker{}
	fexec := &fakeExecBreaker{}
	notify := &fakeNotify{}
	pub := &fakePublisher{}
	p := pipeline.New(s, fgh, fexec, notify)
	p.SetPublisher(pub)
	p.SetBotLogin("heimdallm-bot")
	// Cap at 3 per PR HEAD so the next explicit re-request on the same commit
	// is the one that must trip.
	p.SetCircuitBreakerLimits(&store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 999})

	now := time.Now().UTC()
	prID, err := s.UpsertPR(&store.PR{
		GithubID:  42,
		Repo:      "org/r",
		Number:    42,
		Title:     "t",
		Author:    "alice",
		URL:       "https://github.com/org/r/pull/42",
		State:     "open",
		UpdatedAt: now,
		FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	for i := 0; i < 3; i++ {
		createdAt := now.Add(time.Duration(-3+i) * time.Minute)
		if _, err := s.InsertReview(&store.Review{
			PRID:              prID,
			CLIUsed:           "claude",
			Issues:            "[]",
			Suggestions:       "[]",
			Severity:          "low",
			CreatedAt:         createdAt,
			PublishedAt:       createdAt,
			GitHubReviewID:    int64(9000 + i),
			GitHubReviewState: "APPROVED",
			HeadSHA:           "sha1",
		}); err != nil {
			t.Fatalf("insert review %d: %v", i, err)
		}
	}

	// SHA dedup would normally skip same-commit reviews. Simulate an explicit
	// GitHub re-request after the latest local review so the pipeline bypasses
	// SHA dedup and the circuit breaker remains the last defense.
	p.SetTimelineFetcher(&fakeTimeline{events: []gh.TimelineEvent{
		{Event: "review_requested", Actor: "alice", CreatedAt: now.Add(time.Minute)},
	}})

	before := fexec.calls
	pr := &gh.PullRequest{
		ID: 42, Number: 42, Title: "t", Repo: "org/r",
		User: gh.User{Login: "alice"}, State: "open",
		UpdatedAt: now.Add(time.Minute), HTMLURL: "https://github.com/org/r/pull/42",
		Head: gh.Branch{SHA: "sha1"},
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

	// #322 review feedback: notify "PR Review Started" and the
	// EventReviewStarted SSE must NOT fire on a breaker-trip path.
	// Pre-fix, both lived above the breaker check, leaving Flutter with a
	// phantom spinner and the operator with a phantom desktop notification
	// every time the cap clamped down.
	startedNotifies := countNotify(notify.events, "PR Review Started")
	if startedNotifies != 0 {
		t.Errorf("notify(\"PR Review Started\"): got %d, want 0 on breaker trip", startedNotifies)
	}
	startedSSEs := 0
	for _, ev := range pub.types() {
		if ev == "review_started" {
			startedSSEs++
		}
	}
	if startedSSEs != 0 {
		t.Errorf("EventReviewStarted: got %d, want 0 on breaker trip", startedSSEs)
	}
}
