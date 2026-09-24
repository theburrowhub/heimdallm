package github_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// The GraphQL path was the one request path left outside the rate-limit
// circuit breaker: it POSTed straight through c.http.Do, so merge tracking's
// GraphQL queries kept hitting GitHub during a secondary/abuse block — the
// traffic that keeps such a block alive. These tests pin both halves of the
// fix: honour an open breaker, and open it on a rejection.

// tripBreaker drives one rate-limited GraphQL response through the client so
// the breaker opens, and returns the number of requests that took to make.
func tripBreaker(t *testing.T, c *gh.Client) {
	t.Helper()
	if err := c.GraphQLForTest(`query { viewer { login } }`); err == nil {
		t.Fatal("expected the rate-limited response to surface as an error")
	}
}

func TestGraphQL_OpenBreakerFailsFastWithoutRequest(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// First call: GitHub rejects for rate limiting → breaker opens.
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("token", gh.WithBaseURL(srv.URL))
	tripBreaker(t, c)
	if hits != 1 {
		t.Fatalf("setup: expected exactly 1 request to trip the breaker, got %d", hits)
	}

	// Second call must not reach the server at all.
	err := c.GraphQLForTest(`query { viewer { login } }`)
	if err == nil {
		t.Fatal("expected an error while the breaker is open")
	}
	var rlErr *gh.RateLimitError
	if !errors.As(err, &rlErr) {
		t.Errorf("error = %v (%T), want *gh.RateLimitError", err, err)
	}
	if hits != 1 {
		t.Errorf("breaker open: GraphQL sent %d requests, want the second suppressed (1 total)", hits)
	}
}

func TestGraphQL_RateLimitedResponseOpensBreakerForOtherPaths(t *testing.T) {
	var restHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		restHits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"total_count":0,"items":[]}`))
	}))
	defer srv.Close()

	c := gh.NewClient("token", gh.WithBaseURL(srv.URL))
	tripBreaker(t, c)

	// The breaker is shared: a GET must now fail fast too, rather than adding
	// traffic on a different path during the same block.
	if _, err := c.DoGETForTest("/rate_limit", "application/vnd.github+json"); err == nil {
		t.Error("expected the GET path to honour a breaker opened by GraphQL")
	}
	if restHits != 0 {
		t.Errorf("GET reached the server %d times while the breaker was open", restHits)
	}
}
