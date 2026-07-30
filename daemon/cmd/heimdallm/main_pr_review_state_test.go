package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
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
	err   error // when non-nil, every Run returns this error
	// lastCtx captures the context the dispatcher was invoked with
	// so tests can assert the CheckItem ctx propagated all the way
	// through refreshAutoImplementPRReviewState rather than being
	// silently replaced by context.Background().
	lastCtx context.Context
}

type reviewStateDispatchCall struct {
	PRID    int64
	IssueID int64
}

func (d *reviewStateDispatcher) Run(ctx context.Context, pr *store.PR, issueID int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastCtx = ctx
	d.calls = append(d.calls, reviewStateDispatchCall{PRID: pr.ID, IssueID: issueID})
	return d.err
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

	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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

	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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
// backoff despite the daemon doing real work. The returned snapshot
// is marked Handled so HandleChange short-circuits while the worker
// can persist the exact remote updated_at instead of time.Now.
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
	wantObservedAt := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	if snap == nil || !snap.Handled || !snap.UpdatedAt.Equal(wantObservedAt) {
		t.Errorf("snap = %+v, want handled snapshot at %s", snap, wantObservedAt)
	}
}

func TestCheckItem_AutoImplementPRBranch_DisabledAfterSnapshotSkipsReaction(t *testing.T) {
	var (
		cfgMu        sync.Mutex
		cfg          = &config.Config{GitHub: config.GitHubConfig{Repositories: []string{"org/repo"}}}
		reviewsCalls int
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/pulls/42", func(w http.ResponseWriter, _ *http.Request) {
		// Simulate an operator disabling the repo while the already-queued
		// state check is fetching its fresh PR snapshot.
		cfgMu.Lock()
		cfg.GitHub.NonMonitored = []string{"org/repo"}
		cfgMu.Unlock()
		_, _ = w.Write([]byte(`{
			"state":"open","draft":false,
			"user":{"login":"heimdallm-bot"},
			"updated_at":"2026-05-14T10:00:00Z",
			"head":{"sha":"deadbeef","ref":"heimdallm/issue-99"}
		}`))
	})
	mux.HandleFunc("/repos/org/repo/pulls/42/reviews", func(w http.ResponseWriter, _ *http.Request) {
		reviewsCalls++
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"x","submitted_at":"2026-05-14T09:00:00Z"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newMemStore(t)
	seedAutoImplementPR(t, s, 5002, 42)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	a.cfgMu = &cfgMu
	a.cfg = &cfg
	fixer := &reviewStateDispatcher{}
	a.fixRunner = fixer

	_, _, err := a.CheckItem(context.Background(), &scheduler.WatchItem{
		Type: "pr", Repo: "org/repo", Number: 42, GithubID: 5002,
	})
	if err != nil {
		t.Fatalf("CheckItem: %v", err)
	}
	if reviewsCalls != 0 {
		t.Fatalf("reviews endpoint called %d time(s) after repo disable, want 0", reviewsCalls)
	}
	if fixer.count() != 0 {
		t.Fatalf("FixRunner called %d time(s) after repo disable, want 0", fixer.count())
	}
}

// TestCheckItem_AutoImplementPRBranch_PropagatesCtxToDispatchers
// pins the cancellation contract reviewers flagged: the runner runs
// can be long-lived (FixRunner does checkout + agent + push), so the
// CheckItem ctx must reach them. Using context.Background() would
// leak in-flight goroutines on daemon shutdown.
func TestCheckItem_AutoImplementPRBranch_PropagatesCtxToDispatchers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/pulls/64", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"open","draft":false,
			"user":{"login":"heimdallm-bot"},
			"updated_at":"2026-05-14T10:00:00Z",
			"head":{"sha":"deadbeef","ref":"heimdallm/issue-80"}
		}`))
	})
	mux.HandleFunc("/repos/org/repo/pulls/64/reviews", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"x","submitted_at":"2026-05-14T09:00:00Z"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newMemStore(t)
	seedAutoImplementPR(t, s, 7004, 64)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	fixer := &reviewStateDispatcher{}
	a.fixRunner = fixer
	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 64, GithubID: 7004}

	// Marker value flows through CheckItem -> refresh -> dispatcher.
	type ctxKey string
	ctx := context.WithValue(context.Background(), ctxKey("marker"), "from-caller")
	if _, _, err := a.CheckItem(ctx, item); err != nil {
		t.Fatalf("CheckItem: %v", err)
	}
	if fixer.lastCtx == nil {
		t.Fatal("dispatcher saw no ctx")
	}
	if got, _ := fixer.lastCtx.Value(ctxKey("marker")).(string); got != "from-caller" {
		t.Errorf("dispatcher ctx did not carry the caller's value (got %q) — context.Background() was used somewhere on the path", got)
	}
}

// TestCheckItem_AutoImplementPRBranch_PropagatesRunnerError pins the
// end-to-end retry contract for #482 phase 2/3: when the runner
// fails, the error must propagate through CheckItem so the
// state-handler returns an error to the StateWorker, which then
// calls IncreaseBackoff (NOT ResetBackoff). LastSeen must NOT
// advance — otherwise the next tick's early gate
// `!snap.UpdatedAt.After(item.LastSeen)` would silently drop the
// review and the runner never retries until GitHub bumps the PR's
// updated_at again.
func TestCheckItem_AutoImplementPRBranch_PropagatesRunnerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/pulls/61", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"open","draft":false,
			"user":{"login":"heimdallm-bot"},
			"updated_at":"2026-05-14T10:00:00Z",
			"head":{"sha":"deadbeef","ref":"heimdallm/issue-77"}
		}`))
	})
	mux.HandleFunc("/repos/org/repo/pulls/61/reviews", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"rename","submitted_at":"2026-05-14T09:00:00Z"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newMemStore(t)
	seedAutoImplementPR(t, s, 7001, 61)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	a.fixRunner = &reviewStateDispatcher{err: errors.New("transient executor failure")}
	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 61, GithubID: 7001}

	changed, _, err := a.CheckItem(context.Background(), item)
	if err == nil {
		t.Fatal("CheckItem returned nil error after FixRunner failure — StateWorker will ResetBackoff and advance LastSeen, dropping the retry")
	}
	if !strings.Contains(err.Error(), "transient executor failure") {
		t.Errorf("err = %v, want it to wrap the runner's error", err)
	}
	if changed {
		t.Error("changed = true on runner failure — should be false so the worker IncreaseBackoff path runs (no LastSeen advance)")
	}
}

// TestCheckItem_AutoImplementPRBranch_PropagatesResponderError is
// the symmetric pin for the COMMENTED → Responder path.
func TestCheckItem_AutoImplementPRBranch_PropagatesResponderError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/pulls/62", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"open","draft":false,
			"user":{"login":"heimdallm-bot"},
			"updated_at":"2026-05-14T10:00:00Z",
			"head":{"sha":"deadbeef","ref":"heimdallm/issue-78"}
		}`))
	})
	mux.HandleFunc("/repos/org/repo/pulls/62/reviews", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"COMMENTED","body":"q","submitted_at":"2026-05-14T09:00:00Z"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newMemStore(t)
	seedAutoImplementPR(t, s, 7002, 62)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	a.responder = &reviewStateDispatcher{err: errors.New("post comment 5xx")}
	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 62, GithubID: 7002}

	changed, _, err := a.CheckItem(context.Background(), item)
	if err == nil {
		t.Fatal("CheckItem returned nil after Responder failure — retry path is broken")
	}
	if changed {
		t.Error("changed = true on runner failure")
	}
}

// TestCheckItem_AutoImplementPRBranch_PersistsStateBeforeRunnerError
// pins the side-effect ordering: even when the runner fails, the
// state observation was real — so the new aggregate must be
// persisted and the SSE must fire before the error propagates.
// Subsequent retry ticks then see stateMoved=false and only
// re-dispatch (no SSE flapping).
func TestCheckItem_AutoImplementPRBranch_PersistsStateBeforeRunnerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/pulls/63", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"open","draft":false,
			"user":{"login":"heimdallm-bot"},
			"updated_at":"2026-05-14T10:00:00Z",
			"head":{"sha":"deadbeef","ref":"heimdallm/issue-79"}
		}`))
	})
	mux.HandleFunc("/repos/org/repo/pulls/63/reviews", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"x","submitted_at":"2026-05-14T09:00:00Z"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newMemStore(t)
	seedAutoImplementPR(t, s, 7003, 63)
	a, _, sub := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	a.fixRunner = &reviewStateDispatcher{err: errors.New("boom")}
	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 63, GithubID: 7003}

	_, _, _ = a.CheckItem(context.Background(), item)

	got, _ := s.GetPRByGithubID(7003)
	if got.ExternalReviewState != "CHANGES_REQUESTED" {
		t.Errorf("ExternalReviewState = %q, want CHANGES_REQUESTED — observation must persist even when runner fails", got.ExternalReviewState)
	}
	select {
	case ev := <-sub:
		if ev.Type != sse.EventPRReviewStateChanged {
			t.Errorf("first event = %q, want pr_review_state_changed", ev.Type)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no SSE event despite real state transition")
	}
}

// TestRefreshAutoImplementPRReviewState_RetriesDispatchOnUnchangedCR
// pins the bug found in PR review: when a previous tick persisted
// the CHANGES_REQUESTED state but the FixRunner failed (executor
// error, transient push failure, PostComment 5xx), the dispatch
// must still fire on the next tick. Coupling persist + dispatch
// behind the same "state moved" gate silently swallows the retry —
// the reviewer's CR sits unaddressed until they post a fresh
// review.
func TestRefreshAutoImplementPRReviewState_RetriesDispatchOnUnchangedCR(t *testing.T) {
	crTS := "2026-05-13T09:00:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"please rename","submitted_at":"` + crTS + `"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 6001, 51)
	// Pre-seed: state is CR with exactly the timestamp the stub
	// returns. Simulates "previous tick persisted state, dispatch
	// failed, state has not moved".
	at, _ := time.Parse(time.RFC3339, crTS)
	if err := s.UpdatePRReviewState(pr.ID, "CHANGES_REQUESTED", "alice", at); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pr, _ = s.GetPRByGithubID(6001)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	fixer := &reviewStateDispatcher{}
	a.fixRunner = fixer

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 51, GithubID: 6001}
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if fixer.count() != 1 {
		t.Fatalf("FixRunner did not retry on unchanged CR state: %d calls (a failed previous tick would now leave the CR forever unaddressed)", fixer.count())
	}
}

// TestRefreshAutoImplementPRReviewState_RetriesDispatchOnUnchangedCommented
// is the symmetric pin for the Responder.
func TestRefreshAutoImplementPRReviewState_RetriesDispatchOnUnchangedCommented(t *testing.T) {
	commTS := "2026-05-13T09:00:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"COMMENTED","body":"q","submitted_at":"` + commTS + `"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 6002, 52)
	at, _ := time.Parse(time.RFC3339, commTS)
	if err := s.UpdatePRReviewState(pr.ID, "COMMENTED", "alice", at); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pr, _ = s.GetPRByGithubID(6002)
	a, _, _ := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	responder := &reviewStateDispatcher{}
	a.responder = responder

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 52, GithubID: 6002}
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if responder.count() != 1 {
		t.Fatalf("Responder did not retry on unchanged COMMENTED state: %d calls", responder.count())
	}
}

// TestRefreshAutoImplementPRReviewState_UnchangedCR_NoExtraSSE pins
// that the retry-dispatch behaviour above does NOT come with an
// extra noisy SSE event on every tick. Persist + emit stay gated on
// real state movement.
func TestRefreshAutoImplementPRReviewState_UnchangedCR_NoExtraSSE(t *testing.T) {
	crTS := "2026-05-13T09:00:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"x","submitted_at":"` + crTS + `"}
		]`))
	}))
	defer srv.Close()

	s := newMemStore(t)
	pr := seedAutoImplementPR(t, s, 6003, 53)
	at, _ := time.Parse(time.RFC3339, crTS)
	if err := s.UpdatePRReviewState(pr.ID, "CHANGES_REQUESTED", "alice", at); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pr, _ = s.GetPRByGithubID(6003)
	a, _, sub := makePRReviewStateAdapter(t, srv, s, "heimdallm-bot")
	a.fixRunner = &reviewStateDispatcher{}

	item := &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 53, GithubID: 6003}
	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	select {
	case ev := <-sub:
		t.Fatalf("unexpected SSE event %q on unchanged-state retry tick", ev.Type)
	case <-time.After(100 * time.Millisecond):
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

	if err := a.refreshAutoImplementPRReviewState(context.Background(), item, pr); err != nil {
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
