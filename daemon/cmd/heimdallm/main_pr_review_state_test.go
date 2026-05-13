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

// TestRefreshAutoImplementPRReviewState_NewCommentReFiresResponder
// pins the bug found in PR review: the early-return on `state ==
// stored.ExternalReviewState` swallowed a fresh COMMENTED review
// submitted after the daemon already responded to an earlier one.
// A new review (SubmittedAt > stored.ExternalReviewAt) IS a fresh
// trigger and must re-dispatch.
func TestRefreshAutoImplementPRReviewState_NewCommentReFiresResponder(t *testing.T) {
	newerTS := "2026-05-14T09:00:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"COMMENTED","body":"second question","submitted_at":"` + newerTS + `"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 4001, 31)
	// Simulate: a prior COMMENTED already drove the state; the
	// daemon recorded the older timestamp. The stub now returns a
	// strictly-newer COMMENTED.
	older, _ := time.Parse(time.RFC3339, "2026-05-13T09:00:00Z")
	if err := s.UpdatePRReviewState(pr.ID, "COMMENTED", "alice", older); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pr, _ = s.GetPRByGithubID(4001)
	a, _, sub := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	responder := &reviewStateDispatcher{}
	a.responder = responder

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 31, GithubID: 4001}
	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if responder.count() != 1 {
		t.Fatalf("Responder not re-fired on fresh COMMENTED: %d calls", responder.count())
	}
	got, _ := s.GetPRByGithubID(4001)
	wantAt, _ := time.Parse(time.RFC3339, newerTS)
	if !got.ExternalReviewAt.Equal(wantAt) {
		t.Errorf("stored ExternalReviewAt = %v, want %v (timestamp must advance)", got.ExternalReviewAt, wantAt)
	}
	// SSE event must still fire so Flutter knows the row has a fresh
	// trigger (the aggregate state didn't change but the timestamp
	// did).
	select {
	case ev := <-sub:
		if ev.Type != sse.EventPRReviewStateChanged {
			t.Errorf("event type = %q, want pr_review_state_changed", ev.Type)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no SSE event on fresh COMMENTED")
	}
}

// TestRefreshAutoImplementPRReviewState_NewCRReFiresFix_NoAdvisoryPriorPush
// is the symmetric pin for the CHANGES_REQUESTED case after a no-
// changes advisory (NOT after a successful push, which is covered by
// the FIX_PUSHED guard): a reviewer who submits a second CR with
// more context after the daemon's advisory must re-trigger the fix
// runner.
func TestRefreshAutoImplementPRReviewState_NewCRReFiresFix_NoAdvisoryPriorPush(t *testing.T) {
	newerTS := "2026-05-14T09:00:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":2,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"please rename","submitted_at":"` + newerTS + `"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 4002, 32)
	// Stored state is CHANGES_REQUESTED with an OLDER timestamp —
	// represents the path where the FixRunner posted an advisory
	// (no push) and the reviewer follows up with more context.
	older, _ := time.Parse(time.RFC3339, "2026-05-13T09:00:00Z")
	if err := s.UpdatePRReviewState(pr.ID, "CHANGES_REQUESTED", "alice", older); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pr, _ = s.GetPRByGithubID(4002)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	fixer := &reviewStateDispatcher{}
	a.fixRunner = fixer

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 32, GithubID: 4002}
	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if fixer.count() != 1 {
		t.Fatalf("FixRunner not re-fired on fresh CHANGES_REQUESTED: %d calls", fixer.count())
	}
}

// TestRefreshAutoImplementPRReviewState_FixPushedSuppressesStaleCR
// pins the re-arm fix for the bug review caught: after the FixRunner
// flips the stored state to FIX_PUSHED, the next Tier 3 tick must NOT
// flip back to CHANGES_REQUESTED on the same historical CR. Only a
// fresh CR (SubmittedAt > stored.ExternalReviewAt) reactivates the
// cycle.
func TestRefreshAutoImplementPRReviewState_FixPushedSuppressesStaleCR(t *testing.T) {
	crTS := "2026-05-13T09:00:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"rename","submitted_at":"` + crTS + `"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 3001, 21)
	// Simulate: the FixRunner already addressed this exact CR. Stored
	// state is FIX_PUSHED with external_review_at set to the CR's own
	// SubmittedAt.
	at, _ := time.Parse(time.RFC3339, crTS)
	if err := s.UpdatePRReviewState(pr.ID, "FIX_PUSHED", "alice", at); err != nil {
		t.Fatalf("seed FIX_PUSHED: %v", err)
	}
	pr, _ = s.GetPRByGithubID(3001)
	a, _, sub := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	responder := &reviewStateDispatcher{}
	fixer := &reviewStateDispatcher{}
	a.responder = responder
	a.fixRunner = fixer

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 21, GithubID: 3001}
	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	got, _ := s.GetPRByGithubID(3001)
	if got.ExternalReviewState != "FIX_PUSHED" {
		t.Errorf("state flipped back to %q from FIX_PUSHED", got.ExternalReviewState)
	}
	if fixer.count() != 0 {
		t.Errorf("FixRunner re-fired on the same historical CR: %d calls", fixer.count())
	}
	select {
	case ev := <-sub:
		t.Fatalf("unexpected SSE event %q on FIX_PUSHED re-arm tick", ev.Type)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRefreshAutoImplementPRReviewState_FixPushed_NewCRReactivates
// is the symmetric pin: a CR submitted AFTER the FixRunner's response
// (SubmittedAt > stored.ExternalReviewAt) does flip the state back to
// CHANGES_REQUESTED and re-dispatches.
func TestRefreshAutoImplementPRReviewState_FixPushed_NewCRReactivates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":2,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"second pass","submitted_at":"2026-05-13T12:00:00Z"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 3002, 22)
	earlier, _ := time.Parse(time.RFC3339, "2026-05-13T09:00:00Z")
	if err := s.UpdatePRReviewState(pr.ID, "FIX_PUSHED", "alice", earlier); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pr, _ = s.GetPRByGithubID(3002)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	fixer := &reviewStateDispatcher{}
	a.fixRunner = fixer

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 22, GithubID: 3002}
	if err := a.refreshAutoImplementPRReviewState(item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if fixer.count() != 1 {
		t.Fatalf("FixRunner not re-fired on fresh CR: %d calls", fixer.count())
	}
}

// TestCheckItem_AutoImplementPRBranch_ReturnsChangedSoLastSeenAdvances
// pins the contract with the StateWorker (#482, second-pass review):
// after the auto-implement branch refreshes the review state, it
// must signal `changed=true` so the watch KV resets the backoff and
// advances LastSeen. Returning false would re-poll on every tick
// (same updated_at keeps looking new), burn API calls, and grow the
// backoff despite the daemon doing real work. A nil snap is
// returned alongside so HandleChange's first guard short-circuits
// — we already handled the dispatch inline.
func TestCheckItem_AutoImplementPRBranch_ReturnsChangedSoLastSeenAdvances(t *testing.T) {
	// Stub the Pulls + reviews endpoints. updated_at strictly newer
	// than item.LastSeen so the standard "no change" gate falls
	// through into the auto-implement branch.
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/pulls/41", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"open","draft":false,
			"user":{"login":"heimdallm-bot"},
			"updated_at":"2026-05-14T10:00:00Z",
			"head":{"sha":"deadbeef","ref":"heimdallm/issue-99"}
		}`))
	})
	mux.HandleFunc("/repos/org/repo/pulls/41/reviews", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newMemStore(t)
	seedAutoImplementPR(t, s, 5001, 41)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	item := &scheduler.WatchItem{
		Type: "pr", Repo: "org/repo", Number: 41, GithubID: 5001,
		LastSeen: time.Time{}, // zero → snap.UpdatedAt advances
	}

	changed, snap, err := a.CheckItem(context.Background(), item)
	if err != nil {
		t.Fatalf("CheckItem: %v", err)
	}
	if !changed {
		t.Fatal("CheckItem returned changed=false on auto-implement refresh — LastSeen will not advance, backoff will grow")
	}
	if snap != nil {
		t.Errorf("snap must be nil so HandleChange short-circuits, got %+v", snap)
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
