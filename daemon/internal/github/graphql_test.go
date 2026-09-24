package github_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// ── GraphQL test helpers ──────────────────────────────────────────────────────

const testGraphQLQuery = `query { viewer { login } }`

// gqlDataEnvelope builds a successful GraphQL response envelope.
func gqlDataEnvelope() []byte {
	b, _ := json.Marshal(map[string]any{"data": map[string]any{"viewer": map[string]string{"login": "bot"}}})
	return b
}

// gqlErrorEnvelope builds a GraphQL response with top-level errors.
func gqlErrorEnvelope(messages ...string) []byte {
	errs := make([]map[string]string, len(messages))
	for i, m := range messages {
		errs[i] = map[string]string{"message": m}
	}
	b, _ := json.Marshal(map[string]any{"errors": errs})
	return b
}

// ── graphQL() low-level tests ─────────────────────────────────────────────────

func TestGraphQLLowLevel_HTTPErrorReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := gh.NewClient("bad-token", gh.WithBaseURL(srv.URL))
	err := client.GraphQLForTest(testGraphQLQuery)
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should contain status 401, got: %v", err)
	}
}

func TestGraphQLLowLevel_ErrorsEnvelopeReturnsError(t *testing.T) {
	body := gqlErrorEnvelope("NOT_FOUND: resource not found", "FORBIDDEN: insufficient scope")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	err := client.GraphQLForTest(testGraphQLQuery)
	if err == nil {
		t.Fatal("expected error from GraphQL errors envelope, got nil")
	}
	if !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Errorf("error should contain first error message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "FORBIDDEN") {
		t.Errorf("error should contain second error message, got: %v", err)
	}
}

func TestGraphQLLowLevel_RateObserverNotified(t *testing.T) {
	body := gqlDataEnvelope()

	observedCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4500")
		w.Header().Set("X-RateLimit-Resource", "graphql")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	client.SetRateObserver(gh.RateLimitObserverFunc(func(resp *http.Response) {
		observedCount++
	}))

	if err := client.GraphQLForTest(testGraphQLQuery); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if observedCount != 1 {
		t.Errorf("rate observer should have been called once, got %d", observedCount)
	}
}

// ── Gate tests (through a merge-tracking mutation) ───────────────────────────

func TestGraphQLUsesGraphQLGateNotRESTSearchGate(t *testing.T) {
	body := gqlDataEnvelope()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	client.SetSearchGate(func() error { return errors.New("REST search exhausted") })
	graphqlCalls := 0
	client.SetGraphQLGate(func() error {
		graphqlCalls++
		return nil
	})

	if err := client.DisableAutoMerge("PR_node"); err != nil {
		t.Fatalf("GraphQL should not be blocked by REST Search: %v", err)
	}
	if graphqlCalls != 1 {
		t.Fatalf("GraphQL gate calls = %d, want 1", graphqlCalls)
	}
}

func TestGraphQLGateErrorStopsRequest(t *testing.T) {
	gateErr := errors.New("graphql budget exhausted")
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	client.SetGraphQLGate(func() error { return gateErr })

	err := client.DisableAutoMerge("PR_node")
	if !errors.Is(err, gateErr) {
		t.Fatalf("GraphQL error = %v, want wrapped gate error", err)
	}
	if requestCount != 0 {
		t.Fatalf("GraphQL requests = %d, want 0 when gate rejects", requestCount)
	}
}

func TestSetGraphQLGateNilDisablesGate(t *testing.T) {
	body := gqlDataEnvelope()
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	client.SetGraphQLGate(func() error { return errors.New("stale gate") })
	client.SetGraphQLGate(nil)

	if err := client.DisableAutoMerge("PR_node"); err != nil {
		t.Fatalf("GraphQL after SetGraphQLGate(nil): %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("GraphQL requests = %d, want 1 after disabling gate", requestCount)
	}
}
