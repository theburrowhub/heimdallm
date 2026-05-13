package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// reviewStateDispatcher captures every (pr, issueID) pair passed to
// Run so the routing tests can assert exactly which dispatcher fired
// for a given external review state.
type reviewStateDispatcher struct {
	mu    sync.Mutex
	calls []reviewStateDispatchCall
}

type reviewStateDispatchCall struct {
	PRID    int64
	IssueID int64
}

func (d *reviewStateDispatcher) Run(_ context.Context, pr *store.PR, issueID int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, reviewStateDispatchCall{PRID: pr.ID, IssueID: issueID})
	return nil
}

func (d *reviewStateDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

// makePRReviewStateAdapter wires the minimum tier2Adapter fields
// refreshAutoImplementPRReviewState reads. The /reviews endpoint is
// stubbed via httptest so the tests stay hermetic — no real network.
func makePRReviewStateAdapter(t *testing.T, srv *httptest.Server, s *store.Store, botLogin string) (*tier2Adapter, *sse.Broker, chan sse.Event) {
	t.Helper()
	broker := sse.NewBroker()
	broker.Start()
	sub := broker.Subscribe()
	if sub == nil {
		t.Fatal("broker.Subscribe returned nil")
	}
	t.Cleanup(broker.Stop)

	var loginMu sync.Mutex
	login := botLogin
	a := &tier2Adapter{
		ghClient: gh.NewClient("fake", gh.WithBaseURL(srv.URL)),
		store:    s,
		broker:   broker,
		loginMu:  &loginMu,
		login:    &login,
	}
	return a, broker, sub
}

// seedAutoImplementPR inserts a PR row with AutoImplementIssueID set
// (and a blank external_review_state) so the helper recognises it as
// eligible for review-state vigilance.
func seedAutoImplementPR(t *testing.T, s *store.Store, githubID int64, number int) *store.PR {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	id, err := s.UpsertPR(&store.PR{
		GithubID: githubID, Repo: "org/repo", Number: number,
		Title: "PR", Author: "heimdallm-bot", URL: "u",
		State: "open", UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	if err := s.MarkPRAutoImplementOrigin(id, 4242); err != nil {
		t.Fatalf("mark origin: %v", err)
	}
	pr, err := s.GetPRByGithubID(githubID)
	if err != nil {
		t.Fatalf("get pr: %v", err)
	}
	return pr
}

// TestRefreshAutoImplementPRReviewState_DetectsStateChange pins the
// happy path of #482 phase 1: a stub /reviews response with a single
// CHANGES_REQUESTED review flips the stored ExternalReviewState and
// publishes EventPRReviewStateChanged with the new state, the
// reviewer, and the prior empty state.
func TestRefreshAutoImplementPRReviewState_DetectsStateChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"rename","submitted_at":"2026-05-13T11:00:00Z"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 1234, 7)
	a, _, sub := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 7, GithubID: 1234}

	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	got, err := s.GetPRByGithubID(1234)
	if err != nil {
		t.Fatalf("get pr: %v", err)
	}
	if got.ExternalReviewState != "CHANGES_REQUESTED" {
		t.Errorf("ExternalReviewState = %q, want CHANGES_REQUESTED", got.ExternalReviewState)
	}
	if got.ExternalReviewer != "alice" {
		t.Errorf("ExternalReviewer = %q, want alice", got.ExternalReviewer)
	}

	select {
	case ev := <-sub:
		if ev.Type != sse.EventPRReviewStateChanged {
			t.Fatalf("event type = %q, want %q", ev.Type, sse.EventPRReviewStateChanged)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if payload["state"] != "CHANGES_REQUESTED" {
			t.Errorf("payload state = %v, want CHANGES_REQUESTED", payload["state"])
		}
		if payload["reviewer"] != "alice" {
			t.Errorf("payload reviewer = %v, want alice", payload["reviewer"])
		}
		if payload["prev_state"] != "" {
			t.Errorf("payload prev_state = %v, want empty", payload["prev_state"])
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no SSE event received within 500ms")
	}
}

// TestRefreshAutoImplementPRReviewState_NoChange_NoSideEffects pins
// that a refresh which returns the same aggregate state as the stored
// one does NOT publish an SSE event and does NOT touch the store row
// (so Flutter doesn't see needless "state changed" cards).
func TestRefreshAutoImplementPRReviewState_NoChange_NoSideEffects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"APPROVED","body":"lgtm","submitted_at":"2026-05-13T11:00:00Z"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 1235, 8)
	// Pre-seed the store with the same state the stub returns.
	at, _ := time.Parse(time.RFC3339, "2026-05-13T11:00:00Z")
	if err := s.UpdatePRReviewState(pr.ID, "APPROVED", "alice", at); err != nil {
		t.Fatalf("seed prior state: %v", err)
	}
	pr, _ = s.GetPRByGithubID(1235)
	a, _, sub := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 8, GithubID: 1235}

	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	select {
	case ev := <-sub:
		t.Fatalf("unexpected SSE event %q on no-op refresh", ev.Type)
	case <-time.After(100 * time.Millisecond):
		// no event — correct
	}
}

// TestRefreshAutoImplementPRReviewState_RoutesCommentedToResponder
// pins phase-2 dispatch: when the aggregate state goes to COMMENTED,
// the responder dispatcher is invoked exactly once with the PR row
// and the originating issue id. The fix runner is NOT invoked.
func TestRefreshAutoImplementPRReviewState_RoutesCommentedToResponder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"COMMENTED","body":"q","submitted_at":"2026-05-13T11:00:00Z"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 2001, 11)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	responder := &reviewStateDispatcher{}
	fixer := &reviewStateDispatcher{}
	a.responder = responder
	a.fixRunner = fixer

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 11, GithubID: 2001}
	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if responder.count() != 1 {
		t.Errorf("responder calls = %d, want 1", responder.count())
	}
	if fixer.count() != 0 {
		t.Errorf("fixer calls = %d, want 0", fixer.count())
	}
	if responder.calls[0].IssueID != 4242 {
		t.Errorf("origin issue id = %d, want 4242", responder.calls[0].IssueID)
	}
}

// TestRefreshAutoImplementPRReviewState_RoutesChangesRequestedToFix
// is the symmetric pin for phase-3 dispatch.
func TestRefreshAutoImplementPRReviewState_RoutesChangesRequestedToFix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"rename","submitted_at":"2026-05-13T11:00:00Z"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 2002, 12)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	responder := &reviewStateDispatcher{}
	fixer := &reviewStateDispatcher{}
	a.responder = responder
	a.fixRunner = fixer

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 12, GithubID: 2002}
	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if fixer.count() != 1 {
		t.Errorf("fixer calls = %d, want 1", fixer.count())
	}
	if responder.count() != 0 {
		t.Errorf("responder calls = %d, want 0", responder.count())
	}
}

// TestRefreshAutoImplementPRReviewState_ApprovedRoutesNowhere pins
// that APPROVED is a terminal observation event only — no
// dispatcher is invoked.
func TestRefreshAutoImplementPRReviewState_ApprovedRoutesNowhere(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"APPROVED","body":"lgtm","submitted_at":"2026-05-13T11:00:00Z"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 2003, 13)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	responder := &reviewStateDispatcher{}
	fixer := &reviewStateDispatcher{}
	a.responder = responder
	a.fixRunner = fixer

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 13, GithubID: 2003}
	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if responder.count() != 0 || fixer.count() != 0 {
		t.Errorf("APPROVED dispatched somewhere: responder=%d fixer=%d", responder.count(), fixer.count())
	}
}

// TestRefreshAutoImplementPRReviewState_FiltersBotReviews pins the
// self-loop guard: a review submitted by the bot's own login must be
// filtered out by LatestExternalReviewState, so an all-bot reviews
// list yields no state change and no event.
func TestRefreshAutoImplementPRReviewState_FiltersBotReviews(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"heimdallm-bot"},"state":"APPROVED","body":"self-approve","submitted_at":"2026-05-13T11:00:00Z"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 1236, 9)
	a, _, sub := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 9, GithubID: 1236}

	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got, _ := s.GetPRByGithubID(1236)
	if got.ExternalReviewState != "" {
		t.Errorf("ExternalReviewState = %q, want empty (bot review must be filtered)", got.ExternalReviewState)
	}
	select {
	case ev := <-sub:
		t.Fatalf("unexpected SSE event %q for bot-only reviews", ev.Type)
	case <-time.After(100 * time.Millisecond):
	}
}
