package main

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
)

// TestProcessDiscoveredRepos_PublishesSSEEvent asserts that the discovery
// path emits one EventRepoDiscovered per newly-added repo, with the repo
// name in the event payload, and emits nothing for already-known repos.
//
// Covers the FetchPRsToReview -> upsertDiscoveredRepos -> processDiscoveredRepos
// flow that wires auto-discovery to the Flutter UI's NEW badge.
func TestProcessDiscoveredRepos_PublishesSSEEvent(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"a/known"}

	prs := []*gh.PullRequest{
		{RepositoryURL: "https://api.github.com/repos/a/known", Number: 1},
		{RepositoryURL: "https://api.github.com/repos/a/new", Number: 2},
		{RepositoryURL: "https://api.github.com/repos/a/another-new", Number: 3},
	}
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	// Mirror the adapter's in-memory mutate + snapshot step.
	added := upsertDiscoveredRepos(cfg, prs)
	reposSnap := append([]string(nil), cfg.GitHub.Repositories...)
	nonMonSnap := append([]string(nil), cfg.GitHub.NonMonitored...)

	if len(added) != 2 {
		t.Fatalf("expected 2 added repos, got %v", added)
	}

	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	ch := broker.Subscribe()
	if ch == nil {
		t.Fatal("subscribe returned nil — broker rejected subscriber")
	}
	defer broker.Unsubscribe(ch)

	st := newMemStore(t)

	processDiscoveredRepos(added, reposSnap, nonMonSnap, st, broker, time.Unix(1_700_000_000, 0))

	// Drain exactly len(added) events — one per added repo — then assert no
	// extra event arrives (e.g. a stray publish for the already-known repo).
	seen := map[string]int{}
	for i := 0; i < len(added); i++ {
		select {
		case ev := <-ch:
			if ev.Type != sse.EventRepoDiscovered {
				t.Fatalf("event %d: type = %q, want %q", i, ev.Type, sse.EventRepoDiscovered)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
				t.Fatalf("event %d: unmarshal data %q: %v", i, ev.Data, err)
			}
			repo, ok := payload["repo"].(string)
			if !ok {
				t.Fatalf("event %d: payload missing string `repo`: %v", i, payload)
			}
			seen[repo]++
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for event %d of %d", i+1, len(added))
		}
	}

	// Assert we got one event per added repo — no duplicates, no misses.
	got := make([]string, 0, len(seen))
	for r, n := range seen {
		if n != 1 {
			t.Errorf("repo %q: got %d events, want 1", r, n)
		}
		got = append(got, r)
	}
	sort.Strings(got)
	want := []string{"a/another-new", "a/new"}
	if len(got) != len(want) {
		t.Fatalf("events repos = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("events repos = %v, want %v", got, want)
		}
	}

	// No event should have fired for the already-known repo.
	if _, ok := seen["a/known"]; ok {
		t.Error("a/known must not trigger a repo_discovered event")
	}

	// And no extra event should land on the channel (ruling out a stray
	// publish for a no-op case).
	select {
	case ev := <-ch:
		t.Fatalf("unexpected extra event after len(added): %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// good — channel quiescent
	}
}

func TestProcessDiscoveredReposOrderedPublishesOneBatch(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	ch := broker.Subscribe()
	if ch == nil {
		t.Fatal("subscribe returned nil")
	}
	defer broker.Unsubscribe(ch)

	calls := 0
	batchSize := 0
	processDiscoveredReposOrdered(
		[]string{"org/a", "org/b"},
		[]string{"org/a", "org/b"},
		[]string{},
		newMemStore(t),
		broker,
		time.Unix(1_700_000_000, 0),
		func(events []sse.Event) error {
			calls++
			batchSize = len(events)
			return nil
		},
	)
	if calls != 1 || batchSize != 2 {
		t.Fatalf("ordered publish calls=%d batch=%d, want one batch of 2", calls, batchSize)
	}
	for i := 0; i < 2; i++ {
		select {
		case event := <-ch:
			if !event.NATSForwarded {
				t.Fatalf("event %d was not marked as synchronously forwarded", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for broker copy %d", i)
		}
	}
}

func TestProcessDiscoveredReposOrderedKeepsBridgeFallbackOnPublishFailure(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	ch := broker.Subscribe()
	if ch == nil {
		t.Fatal("subscribe returned nil")
	}
	defer broker.Unsubscribe(ch)

	processDiscoveredReposOrdered(
		[]string{"org/a"},
		[]string{"org/a"},
		[]string{},
		newMemStore(t),
		broker,
		time.Unix(1_700_000_000, 0),
		func([]sse.Event) error { return errors.New("nats unavailable") },
	)
	select {
	case event := <-ch:
		if event.NATSForwarded {
			t.Fatal("failed synchronous publish suppressed the asynchronous bridge fallback")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for fallback broker event")
	}
}

// TestProcessDiscoveredRepos_NoEventsWhenAddedEmpty confirms the helper is a
// no-op when nothing was discovered — no publish, no store write. Guards
// against a regression where an empty poll cycle would churn SSE traffic.
func TestProcessDiscoveredRepos_NoEventsWhenAddedEmpty(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	ch := broker.Subscribe()
	if ch == nil {
		t.Fatal("subscribe returned nil — broker rejected subscriber")
	}
	defer broker.Unsubscribe(ch)

	st := newMemStore(t)

	processDiscoveredRepos(nil, nil, nil, st, broker, time.Unix(1_700_000_000, 0))

	select {
	case ev := <-ch:
		t.Fatalf("expected no events, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// good — no event
	}

	// And nothing should have been persisted.
	rows, err := st.ListConfigs()
	if err != nil {
		t.Fatalf("list configs: %v", err)
	}
	for _, k := range []string{"repositories", "non_monitored", "repo_first_seen"} {
		if _, present := rows[k]; present {
			t.Errorf("store should be untouched but key %q was written", k)
		}
	}
}
