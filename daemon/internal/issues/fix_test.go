package issues_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// ── FixRunner fakes ──────────────────────────────────────────────────────────

type fakeFixStore struct {
	mu              sync.Mutex
	count           int
	lastRespondedAt time.Time
	state           string
	reviewer        string
	stateAt         time.Time
	issue           *store.Issue
}

func (f *fakeFixStore) IncrementPRReviewFixCount(_ int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count++
	return f.count, nil
}
func (f *fakeFixStore) SetPRLastRespondedAt(_ int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastRespondedAt = at
	return nil
}
func (f *fakeFixStore) UpdatePRReviewState(_ int64, state, reviewer string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = state
	f.reviewer = reviewer
	f.stateAt = at
	return nil
}
func (f *fakeFixStore) GetIssue(_ int64) (*store.Issue, error) {
	if f.issue == nil {
		return nil, errors.New("not found")
	}
	return f.issue, nil
}

type fakeFixGH struct {
	reviews    []github.PRReview
	fetchErr   error
	postBody   string
	postCalled bool
	postErr    error
}

func (f *fakeFixGH) GetPRReviews(_ string, _ int) ([]github.PRReview, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.reviews, nil
}
func (f *fakeFixGH) PostComment(_ string, _ int, body string) (time.Time, error) {
	f.postCalled = true
	f.postBody = body
	if f.postErr != nil {
		return time.Time{}, f.postErr
	}
	return time.Now().UTC(), nil
}

type fakeFixExec struct {
	called bool
	req    issues.FixRequest
	result issues.FixResult
	err    error
}

func (f *fakeFixExec) RunFix(_ context.Context, req issues.FixRequest) (issues.FixResult, error) {
	f.called = true
	f.req = req
	return f.result, f.err
}

func makeFixRunner(t *testing.T,
	st *fakeFixStore,
	gh *fakeFixGH,
	exec *fakeFixExec,
	broker *fakeResponderBroker,
	cfg config.ReviewFixConfig,
) *issues.FixRunner {
	t.Helper()
	return issues.NewFixRunner(
		st, gh, exec, broker,
		func() config.ReviewFixConfig { return cfg },
		func() string { return "heimdallm-bot" },
	)
}

func crReview(login, body string, at time.Time) github.PRReview {
	return github.PRReview{
		User: github.User{Login: login}, State: "CHANGES_REQUESTED",
		Body: body, SubmittedAt: at,
	}
}

// ── tests ────────────────────────────────────────────────────────────────────

// pushedResult is the canonical "happy path" result the production
// executor returns when the agent produced changes that were
// successfully pushed.
func pushedResult() issues.FixResult {
	return issues.FixResult{Pushed: true, CommentBody: "Heimdallm pushed a fix."}
}

func TestFixRunner_DisabledByDefault_NoOp(t *testing.T) {
	st := &fakeFixStore{}
	gh := &fakeFixGH{reviews: []github.PRReview{crReview("alice", "rename", time.Now())}}
	exec := &fakeFixExec{result: pushedResult()}
	broker := &fakeResponderBroker{}
	r := makeFixRunner(t, st, gh, exec, broker, config.ReviewFixConfig{
		Enabled: false, PerPRLifetime: 3, CooldownSecs: 0,
	})

	if err := r.Run(context.Background(), samplePR(), 99); err != nil {
		t.Fatalf("run: %v", err)
	}
	if exec.called || gh.postCalled || st.count != 0 {
		t.Errorf("disabled path had side effects: called=%v posted=%v count=%d",
			exec.called, gh.postCalled, st.count)
	}
}

func TestFixRunner_PerPRLifetimeCap(t *testing.T) {
	st := &fakeFixStore{count: 3}
	gh := &fakeFixGH{reviews: []github.PRReview{crReview("alice", "rename", time.Now())}}
	exec := &fakeFixExec{result: pushedResult()}
	broker := &fakeResponderBroker{}
	r := makeFixRunner(t, st, gh, exec, broker, config.ReviewFixConfig{
		Enabled: true, PerPRLifetime: 3, CooldownSecs: 0,
	})

	err := r.Run(context.Background(), samplePR(), 99)
	if !errors.Is(err, issues.ErrFixCapExceeded) {
		t.Fatalf("err = %v, want ErrFixCapExceeded", err)
	}
	if exec.called {
		t.Error("executor invoked above cap")
	}
	ev := broker.eventsByType(sse.EventIssueReviewError)
	if len(ev) != 1 {
		t.Fatalf("review_error events = %d, want 1", len(ev))
	}
	var payload map[string]any
	json.Unmarshal([]byte(ev[0].Data), &payload)
	if payload["reason"] != "review_fix_cap_exceeded" {
		t.Errorf("reason = %v, want review_fix_cap_exceeded", payload["reason"])
	}
}

func TestFixRunner_CooldownRespected(t *testing.T) {
	st := &fakeFixStore{}
	gh := &fakeFixGH{reviews: []github.PRReview{crReview("alice", "rename", time.Now())}}
	exec := &fakeFixExec{result: pushedResult()}
	broker := &fakeResponderBroker{}
	r := makeFixRunner(t, st, gh, exec, broker, config.ReviewFixConfig{
		Enabled: true, PerPRLifetime: 3, CooldownSecs: 600,
	})
	pr := samplePR()
	pr.LastRespondedAt = time.Now().Add(-60 * time.Second) // inside cooldown

	if err := r.Run(context.Background(), pr, 99); err != nil {
		t.Fatalf("run: %v", err)
	}
	if exec.called || gh.postCalled {
		t.Error("executor or post invoked inside cooldown")
	}
}

// TestFixRunner_BotOnlyCRIsNoOp pins the self-loop guard for phase 3:
// a CHANGES_REQUESTED review whose author is the bot must never start
// the fix flow.
func TestFixRunner_BotOnlyCRIsNoOp(t *testing.T) {
	st := &fakeFixStore{}
	gh := &fakeFixGH{reviews: []github.PRReview{
		crReview("HeimdallM-Bot", "self-CR", time.Now()),
	}}
	exec := &fakeFixExec{result: pushedResult()}
	broker := &fakeResponderBroker{}
	r := makeFixRunner(t, st, gh, exec, broker, config.ReviewFixConfig{
		Enabled: true, PerPRLifetime: 3, CooldownSecs: 0,
	})
	if err := r.Run(context.Background(), samplePR(), 99); err != nil {
		t.Fatalf("run: %v", err)
	}
	if exec.called {
		t.Error("executor invoked on bot-only CR")
	}
	if st.count != 0 {
		t.Errorf("counter advanced on bot-only CR: %d", st.count)
	}
}

// TestFixRunner_PushedFlipsFixPushedState pins the re-arm behaviour
// for the success path: when the executor reports Pushed=true the
// runner flips ExternalReviewState to FIX_PUSHED so the next Tier 3
// tick compares against the live reviews list rather than re-firing
// on the same CR.
func TestFixRunner_PushedFlipsFixPushedState(t *testing.T) {
	st := &fakeFixStore{}
	now := time.Now()
	gh := &fakeFixGH{reviews: []github.PRReview{
		crReview("alice", "rename Foo to Bar", now),
	}}
	exec := &fakeFixExec{result: issues.FixResult{
		Pushed: true, CommentBody: "Pushed a follow-up commit.",
	}}
	broker := &fakeResponderBroker{}
	r := makeFixRunner(t, st, gh, exec, broker, config.ReviewFixConfig{
		Enabled: true, PerPRLifetime: 3, CooldownSecs: 0,
	})

	if err := r.Run(context.Background(), samplePR(), 99); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !exec.called {
		t.Fatal("executor not invoked")
	}
	if !gh.postCalled {
		t.Fatal("PostComment not invoked")
	}
	if st.state != "FIX_PUSHED" {
		t.Errorf("state = %q, want FIX_PUSHED", st.state)
	}
	if st.reviewer != "alice" {
		t.Errorf("reviewer = %q, want alice", st.reviewer)
	}
	events := broker.eventsByType(sse.EventIssueReviewCompleted)
	if len(events) != 1 {
		t.Fatalf("completed events = %d, want 1", len(events))
	}
	var payload map[string]any
	json.Unmarshal([]byte(events[0].Data), &payload)
	if payload["mode"] != "review_fix" {
		t.Errorf("mode = %v, want review_fix", payload["mode"])
	}
	if payload["pushed"] != true {
		t.Errorf("pushed = %v, want true", payload["pushed"])
	}
}

// TestFixRunner_NoChangesDoesNotFlipFixPushed pins the advisory-
// fallback semantics: when the executor returns Pushed=false (the
// agent looked at the CR and left the working tree unchanged) the
// runner posts the advisory comment but does NOT mark FIX_PUSHED.
// Leaving the state at CHANGES_REQUESTED lets a reviewer who
// supplies more context retrigger the runner — until the lifetime
// cap.
func TestFixRunner_NoChangesDoesNotFlipFixPushed(t *testing.T) {
	st := &fakeFixStore{}
	gh := &fakeFixGH{reviews: []github.PRReview{
		crReview("alice", "rename", time.Now()),
	}}
	exec := &fakeFixExec{result: issues.FixResult{
		Pushed:      false,
		CommentBody: "Agent declined to apply changes.",
	}}
	broker := &fakeResponderBroker{}
	r := makeFixRunner(t, st, gh, exec, broker, config.ReviewFixConfig{
		Enabled: true, PerPRLifetime: 3, CooldownSecs: 0,
	})
	if err := r.Run(context.Background(), samplePR(), 99); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !gh.postCalled {
		t.Error("advisory comment not posted on no-changes path")
	}
	if st.state == "FIX_PUSHED" {
		t.Error("state flipped to FIX_PUSHED despite Pushed=false")
	}
	if st.count != 1 {
		t.Errorf("counter = %d, want 1 (no-changes path must still advance counter)", st.count)
	}
	events := broker.eventsByType(sse.EventIssueReviewCompleted)
	if len(events) != 1 {
		t.Fatalf("completed events = %d, want 1", len(events))
	}
	var payload map[string]any
	json.Unmarshal([]byte(events[0].Data), &payload)
	if payload["pushed"] != false {
		t.Errorf("pushed payload field = %v, want false", payload["pushed"])
	}
}

// TestFixRunner_PassesOriginIssueToExecutor pins that the FixRunner
// hydrates and forwards the originating issue to the executor — the
// executor is the one that builds the agent prompt, so without this
// hand-off the agent would lose the issue context #482 specifies.
func TestFixRunner_PassesOriginIssueToExecutor(t *testing.T) {
	issue := &store.Issue{ID: 99, Number: 17, Title: "Refactor", Body: "Move config"}
	st := &fakeFixStore{issue: issue}
	gh := &fakeFixGH{reviews: []github.PRReview{
		crReview("alice", "rename", time.Now()),
	}}
	exec := &fakeFixExec{result: pushedResult()}
	broker := &fakeResponderBroker{}
	r := makeFixRunner(t, st, gh, exec, broker, config.ReviewFixConfig{
		Enabled: true, PerPRLifetime: 3, CooldownSecs: 0,
	})
	if err := r.Run(context.Background(), samplePR(), 99); err != nil {
		t.Fatalf("run: %v", err)
	}
	if exec.req.OriginIssue == nil {
		t.Fatal("OriginIssue not forwarded to executor")
	}
	if exec.req.OriginIssue.Number != 17 {
		t.Errorf("OriginIssue.Number = %d, want 17", exec.req.OriginIssue.Number)
	}
	if exec.req.ReviewerLogin != "alice" {
		t.Errorf("ReviewerLogin = %q", exec.req.ReviewerLogin)
	}
	if exec.req.ReviewBody != "rename" {
		t.Errorf("ReviewBody = %q", exec.req.ReviewBody)
	}
}

// TestFixRunner_EmptyCommentBody_PostsNothing pins that an executor
// that returned a non-error result with an empty CommentBody (a
// pathological case — the production wiring always populates it,
// but defensive) does not produce a spurious empty comment. The
// counter still advances.
func TestFixRunner_EmptyCommentBody_PostsNothing(t *testing.T) {
	st := &fakeFixStore{}
	gh := &fakeFixGH{reviews: []github.PRReview{crReview("alice", "rename", time.Now())}}
	exec := &fakeFixExec{result: issues.FixResult{Pushed: false, CommentBody: "   "}}
	broker := &fakeResponderBroker{}
	r := makeFixRunner(t, st, gh, exec, broker, config.ReviewFixConfig{
		Enabled: true, PerPRLifetime: 3, CooldownSecs: 0,
	})

	if err := r.Run(context.Background(), samplePR(), 99); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gh.postCalled {
		t.Error("PostComment invoked for empty CommentBody")
	}
	if st.count != 1 {
		t.Errorf("counter = %d, want 1", st.count)
	}
}
