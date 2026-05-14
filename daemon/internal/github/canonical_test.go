package github_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// TestGetCanonicalFullName_ReturnsNewSlug pins the contract used by the
// rename probe (#489): calling GET /repos/{owner}/{repo} against the OLD
// slug returns the canonical, current `full_name` because GitHub
// transparently 301s the request and the upstream JSON always carries
// the up-to-date name. Detection is then a simple string compare.
func TestGetCanonicalFullName_ReturnsNewSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/old" {
			t.Errorf("path = %q, want /repos/acme/old", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"full_name":"acme/new","name":"new","owner":{"login":"acme"}}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	got, err := c.GetCanonicalFullName("acme/old")
	if err != nil {
		t.Fatalf("GetCanonicalFullName: %v", err)
	}
	if got != "acme/new" {
		t.Errorf("canonical = %q, want acme/new", got)
	}
}

// TestGetCanonicalFullName_404_PropagatesAPIError ensures a deleted-repo
// response is distinguishable from a rename: callers treat *APIError
// with StatusCode=404 as "repo unreachable", which routes to a separate
// SSE event instead of the rename reconciler.
func TestGetCanonicalFullName_404_PropagatesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := c.GetCanonicalFullName("acme/gone")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	var apiErr *gh.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
	}
}
