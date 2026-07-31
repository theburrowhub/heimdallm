package github_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// The GET path lost the circuit breaker during the rebase over #583: main
// routed everything through doWithBody, which checks it, and the ETag layer
// gave GETs their own path in do() that did not. Nothing failed — it was a
// silent semantic merge casualty, the class of regression that only a pinning
// test catches on the next rebase. This is that test.

func TestGETPath_HonoursAndOpensTheBreaker(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("token", gh.WithBaseURL(srv.URL))

	// First GET is rejected for rate limiting and must OPEN the breaker.
	resp, err := c.DoGETForTest("/rate_limit", "application/vnd.github+json")
	if err != nil {
		t.Fatalf("first GET should return the 403 response, not an error: %v", err)
	}
	resp.Body.Close()
	if hits != 1 {
		t.Fatalf("setup: expected 1 request, got %d", hits)
	}

	// Second GET must fail fast without reaching the server.
	_, err = c.DoGETForTest("/rate_limit", "application/vnd.github+json")
	if err == nil {
		t.Fatal("second GET should fail fast while the breaker is open")
	}
	var rlErr *gh.RateLimitError
	if !errors.As(err, &rlErr) {
		t.Errorf("error = %v (%T), want *gh.RateLimitError", err, err)
	}
	if hits != 1 {
		t.Errorf("GET sent %d requests; the second must be suppressed by the breaker", hits)
	}
}

// The ETag layer is what gave GETs a separate path in the first place, so pin
// that a cached (conditional) GET is gated too — not just the uncached one.
func TestGETPath_BreakerAppliesToConditionalRequests(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch hits {
		case 1:
			// Prime the ETag cache with a normal 200.
			w.Header().Set("ETag", `W/"abc"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		}
	}))
	defer srv.Close()

	c := gh.NewClient("token", gh.WithBaseURL(srv.URL))

	r1, err := c.DoGETForTest("/cached", "application/vnd.github+json")
	if err != nil {
		t.Fatalf("priming GET: %v", err)
	}
	r1.Body.Close()

	// Second call sends If-None-Match and is rate-limited → breaker opens.
	r2, err := c.DoGETForTest("/cached", "application/vnd.github+json")
	if err != nil {
		t.Fatalf("second GET should return the 403 response: %v", err)
	}
	r2.Body.Close()

	// Third must be suppressed even though a cache entry exists for the path.
	if _, err := c.DoGETForTest("/cached", "application/vnd.github+json"); err == nil {
		t.Error("a conditional GET must also honour the open breaker")
	}
	if hits != 2 {
		t.Errorf("server saw %d requests, want 2 (third suppressed)", hits)
	}
}
