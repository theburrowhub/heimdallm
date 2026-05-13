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
)

// ── FixRunner fakes ──────────────────────────────────────────────────────────

type fakeFixStore struct {
	mu              sync.Mutex
	count           int
	lastRespondedAt time.Time
	state           string
	reviewer        string
	stateAt         time.Time
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
	prompt string
	body   string
	err    error
}

func (f *fakeFixExec) GenerateFixResponse(_ context.Context, prompt string) (string, error) {
	f.called = true
	f.prompt = prompt
	return f.body, f.err
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

func TestFixRunner_DisabledByDefault_NoOp(t *testing.T) {
	st := &fakeFixStore{}
	gh := &fakeFixGH{reviews: []github.PRReview{crReview("alice", "rename", time.Now())}}
	exec := &fakeFixExec{body: "would do X"}
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
	exec := &fakeFixExec{body: "would do X"}
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
	exec := &fakeFixExec{body: "would do X"}
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
	exec := &fakeFixExec{body: "x"}
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

// TestFixRunner_HappyPath_MarksFixPushed pins the re-arm behaviour:
// after a successful run, external_review_state flips to FIX_PUSHED
// so the next Tier 3 tick compares against the live reviews list
// rather than re-firing on the same CR.
func TestFixRunner_HappyPath_MarksFixPushed(t *testing.T) {
	st := &fakeFixStore{}
	now := time.Now()
	gh := &fakeFixGH{reviews: []github.PRReview{
		crReview("alice", "rename Foo to Bar", now),
	}}
	exec := &fakeFixExec{body: "Renamed Foo → Bar in pkg/x and pkg/y."}
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
}

// TestFixRunner_AgentReturnsEmpty_PostsNothing pins that an empty
// agent response is treated as "no advisable fix" — the counter
// advances (cap math stays honest) but no comment is posted.
func TestFixRunner_AgentReturnsEmpty_PostsNothing(t *testing.T) {
	st := &fakeFixStore{}
	gh := &fakeFixGH{reviews: []github.PRReview{crReview("alice", "rename", time.Now())}}
	exec := &fakeFixExec{body: ""}
	broker := &fakeResponderBroker{}
	r := makeFixRunner(t, st, gh, exec, broker, config.ReviewFixConfig{
		Enabled: true, PerPRLifetime: 3, CooldownSecs: 0,
	})

	if err := r.Run(context.Background(), samplePR(), 99); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gh.postCalled {
		t.Error("PostComment invoked for empty agent output")
	}
	if st.state == "FIX_PUSHED" {
		t.Error("state flipped to FIX_PUSHED on empty agent output")
	}
	if st.count != 1 {
		t.Errorf("counter = %d, want 1", st.count)
	}
}
