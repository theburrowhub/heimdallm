package issues_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/store"
)

// Integration test that wires a real *store.Store to both the Fetcher and a
// real *Pipeline, with fakes only for the network-facing edges (GitHub and
// the CLI executor). Covers the end-to-end flow the reviewers wanted an
// integration-level guard on.
func TestIntegration_FetcherDrivesPipelineEndToEnd(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Two issues: one plain review_only, one classified develop but without a
	// working directory — the pipeline must downgrade it to review_only and
	// both should still end up triaged.
	now := time.Now().UTC().Truncate(time.Second)
	reviewOnly := &github.Issue{
		ID: 1001, Number: 1, Repo: "org/repo",
		Title: "Needs triage", Body: "Please look at this.",
		State: "open", User: github.User{Login: "reporter"},
		Labels:    []github.Label{{Name: "question"}},
		Assignees: []github.User{},
		CreatedAt: now, UpdatedAt: now,
		Mode: config.IssueModeReviewOnly,
	}
	developFallback := &github.Issue{
		ID: 1002, Number: 2, Repo: "org/repo",
		Title: "Fix crash", Body: "Null pointer in auth.",
		State: "open", User: github.User{Login: "reporter"},
		Labels:    []github.Label{{Name: "bug"}},
		Assignees: []github.User{},
		CreatedAt: now, UpdatedAt: now,
		Mode: config.IssueModeDevelop, // no WorkDir in RunOptions → fallback
	}

	client := &fakeClient{issues: []*github.Issue{reviewOnly, developFallback}}
	gh := &fakeGH{}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte(validResult)}
	broker := &fakeBroker{}

	pipe := issues.New(s, gh, exec, nil, broker, nil)
	fetcher := issues.NewFetcher(client, gh, s, pipe)

	processed, err := fetcher.ProcessRepo(
		context.Background(),
		"org/repo",
		config.IssueTrackingConfig{Enabled: true},
		"reporter",
		func(_ *github.Issue) issues.RunOptions { return issues.RunOptions{Primary: "claude"} },
	)
	if err != nil {
		t.Fatalf("ProcessRepo: %v", err)
	}
	if processed != 2 {
		t.Fatalf("expected both issues processed (fallback preserves processing), got %d", processed)
	}

	// Store has both issues + both reviews.
	list, err := s.ListIssues()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 issues in store, got %d", len(list))
	}
	for _, row := range list {
		latest, err := s.LatestIssueReview(row.ID)
		if err != nil {
			t.Fatalf("latest review for issue %d: %v", row.ID, err)
		}
		if latest.ActionTaken != string(config.IssueModeReviewOnly) {
			t.Errorf("issue %d action_taken=%q, want review_only (incl. fallback)",
				row.Number, latest.ActionTaken)
		}
	}

	// Both comments landed on GitHub.
	if len(gh.postCalls) != 2 {
		t.Errorf("expected 2 PostComment calls, got %d", len(gh.postCalls))
	}

	// Second pass immediately after: the grace window should skip both.
	processed2, err := fetcher.ProcessRepo(
		context.Background(),
		"org/repo",
		config.IssueTrackingConfig{Enabled: true},
		"reporter",
		func(_ *github.Issue) issues.RunOptions { return issues.RunOptions{Primary: "claude"} },
	)
	if err != nil {
		t.Fatalf("second ProcessRepo: %v", err)
	}
	if processed2 != 0 {
		t.Errorf("dedup: expected 0 re-processed within grace, got %d", processed2)
	}
	if len(gh.postCalls) != 2 {
		t.Errorf("dedup: expected no new PostComment calls, got %d total", len(gh.postCalls))
	}
}

// TestIntegration_ConcurrentPipelineRunsCollapseToOneDispatch locks in
// the persistent in-flight claim behaviour from #292: two concurrent
// Run calls on the same (github_issue_id, updated_at) snapshot must
// reach the LLM executor exactly once. The second call is expected to
// see the claim already taken and return (nil, nil) immediately.
//
// The coordination is deterministic: goroutine A runs first and blocks
// inside ExecuteRaw (holding the claim); the test waits for that block
// via a channel before launching goroutine B, so B is guaranteed to
// contend on the claim. A 2-second timeout on each wait keeps a
// regression from deadlocking the suite.
func TestIntegration_ConcurrentPipelineRunsCollapseToOneDispatch(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	issue := &github.Issue{
		ID: 2001, Number: 7, Repo: "org/repo",
		Title: "Concurrent triage", Body: ".",
		State: "open", User: github.User{Login: "reporter"},
		Labels: []github.Label{}, Assignees: []github.User{},
		CreatedAt: now, UpdatedAt: now,
		Mode: config.IssueModeReviewOnly,
	}

	holdingClaim := make(chan struct{})
	release := make(chan struct{})
	var claimSignaler sync.Once
	var execCount int32
	bexec := &blockingExec{
		cli: "claude",
		out: []byte(validResult),
		onCall: func() {
			atomic.AddInt32(&execCount, 1)
			claimSignaler.Do(func() { close(holdingClaim) })
			<-release
		},
	}

	gh := &fakeGH{}
	broker := &fakeBroker{}
	pipe := issues.New(s, gh, bexec, nil, broker, nil)

	opts := issues.RunOptions{Primary: "claude"}

	// Goroutine A: will claim and block in ExecuteRaw until release.
	doneA := make(chan error, 1)
	go func() {
		_, err := pipe.Run(context.Background(), issue, opts)
		doneA <- err
	}()

	// Wait up to 2 s for A to be inside ExecuteRaw holding the claim.
	select {
	case <-holdingClaim:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("goroutine A never reached ExecuteRaw; claim path is broken")
	}

	// Goroutine B: guaranteed to see the claim already taken. Should
	// return (nil, nil) immediately without hitting the executor.
	doneB := make(chan error, 1)
	go func() {
		_, err := pipe.Run(context.Background(), issue, opts)
		doneB <- err
	}()

	select {
	case err := <-doneB:
		if err != nil {
			t.Fatalf("goroutine B should return (nil, nil) when claim is taken, got err=%v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("goroutine B blocked; claim is not short-circuiting Run")
	}

	// Now release A so the test can finish cleanly.
	close(release)
	select {
	case err := <-doneA:
		if err != nil {
			t.Fatalf("goroutine A returned err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine A never completed after release")
	}

	if n := atomic.LoadInt32(&execCount); n != 1 {
		t.Errorf("expected exactly 1 ExecuteRaw call (concurrent dispatches must collapse), got %d", n)
	}
	if len(gh.postCalls) != 1 {
		t.Errorf("expected 1 PostComment call (one triage landed), got %d", len(gh.postCalls))
	}
}

// blockingExec is a CLIExecutor whose ExecuteRaw invokes `onCall` (if
// set) before returning. Tests use it to insert a sync primitive between
// "pipeline took the claim" and "pipeline releases the claim", so two
// goroutines can deterministically contend on the claim. `called` is an
// atomic.Bool other tests use to assert the executor was or was not
// reached at all (circuit-breaker short-circuit) — kept atomic so the
// test stays clean under -race even if a future regression in claim
// dedup lets two goroutines reach ExecuteRaw concurrently.
type blockingExec struct {
	cli    string
	out    []byte
	onCall func()
	called atomic.Bool
}

func (e *blockingExec) Detect(_, _ string) (string, error) { return e.cli, nil }

func (e *blockingExec) ExecuteRaw(_, _ string, _ executor.ExecOptions) ([]byte, error) {
	e.called.Store(true)
	if e.onCall != nil {
		e.onCall()
	}
	return e.out, nil
}

// TestIntegration_IssueCircuitBreakerTripsAfterCap locks in the Fix 3
// behaviour from #292: after PerIssue24h successful triages on the same
// issue, the next Run must short-circuit with a *CircuitBreakerError
// and MUST NOT call the LLM executor. Uses a real store (so row counts
// are authoritative) and pre-seeds issue_reviews to reach the cap.
func TestIntegration_IssueCircuitBreakerTripsAfterCap(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	ghIssue := &github.Issue{
		ID: 3001, Number: 11, Repo: "org/repo",
		Title: "Cost runaway candidate", Body: ".",
		State: "open", User: github.User{Login: "reporter"},
		Labels: []github.Label{}, Assignees: []github.User{},
		CreatedAt: now, UpdatedAt: now,
		Mode: config.IssueModeReviewOnly,
	}

	// Upsert the issue so we can seed reviews keyed on its store ID.
	storeIssue := &store.Issue{
		GithubID: ghIssue.ID, Repo: ghIssue.Repo, Number: ghIssue.Number,
		Title: ghIssue.Title, Body: ghIssue.Body, Author: ghIssue.User.Login,
		State:     ghIssue.State,
		CreatedAt: ghIssue.CreatedAt, FetchedAt: now,
	}
	storeID, err := s.UpsertIssue(storeIssue)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Seed 3 triages within the 24h window — at the cap.
	for i := 0; i < 3; i++ {
		if _, err := s.InsertIssueReview(&store.IssueReview{
			IssueID: storeID, CLIUsed: "claude",
			Summary:     "prior",
			Triage:      "{}",
			Suggestions: "[]",
			ActionTaken: string(config.IssueModeReviewOnly),
			CreatedAt:   time.Now().Add(time.Duration(-i) * time.Minute),
		}); err != nil {
			t.Fatalf("seed review %d: %v", i, err)
		}
	}

	bexec := &blockingExec{cli: "claude", out: []byte(validResult)}
	pipe := issues.New(s, &fakeGH{}, bexec, nil, &fakeBroker{}, nil)
	pipe.SetCircuitBreakerLimits(&store.IssueCircuitBreakerLimits{
		PerIssue24h: 3,
		PerRepoHr:   10,
	})

	// Contract: the pipeline takes the in-flight claim on issue.ID
	// (GitHub ID), then upserts the issue, and only THEN calls
	// CheckIssueCircuitBreaker with the internal store ID. This test
	// exercises that ordering — if the breaker call ever moved before
	// the upsert, it would receive a zero issue_id and the count would
	// always come back 0, silently disarming the breaker.
	_, runErr := pipe.Run(context.Background(), ghIssue, issues.RunOptions{Primary: "claude"})

	var cbErr *issues.CircuitBreakerError
	if !errors.As(runErr, &cbErr) {
		t.Fatalf("expected *issues.CircuitBreakerError, got %v", runErr)
	}
	if cbErr.Reason == "" {
		t.Errorf("CircuitBreakerError.Reason empty; telemetry relies on it")
	}
	if !errors.Is(runErr, issues.ErrCircuitBreakerTripped) {
		t.Errorf("expected errors.Is match on ErrCircuitBreakerTripped, got nope")
	}
	// LLM must not have been called.
	if bexec.called.Load() {
		t.Errorf("circuit breaker should short-circuit BEFORE ExecuteRaw; executor was called")
	}

	// A second Run on the same (issue, updated_at) MUST short-circuit
	// silently (return nil, nil) because the in-flight claim is held
	// across breaker trips. This is the regression guard for the
	// notification-spam concern raised in code review on PR #296: if the
	// defer released the claim on trip, the next tick would re-acquire,
	// re-trip, and re-fire the operator notification.
	notifier := &countingNotifier{}
	pipe2 := issues.New(s, &fakeGH{}, &blockingExec{cli: "claude", out: []byte(validResult)},
		nil, &fakeBroker{}, notifier)
	pipe2.SetCircuitBreakerLimits(&store.IssueCircuitBreakerLimits{
		PerIssue24h: 3,
		PerRepoHr:   10,
	})
	rev2, runErr2 := pipe2.Run(context.Background(), ghIssue, issues.RunOptions{Primary: "claude"})
	if runErr2 != nil {
		t.Fatalf("second Run on held claim should return (nil, nil), got err=%v", runErr2)
	}
	if rev2 != nil {
		t.Errorf("second Run should return nil review, got %+v", rev2)
	}
	if notifier.count() != 0 {
		t.Errorf("held claim must suppress re-notify on the same snapshot, got %d notify calls", notifier.count())
	}
}

// countingNotifier records how many times Notify was called so tests can
// assert breaker-trip notifications are not repeated on the same snapshot.
type countingNotifier struct {
	mu  sync.Mutex
	n   int
}

func (c *countingNotifier) Notify(_, _ string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *countingNotifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestIntegration_RecomputeGraceIsExportedForMainPipeline(t *testing.T) {
	// The constant is exported specifically so main.go in #28 can align the
	// PR pipeline's grace window with the issue fetcher's. Locking the value
	// (and its exported status) here prevents an accidental private rename
	// from silently drifting the two pipelines apart.
	if issues.RecomputeGrace != 30*time.Second {
		t.Errorf("RecomputeGrace changed to %v — confirm with the PR pipeline's grace value in main.go before updating",
			issues.RecomputeGrace)
	}
}
