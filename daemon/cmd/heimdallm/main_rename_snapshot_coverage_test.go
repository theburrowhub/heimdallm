package main

import (
	"reflect"
	"sync"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

func TestRenameRepoSourcesReturnLockedSnapshots(t *testing.T) {
	cfg := &config.Config{GitHub: config.GitHubConfig{
		Repositories: []string{"acme/api", "acme/web"},
		NonMonitored: []string{"acme/legacy"},
	}}
	var cfgMu sync.Mutex
	monitored := renameRepoListFn(cfg, &cfgMu)
	disabled := renameNonMonitoredListFn(cfg, &cfgMu)

	gotMonitored := monitored()
	gotDisabled := disabled()
	if !reflect.DeepEqual(gotMonitored, []string{"acme/api", "acme/web"}) {
		t.Fatalf("monitored snapshot = %v", gotMonitored)
	}
	if !reflect.DeepEqual(gotDisabled, []string{"acme/legacy"}) {
		t.Fatalf("non-monitored snapshot = %v", gotDisabled)
	}

	gotMonitored[0] = "tampered/locally"
	gotDisabled[0] = "tampered/locally"
	if cfg.GitHub.Repositories[0] != "acme/api" || cfg.GitHub.NonMonitored[0] != "acme/legacy" {
		t.Fatalf("returned slices alias live config: %+v", cfg.GitHub)
	}
}
