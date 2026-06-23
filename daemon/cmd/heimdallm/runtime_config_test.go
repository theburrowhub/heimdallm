package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
)

// TestApplyClientRuntimeConfig_HotTogglesGraphQL proves a use_graphql change
// applied via applyClientRuntimeConfig (the reload path) takes effect on the
// live client without recreating it — guarding against the regression where a
// GUI/TUI toggle was persisted but never re-applied until a daemon restart.
func TestApplyClientRuntimeConfig_HotTogglesGraphQL(t *testing.T) {
	data := map[string]any{"r0": map[string]any{"isArchived": true}}
	envelope, _ := json.Marshal(map[string]any{"data": data})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(envelope)
	}))
	defer srv.Close()

	ghClient := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	limiter := scheduler.NewRateLimiter(100)

	// GraphQL disabled by default → batch archive-check short-circuits with an
	// error so callers fall back to per-repo REST.
	if _, err := ghClient.BatchReposArchived([]string{"org/repo"}); err == nil {
		t.Fatal("expected disabled error before enabling GraphQL")
	}

	// Enable use_graphql via config and re-apply (mirrors the reload path).
	enabled := true
	applyClientRuntimeConfig(ghClient, limiter, &config.Config{
		Polling: config.PollingConfig{UseGraphQL: &enabled},
	})

	got, err := ghClient.BatchReposArchived([]string{"org/repo"})
	if err != nil {
		t.Fatalf("expected GraphQL batch to succeed after enabling, got %v", err)
	}
	if !got["org/repo"] {
		t.Errorf("want org/repo archived=true via GraphQL, got %v", got)
	}

	// Toggle back off → reverts to the disabled (fall-back) behaviour.
	disabled := false
	applyClientRuntimeConfig(ghClient, limiter, &config.Config{
		Polling: config.PollingConfig{UseGraphQL: &disabled},
	})
	if _, err := ghClient.BatchReposArchived([]string{"org/repo"}); err == nil {
		t.Fatal("expected disabled error after toggling GraphQL back off")
	}
}
