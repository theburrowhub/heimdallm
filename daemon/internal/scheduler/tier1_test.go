package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/scheduler"
)

// mockPublisher records calls to PublishRepos.
type mockPublisher struct {
	mu    sync.Mutex
	calls [][]string
}

func (m *mockPublisher) PublishRepos(_ context.Context, repos []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(repos))
	copy(cp, repos)
	m.calls = append(m.calls, cp)
	return nil
}

func (m *mockPublisher) getCalls() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// fakeDiscovery returns a fixed list of discovered repos.
type fakeDiscovery struct {
	repos []string
}

func (f *fakeDiscovery) Discovered() []string {
	return f.repos
}

func TestRunTier1_PublishesRepos(t *testing.T) {
	pub := &mockPublisher{}
	disc := &fakeDiscovery{repos: []string{"org/discovered"}}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go scheduler.RunTier1(ctx, scheduler.Tier1Deps{
		Discovery: disc,
		Limiter:   scheduler.NewRateLimiter(100),
		Publisher: pub,
		ConfigFn: func() scheduler.Tier1Config {
			return scheduler.Tier1Config{
				StaticRepos: []string{"org/static"},
			}
		},
		Interval: 50 * time.Millisecond,
	})

	// Wait for at least the initial publish
	time.Sleep(100 * time.Millisecond)
	cancel()

	calls := pub.getCalls()
	if len(calls) == 0 {
		t.Fatal("PublishRepos never called")
	}

	first := calls[0]
	if len(first) != 2 {
		t.Fatalf("expected 2 repos, got %d: %v", len(first), first)
	}
	has := map[string]bool{}
	for _, r := range first {
		has[r] = true
	}
	if !has["org/static"] || !has["org/discovered"] {
		t.Errorf("expected org/static and org/discovered, got %v", first)
	}
}

func TestRunTier1_ExcludesNonMonitored(t *testing.T) {
	pub := &mockPublisher{}
	disc := &fakeDiscovery{repos: []string{"org/discovered"}}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go scheduler.RunTier1(ctx, scheduler.Tier1Deps{
		Discovery: disc,
		Limiter:   scheduler.NewRateLimiter(100),
		Publisher: pub,
		ConfigFn: func() scheduler.Tier1Config {
			return scheduler.Tier1Config{
				StaticRepos:  []string{"org/static", "org/skip"},
				NonMonitored: []string{"org/skip"},
			}
		},
		Interval: 50 * time.Millisecond,
	})

	time.Sleep(100 * time.Millisecond)
	cancel()

	calls := pub.getCalls()
	if len(calls) == 0 {
		t.Fatal("PublishRepos never called")
	}
	for _, repo := range calls[0] {
		if repo == "org/skip" {
			t.Error("non-monitored repo org/skip was included")
		}
	}
}
