package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// These tests lock in the fix for theburrowhub/heimdallm#264: the persistent
// in-flight claim (#258) is keyed on (pr_id, head_sha), but both callers of
// runReview (tier2 ProcessPR and tier3 HandleChange) used to construct the
// gh.PullRequest with an empty Head.SHA, silently bypassing the claim guard
// and letting two poll ticks review the same PR concurrently — reopening the
// #243 double-review pattern. The fix plumbs HeadSHA through scheduler.Tier2PR
// and scheduler.ItemSnapshot. These tests fail if that plumbing regresses.

// TestTier2Adapter_FetchPRsToReview_PopulatesHeadSHA verifies the adapter
// resolves HEAD SHA via /pulls/N for every PR that cleared the review gate,
// so Tier2PR.HeadSHA arrives at ProcessPR non-empty.
//
// The Search Issues API used by FetchPRsToReview does not populate head.sha
// (see github/client.go:379), so the adapter must make one extra /pulls/N
// call per passing PR. This test drives that contract.
func TestTier2Adapter_FetchPRsToReview_PopulatesHeadSHA(t *testing.T) {
	updatedAt := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	const wantSHA = "deadbeef1234567890abcdef"

	pullsHits := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
		case r.URL.Path == "/search/issues":
			result := struct {
				Items []gh.PullRequest `json:"items"`
			}{Items: []gh.PullRequest{{
				ID:     4242,
				Number: 7,
				Title:  "open non-draft PR",
				State:  "open",
				User:   gh.User{Login: "alice"},
				Head: gh.Branch{
					Repo: gh.Repo{FullName: "org/repo"},
					// SHA intentionally empty — Search API does not populate it.
				},
				UpdatedAt: updatedAt,
			}}}
			_ = json.NewEncoder(w).Encode(result)
		case r.URL.Path == "/repos/org/repo/pulls/7":
			atomic.AddInt32(&pullsHits, 1)
			_ = json.NewEncoder(w).Encode(gh.PullRequest{
				ID: 4242, Number: 7, State: "open",
				Head: gh.Branch{SHA: wantSHA},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
	)

	a := &tier2Adapter{
		ghClient:             gh.NewClient("fake-token", gh.WithBaseURL(srv.URL)),
		store:                s,
		broker:               broker,
		cfgMu:                &cfgMu,
		cfg:                  &cfg,
		loginMu:              &loginMu,
		login:                &login,
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}

	out, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("FetchPRsToReview: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(out))
	}
	if out[0].HeadSHA != wantSHA {
		t.Errorf("Tier2PR.HeadSHA = %q, want %q — SHA is load-bearing for the in-flight claim (#264)",
			out[0].HeadSHA, wantSHA)
	}
	if got := atomic.LoadInt32(&pullsHits); got != 1 {
		t.Errorf("/pulls/7 fetched %d time(s), want 1 (one resolve per passing PR)", got)
	}
}

// TestTier2Adapter_FetchPRsToReview_EmptyHeadSHAWhenResolverFails proves the
// fail-open posture: if /pulls/N returns a transient error, the PR still goes
// through to ProcessPR (with empty HeadSHA) rather than being dropped. The
// other layered defenses (fail-closed SHA in pipeline.Run, circuit breaker,
// PublishedAt grace) cap the worst-case cost when this fallback triggers.
func TestTier2Adapter_FetchPRsToReview_EmptyHeadSHAWhenResolverFails(t *testing.T) {
	updatedAt := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
		case r.URL.Path == "/search/issues":
			result := struct {
				Items []gh.PullRequest `json:"items"`
			}{Items: []gh.PullRequest{{
				ID: 4242, Number: 7, Title: "t", State: "open",
				User:      gh.User{Login: "alice"},
				Head:      gh.Branch{Repo: gh.Repo{FullName: "org/repo"}},
				UpdatedAt: updatedAt,
			}}}
			_ = json.NewEncoder(w).Encode(result)
		case r.URL.Path == "/repos/org/repo/pulls/7":
			// Simulate a persistent GitHub outage on the /pulls endpoint.
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
	)

	a := &tier2Adapter{
		ghClient:             gh.NewClient("fake-token", gh.WithBaseURL(srv.URL)),
		store:                s,
		broker:               broker,
		cfgMu:                &cfgMu,
		cfg:                  &cfg,
		loginMu:              &loginMu,
		login:                &login,
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}

	out, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("FetchPRsToReview should not fail when /pulls errors (got %v)", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 PR passed through on resolver error, got %d", len(out))
	}
	if out[0].HeadSHA != "" {
		t.Errorf("Tier2PR.HeadSHA = %q, want empty on resolver error (fail-open)", out[0].HeadSHA)
	}
}

// TestTier2Adapter_ProcessPR_PlumbsHeadSHAIntoGhPR verifies the wiring from
// scheduler.Tier2PR.HeadSHA to gh.PullRequest.Head.SHA inside ProcessPR.
// This is the line that runReview's claim guard depends on — before #264
// the field was zero-valued and the guard silently skipped.
func TestTier2Adapter_ProcessPR_PlumbsHeadSHAIntoGhPR(t *testing.T) {
	const wantSHA = "cafef00d0123456789abcdef0123456789abcdef"

	var (
		mu        sync.Mutex
		sawGhPR   *gh.PullRequest
		callCount int
	)
	runReview := func(pr *gh.PullRequest, aiCfg config.RepoAI) {
		mu.Lock()
		defer mu.Unlock()
		sawGhPR = pr
		callCount++
	}

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
	)

	a := &tier2Adapter{
		store:     s,
		broker:    broker,
		cfgMu:     &cfgMu,
		cfg:       &cfg,
		loginMu:   &loginMu,
		login:     &login,
		runReview: runReview,
	}

	pr := scheduler.Tier2PR{
		ID:     4242,
		Number: 7,
		Repo:   "org/repo",
		Title:  "t",
		Author: "alice",
		State:  "open",
		Draft:  false,
		HeadSHA: wantSHA,
		UpdatedAt: time.Now(),
	}

	if err := a.ProcessPR(context.Background(), pr); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if callCount != 1 {
		t.Fatalf("runReview called %d time(s), want 1", callCount)
	}
	if sawGhPR == nil {
		t.Fatalf("runReview not invoked with a ghPR")
	}
	if sawGhPR.Head.SHA != wantSHA {
		t.Errorf("ghPR.Head.SHA = %q, want %q — plumbing regression reopens #264",
			sawGhPR.Head.SHA, wantSHA)
	}
}

// TestTier2Adapter_HandleChange_PlumbsHeadSHAIntoGhPR covers the tier3 half of
// the same wiring: ItemSnapshot.HeadSHA → ghPR.Head.SHA. GetPRSnapshot already
// fetches head.sha in the same /pulls/N call as the state/draft fields, so
// this is a free copy — losing it silently was purely a wiring bug.
func TestTier2Adapter_HandleChange_PlumbsHeadSHAIntoGhPR(t *testing.T) {
	const wantSHA = "feedface0123456789abcdef0123456789abcdef"

	var (
		mu        sync.Mutex
		sawGhPR   *gh.PullRequest
		callCount int
	)
	runReview := func(pr *gh.PullRequest, aiCfg config.RepoAI) {
		mu.Lock()
		defer mu.Unlock()
		sawGhPR = pr
		callCount++
	}

	s := newMemStore(t)
	// HandleChange calls a.store.GetPRByGithubID; seeding the row lets the
	// defensive nil-check path not fire and makes the test exercise the
	// exact code that feeds ghPR.Head.SHA.
	if _, err := s.UpsertPR(&store.PR{
		GithubID:  4242,
		Repo:      "org/repo",
		Number:    7,
		Title:     "t",
		State:     "open",
		UpdatedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed PR: %v", err)
	}

	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
	)

	a := &tier2Adapter{
		store:     s,
		broker:    broker,
		cfgMu:     &cfgMu,
		cfg:       &cfg,
		loginMu:   &loginMu,
		login:     &login,
		runReview: runReview,
	}

	item := &scheduler.WatchItem{
		Type:     "pr",
		Repo:     "org/repo",
		Number:   7,
		GithubID: 4242,
		LastSeen: time.Now().Add(-time.Minute),
	}
	snap := &scheduler.ItemSnapshot{
		State:     "open",
		Draft:     false,
		Author:    "alice",
		UpdatedAt: time.Now(),
		HeadSHA:   wantSHA,
	}

	if err := a.HandleChange(context.Background(), item, snap); err != nil {
		t.Fatalf("HandleChange: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if callCount != 1 {
		t.Fatalf("runReview called %d time(s), want 1", callCount)
	}
	if sawGhPR == nil {
		t.Fatalf("runReview not invoked with a ghPR")
	}
	if sawGhPR.Head.SHA != wantSHA {
		t.Errorf("ghPR.Head.SHA = %q, want %q — tier3 plumbing regression",
			sawGhPR.Head.SHA, wantSHA)
	}
}

// TestTier2Adapter_ProcessPR_ConcurrentCallsCollapseToOneReview is the
// integration-style regression for #264: two goroutines enter ProcessPR at
// the same time with the same (pr_id, head_sha); only one passes through to
// the review body. This is the end-to-end property the original #258 fix
// intended and that #264 proved was broken in production (PR #263 received
// two back-to-back reviews 60s apart because the claim was silently skipped).
//
// The test runReview below replicates the production claim/release pattern
// from runReview in main.go so the test actually exercises the SQLite-backed
// atomic claim. A fake "review body" increments a counter while holding the
// claim — under the fix it fires exactly once; before the fix (empty SHA)
// it would fire twice.
func TestTier2Adapter_ProcessPR_ConcurrentCallsCollapseToOneReview(t *testing.T) {
	const (
		prGithubID = int64(4242)
		headSHA    = "1337cafe0123456789abcdef0123456789abcdef"
	)

	s := newMemStore(t)
	stored, err := s.UpsertPR(&store.PR{
		GithubID:  prGithubID,
		Repo:      "org/repo",
		Number:    7,
		Title:     "t",
		State:     "open",
		UpdatedAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("seed PR: %v", err)
	}

	var reviewBody int32
	runReview := func(pr *gh.PullRequest, aiCfg config.RepoAI) {
		// Mirror the production claim logic from runReview in main.go. If
		// the caller forgot to set Head.SHA (the #264 bug) OR the PR is
		// not yet upserted, we still return without running — matches the
		// defensive "skip the claim" branch. Any review that gets past
		// ClaimInFlightReview increments reviewBody; the test asserts on
		// that count.
		storedPR, _ := s.GetPRByGithubID(pr.ID)
		if storedPR == nil || pr.Head.SHA == "" {
			return
		}
		ok, err := s.ClaimInFlightReview(storedPR.ID, pr.Head.SHA)
		if err != nil || !ok {
			return
		}
		defer func() { _ = s.ReleaseInFlightReview(storedPR.ID, pr.Head.SHA) }()

		atomic.AddInt32(&reviewBody, 1)
		// Hold the claim long enough that a parallel ProcessPR on the same
		// (pr_id, head_sha) has time to contend for it. 50 ms is the same
		// magnitude the scheduler uses between poll ticks for the contended
		// case; production tick cadence is 60 s so this is extremely generous.
		time.Sleep(50 * time.Millisecond)
	}

	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
	)

	a := &tier2Adapter{
		store:     s,
		broker:    broker,
		cfgMu:     &cfgMu,
		cfg:       &cfg,
		loginMu:   &loginMu,
		login:     &login,
		runReview: runReview,
	}

	pr := scheduler.Tier2PR{
		ID:        prGithubID,
		Number:    7,
		Repo:      "org/repo",
		Title:     "t",
		Author:    "alice",
		State:     "open",
		HeadSHA:   headSHA,
		UpdatedAt: time.Now(),
	}

	// Two goroutines enter ProcessPR concurrently. This is exactly the race
	// that #264 showed unprotected in production: two poll ticks 60 s apart
	// both found the PR in the "to review" list because the first review
	// hadn't stored its result yet.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = a.ProcessPR(context.Background(), pr)
	}()
	go func() {
		defer wg.Done()
		_ = a.ProcessPR(context.Background(), pr)
	}()
	wg.Wait()

	if got := atomic.LoadInt32(&reviewBody); got != 1 {
		t.Errorf("concurrent ProcessPR ran the review body %d time(s), want exactly 1 — #264 regression",
			got)
	}

	// Sanity: the claim row must have been released so a legitimate re-review
	// on a new commit can proceed. ClaimInFlightReview returning true (ok)
	// here proves the row is free.
	ok, err := s.ClaimInFlightReview(stored, headSHA)
	if err != nil {
		t.Fatalf("re-claim after test: %v", err)
	}
	if !ok {
		t.Errorf("claim row not released — deferred Release in runReview is broken")
	}
	// Clean up to keep the in-memory store tidy for subsequent tests.
	_ = s.ReleaseInFlightReview(stored, headSHA)
}
