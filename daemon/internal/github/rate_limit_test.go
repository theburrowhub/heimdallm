package github_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rate_limit" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fake" {
			t.Errorf("Authorization = %q, want Bearer fake", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"resources": {
				"core":    {"limit": 5000, "remaining": 4990, "reset": 1700000000, "used": 10},
				"search":  {"limit": 30,   "remaining": 28,   "reset": 1700000060, "used": 2},
				"graphql": {"limit": 5000, "remaining": 5000, "reset": 1700000120, "used": 0}
			},
			"rate": {"limit": 5000, "remaining": 4990, "reset": 1700000000, "used": 10}
		}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	rl, err := c.RateLimit()
	if err != nil {
		t.Fatalf("RateLimit: %v", err)
	}
	if rl.Core.Limit != 5000 || rl.Core.Remaining != 4990 || rl.Core.Used != 10 || rl.Core.Reset != 1700000000 {
		t.Errorf("core = %+v, want limit=5000 remaining=4990 used=10 reset=1700000000", rl.Core)
	}
	if rl.Search.Limit != 30 || rl.Search.Remaining != 28 {
		t.Errorf("search = %+v, want limit=30 remaining=28", rl.Search)
	}
	if rl.GraphQL.Limit != 5000 || rl.GraphQL.Remaining != 5000 {
		t.Errorf("graphql = %+v, want limit=5000 remaining=5000", rl.GraphQL)
	}
}

func TestRateLimit_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if _, err := c.RateLimit(); err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

// TestRateLimit_CircuitBreakerPausesAfter403 verifies that once GitHub
// rate-limits a request, the client opens its breaker and subsequent calls fail
// fast WITHOUT hitting GitHub — so the daemon stops piling traffic onto an
// active (secondary) block instead of keeping it alive.
func TestRateLimit_CircuitBreakerPausesAfter403(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	// First call reaches GitHub, is rate-limited → surfaced as an error.
	if _, err := c.IsRepoArchived("acme/widget"); err == nil {
		t.Fatal("expected an error on the first rate-limited call")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("after first call server calls = %d, want 1", got)
	}

	// Breaker is open: the next call fails fast without contacting GitHub.
	_, err := c.IsRepoArchived("acme/other")
	var rle *gh.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("second call error = %v, want *github.RateLimitError", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("second call hit the server (calls=%d); breaker should short-circuit", got)
	}
}
