package main

import (
	"sync"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/sse"
)

func TestUpsertDiscoveredFromTopics_AddsNewRepos(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"org/existing"}

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var mu sync.Mutex
	a := &tier2Adapter{
		cfgMu:  &mu,
		cfg:    &cfg,
		store:  s,
		broker: broker,
	}

	a.upsertDiscoveredFromTopics([]string{"org/existing", "org/new1", "org/new2"})

	if len(cfg.GitHub.Repositories) != 3 {
		t.Fatalf("expected 3 repos, got %v", cfg.GitHub.Repositories)
	}
	want := map[string]bool{"org/existing": true, "org/new1": true, "org/new2": true}
	for _, r := range cfg.GitHub.Repositories {
		if !want[r] {
			t.Fatalf("unexpected repo %q in Repositories", r)
		}
	}
}

func TestUpsertDiscoveredFromTopics_SkipsKnownRepos(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"org/a"}
	cfg.GitHub.NonMonitored = []string{"org/b"}

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var mu sync.Mutex
	a := &tier2Adapter{
		cfgMu:  &mu,
		cfg:    &cfg,
		store:  s,
		broker: broker,
	}

	a.upsertDiscoveredFromTopics([]string{"org/a", "org/b"})

	if len(cfg.GitHub.Repositories) != 1 {
		t.Fatalf("Repositories should be unchanged, got %v", cfg.GitHub.Repositories)
	}
	if len(cfg.GitHub.NonMonitored) != 1 {
		t.Fatalf("NonMonitored should be unchanged, got %v", cfg.GitHub.NonMonitored)
	}
}

func TestUpsertDiscoveredFromTopics_IdempotentOnSecondCall(t *testing.T) {
	cfg := &config.Config{}

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var mu sync.Mutex
	a := &tier2Adapter{
		cfgMu:  &mu,
		cfg:    &cfg,
		store:  s,
		broker: broker,
	}

	a.upsertDiscoveredFromTopics([]string{"org/repo1", "org/repo2"})
	a.upsertDiscoveredFromTopics([]string{"org/repo1", "org/repo2"})

	if len(cfg.GitHub.Repositories) != 2 {
		t.Fatalf("expected 2 repos after idempotent call, got %v", cfg.GitHub.Repositories)
	}
}

func TestUpsertDiscoveredFromTopics_RespectsDisabledFlag(t *testing.T) {
	f := false
	cfg := &config.Config{}
	cfg.GitHub.AutoEnablePROnDiscovery = &f

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var mu sync.Mutex
	a := &tier2Adapter{
		cfgMu:  &mu,
		cfg:    &cfg,
		store:  s,
		broker: broker,
	}

	a.upsertDiscoveredFromTopics([]string{"org/new"})

	if len(cfg.GitHub.Repositories) != 0 {
		t.Fatalf("repo should not be in Repositories when disabled, got %v", cfg.GitHub.Repositories)
	}
	if len(cfg.GitHub.NonMonitored) != 1 || cfg.GitHub.NonMonitored[0] != "org/new" {
		t.Fatalf("repo should be in NonMonitored, got %v", cfg.GitHub.NonMonitored)
	}
}

func TestUpsertDiscoveredFromTopics_EmptyInput(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"org/a"}

	s := newMemStore(t)

	var mu sync.Mutex
	a := &tier2Adapter{
		cfgMu:  &mu,
		cfg:    &cfg,
		store:  s,
		broker: nil,
	}

	a.upsertDiscoveredFromTopics(nil)
	a.upsertDiscoveredFromTopics([]string{})

	if len(cfg.GitHub.Repositories) != 1 {
		t.Fatalf("empty input should be no-op, got %v", cfg.GitHub.Repositories)
	}
}
