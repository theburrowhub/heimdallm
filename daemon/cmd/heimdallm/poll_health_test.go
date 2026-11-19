package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
)

func TestPollHealth_CountsFailuresAndResetsOnSuccess(t *testing.T) {
	var h pollHealth
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	var snap server.HealthSnapshot
	h.fill(&snap)
	if !snap.LastPollSuccessAt.IsZero() || snap.ConsecutivePollFailures != 0 || snap.LastPollErrorKind != "" {
		t.Fatalf("fresh snapshot = %+v, want empty", snap)
	}

	h.record(t0, nil)
	h.record(t0.Add(time.Minute), &gh.RateLimitError{RetryAt: t0.Add(time.Hour)})
	h.record(t0.Add(2*time.Minute), &gh.StatusError{Op: "github: get user", StatusCode: http.StatusUnauthorized})
	h.fill(&snap)
	if !snap.LastPollSuccessAt.Equal(t0) {
		t.Errorf("last success = %v, want %v", snap.LastPollSuccessAt, t0)
	}
	if snap.ConsecutivePollFailures != 2 || snap.LastPollErrorKind != gh.PollErrorAuth || !snap.LastPollErrorAt.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("failing snapshot = %+v, want 2 failures, the latest kind and time", snap)
	}

	h.record(t0.Add(3*time.Minute), nil)
	snap = server.HealthSnapshot{}
	h.fill(&snap)
	if snap.ConsecutivePollFailures != 0 || snap.LastPollErrorKind != "" || !snap.LastPollErrorAt.IsZero() || !snap.LastPollSuccessAt.Equal(t0.Add(3*time.Minute)) {
		t.Errorf("after success = %+v, want failures cleared and the new success time", snap)
	}
}

// The PR tier must hand the fetch error to the recorder: before #897 a failed
// poll and a successful one were indistinguishable on /health.
func TestRunTier2ReportsThePollOutcome(t *testing.T) {
	for name, tc := range map[string]struct {
		handler  http.HandlerFunc
		wantKind string // "" = the poll succeeded
	}{
		"revoked token": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			},
			wantKind: gh.PollErrorAuth,
		},
		"healthy": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
				case "/search/issues":
					_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "items": []any{}})
				default:
					http.NotFound(w, r)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			broker := sse.NewBroker()
			broker.Start()
			defer broker.Stop()
			cfg := &config.Config{GitHub: config.GitHubConfig{Repositories: []string{"org/repo"}}}
			login := "heimdallm-bot"
			var cfgMu, loginMu sync.Mutex
			adapter := &tier2Adapter{
				ghClient: gh.NewClient("token", gh.WithBaseURL(srv.URL)), store: newMemStore(t), broker: broker,
				cfg: &cfg, cfgMu: &cfgMu, login: &login, loginMu: &loginMu,
				lastSkippedUpdatedAt: make(map[int64]time.Time),
			}
			reposCh := make(chan []string, 1)
			reposCh <- []string{"org/repo"}
			outcomes := make(chan error, 1)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				runTier2(ctx, adapter, &requestBudgetPRPublisher{published: make(chan scheduler.Tier2PR, 1)}, broker,
					func() []string { return []string{"org/repo"} }, reposCh, time.Hour, true,
					func(kind string, _ time.Time, err error) {
						if kind == "prs" {
							select {
							case outcomes <- err:
							default:
							}
						}
					})
			}()
			defer func() {
				cancel()
				<-done
			}()

			select {
			case err := <-outcomes:
				if tc.wantKind == "" {
					if err != nil {
						t.Fatalf("poll error = %v, want a successful poll", err)
					}
					return
				}
				if err == nil {
					t.Fatal("poll error = nil, want the fetch failure")
				}
				if got := gh.ClassifyPollError(err); got != tc.wantKind {
					t.Errorf("kind = %q (%v), want %q", got, err, tc.wantKind)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the PR tier never reported a completed poll")
			}
		})
	}
}
