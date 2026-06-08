package discovery_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/discovery"
)

type fakeFetcher struct {
	mu     sync.Mutex
	calls  int32
	repos  []string
	err    error
	onCall func(call int32)
}

func (f *fakeFetcher) FetchReposByTopic(topic string, orgs []string) ([]string, error) {
	n := atomic.AddInt32(&f.calls, 1)
	if f.onCall != nil {
		f.onCall(n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Return copies so callers can mutate safely.
	out := make([]string, len(f.repos))
	copy(out, f.repos)
	return out, f.err
}

func (f *fakeFetcher) set(repos []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos = repos
	f.err = err
}

// ── MergeRepos ───────────────────────────────────────────────────────────────

func TestMergeRepos_PreservesStaticOrderAndDeduplicates(t *testing.T) {
	out := discovery.MergeRepos(
		[]string{"org/static1", "org/shared"},
		[]string{"org/shared", "org/discovered1", "org/discovered2"},
		nil,
	)
	want := []string{"org/static1", "org/shared", "org/discovered1", "org/discovered2"}
	if len(out) != len(want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("out[%d] = %q, want %q", i, out[i], want[i])
		}
	}
}

func TestMergeRepos_BothEmpty(t *testing.T) {
	if got := discovery.MergeRepos(nil, nil, nil); got != nil {
		t.Errorf("MergeRepos(nil, nil, nil) = %v, want nil", got)
	}
}

func TestMergeRepos_OnlyStatic(t *testing.T) {
	got := discovery.MergeRepos([]string{"a", "b"}, nil, nil)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("got %v", got)
	}
}

func TestMergeRepos_OnlyDiscovered(t *testing.T) {
	got := discovery.MergeRepos(nil, []string{"a", "b"}, nil)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("got %v", got)
	}
}

func TestMergeRepos_IgnoresEmptyStrings(t *testing.T) {
	got := discovery.MergeRepos([]string{"", "a", ""}, []string{"", "b"}, nil)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("got %v", got)
	}
}

func TestMergeRepos_NonMonitored(t *testing.T) {
	got := discovery.MergeRepos(
		[]string{"org/static1", "org/blocked"},
		[]string{"org/discovered1", "org/blocked2"},
		[]string{"org/blocked", "org/blocked2"},
	)
	want := []string{"org/static1", "org/discovered1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ── FilterArchived ──────────────────────────────────────────────────────────

type fakeArchivedChecker struct {
	archived map[string]bool
	errRepos map[string]error
}

func (f *fakeArchivedChecker) IsRepoArchived(repo string) (bool, error) {
	if err, ok := f.errRepos[repo]; ok {
		return false, err
	}
	return f.archived[repo], nil
}

func TestFilterArchived_RemovesArchivedRepos(t *testing.T) {
	checker := &fakeArchivedChecker{
		archived: map[string]bool{
			"org/archived1": true,
			"org/archived2": true,
		},
	}
	repos := []string{"org/active", "org/archived1", "org/other", "org/archived2"}
	active, archived := discovery.FilterArchived(repos, checker, nil)
	if len(active) != 2 || active[0] != "org/active" || active[1] != "org/other" {
		t.Errorf("active = %v, want [org/active org/other]", active)
	}
	if len(archived) != 2 || archived[0] != "org/archived1" || archived[1] != "org/archived2" {
		t.Errorf("archived = %v, want [org/archived1 org/archived2]", archived)
	}
}

func TestFilterArchived_SkipsCheckForDiscoveredRepos(t *testing.T) {
	checker := &fakeArchivedChecker{
		archived: map[string]bool{"org/fresh": true},
	}
	repos := []string{"org/fresh", "org/static"}
	skipCheck := []string{"org/fresh"}
	active, archived := discovery.FilterArchived(repos, checker, skipCheck)
	if len(active) != 2 {
		t.Errorf("skipped repo should remain active, got active=%v archived=%v", active, archived)
	}
	if len(archived) != 0 {
		t.Errorf("no repos should be archived when skipped, got %v", archived)
	}
}

func TestFilterArchived_KeepsRepoOnError(t *testing.T) {
	checker := &fakeArchivedChecker{
		errRepos: map[string]error{"org/flaky": errors.New("rate limit")},
	}
	repos := []string{"org/flaky", "org/ok"}
	active, archived := discovery.FilterArchived(repos, checker, nil)
	if len(active) != 2 {
		t.Errorf("error repos should be kept, got active=%v", active)
	}
	if len(archived) != 0 {
		t.Errorf("no repos should be archived on error, got %v", archived)
	}
}

func TestFilterArchived_EmptyInput(t *testing.T) {
	checker := &fakeArchivedChecker{}
	active, archived := discovery.FilterArchived(nil, checker, nil)
	if active != nil {
		t.Errorf("active should be nil for empty input, got %v", active)
	}
	if archived != nil {
		t.Errorf("archived should be nil for empty input, got %v", archived)
	}
}

// ── InferOrgs ───────────────────────────────────────────────────────────────

func TestInferOrgs(t *testing.T) {
	got := discovery.InferOrgs([]string{
		"freepik-company/repo-a",
		"theburrowhub/repo-b",
		"freepik-company/repo-c",
	})
	want := []string{"freepik-company", "theburrowhub"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ── Service.Refresh ──────────────────────────────────────────────────────────

func TestService_Refresh_Success(t *testing.T) {
	f := &fakeFetcher{repos: []string{"org/a", "org/b"}}
	s := discovery.NewService(f)

	if err := s.Refresh("topic", []string{"org"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got := s.Discovered()
	if len(got) != 2 || got[0] != "org/a" || got[1] != "org/b" {
		t.Errorf("got %v", got)
	}
	if s.LastError() != nil {
		t.Errorf("LastError = %v, want nil", s.LastError())
	}
}

func TestService_Refresh_TotalFailurePreservesCache(t *testing.T) {
	f := &fakeFetcher{repos: []string{"org/a"}}
	s := discovery.NewService(f)
	if err := s.Refresh("t", []string{"o"}); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}

	// Simulate a total failure: API returns no repos and an error.
	f.set(nil, errors.New("rate limit"))
	if err := s.Refresh("t", []string{"o"}); err == nil {
		t.Fatal("expected error on total failure")
	}

	got := s.Discovered()
	if len(got) != 1 || got[0] != "org/a" {
		t.Errorf("cache should be preserved on total failure, got %v", got)
	}
	if s.LastError() == nil {
		t.Error("LastError should reflect the failure")
	}
}

func TestService_Refresh_PartialFailureUpdatesWithWhatWeGot(t *testing.T) {
	f := &fakeFetcher{repos: []string{"org/a"}}
	s := discovery.NewService(f)
	if err := s.Refresh("t", []string{"o"}); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}

	// Partial failure: API returns a different result + an error.
	f.set([]string{"org/a", "org/b"}, errors.New("one org failed"))
	err := s.Refresh("t", []string{"o"})
	if err == nil {
		t.Fatal("expected error on partial failure")
	}

	got := s.Discovered()
	if len(got) != 2 || got[0] != "org/a" || got[1] != "org/b" {
		t.Errorf("cache should reflect partial result, got %v", got)
	}
}

func TestService_Discovered_ReturnsCopy(t *testing.T) {
	f := &fakeFetcher{repos: []string{"org/a"}}
	s := discovery.NewService(f)
	if err := s.Refresh("t", []string{"o"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	got := s.Discovered()
	got[0] = "mutated"

	got2 := s.Discovered()
	if got2[0] != "org/a" {
		t.Errorf("callers must not be able to mutate the cache through Discovered(); got %v", got2)
	}
}

// ── Service.Run ──────────────────────────────────────────────────────────────

func TestService_Run_RefreshesOnTick(t *testing.T) {
	called := make(chan struct{}, 4)
	f := &fakeFetcher{
		repos: []string{"org/a"},
		onCall: func(n int32) {
			select {
			case called <- struct{}{}:
			default:
			}
		},
	}
	s := discovery.NewService(f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.Run(ctx, 20*time.Millisecond, "topic", []string{"org"})

	// First call should be immediate.
	select {
	case <-called:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for initial refresh")
	}
	// Second call must come from the ticker.
	select {
	case <-called:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for ticker refresh")
	}
}

func TestService_Run_StopsOnContextCancel(t *testing.T) {
	f := &fakeFetcher{repos: []string{"org/a"}}
	s := discovery.NewService(f)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		s.Run(ctx, 10*time.Millisecond, "t", []string{"o"})
		close(done)
	}()

	// Let it tick a few times then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not return within 1s of cancel")
	}
}

func TestService_Run_ZeroIntervalReturns(t *testing.T) {
	f := &fakeFetcher{repos: []string{"org/a"}}
	s := discovery.NewService(f)

	done := make(chan struct{})
	go func() {
		s.Run(context.Background(), 0, "t", []string{"o"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run should return immediately when interval is zero")
	}
	if atomic.LoadInt32(&f.calls) != 0 {
		t.Errorf("expected no API calls on zero interval, got %d", f.calls)
	}
}
