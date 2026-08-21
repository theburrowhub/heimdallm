package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestAuthenticatedUserCachesSuccessfulLookup(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
	}))
	defer srv.Close()

	client := gh.NewClient("token", gh.WithBaseURL(srv.URL))
	for i := 0; i < 2; i++ {
		got, err := client.AuthenticatedUser()
		if err != nil {
			t.Fatalf("AuthenticatedUser call %d: %v", i+1, err)
		}
		if got != "heimdallm-bot" {
			t.Fatalf("AuthenticatedUser call %d = %q, want heimdallm-bot", i+1, got)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("GET /user calls = %d, want 1", got)
	}
}

func TestAuthenticatedUserRetriesAfterFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
	}))
	defer srv.Close()

	client := gh.NewClient("token", gh.WithBaseURL(srv.URL))
	if _, err := client.AuthenticatedUser(); err == nil {
		t.Fatal("first AuthenticatedUser call should fail")
	}
	if got, err := client.AuthenticatedUser(); err != nil || got != "heimdallm-bot" {
		t.Fatalf("retry = %q, %v; want heimdallm-bot, nil", got, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("GET /user calls = %d, want 2", got)
	}
}
