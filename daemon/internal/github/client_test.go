package github_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestClient_PerOrgToken_RepoEndpoint(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"default_branch":"main"}`))
	}))
	defer srv.Close()

	tr := gh.NewTokenRouter("default-tok", map[string]string{
		"freepik-company": "fgpat-freepik",
	})
	client := gh.NewClient("default-tok", gh.WithBaseURL(srv.URL), gh.WithTokenRouter(tr))

	// Repo-specific endpoint should use org token.
	_, _ = client.GetDefaultBranch("freepik-company/ai-platform")
	if gotAuth != "Bearer fgpat-freepik" {
		t.Errorf("repo endpoint used %q, want Bearer fgpat-freepik", gotAuth)
	}

	// Repo in a different org should use default token.
	_, _ = client.GetDefaultBranch("theburrowhub/heimdallm")
	if gotAuth != "Bearer default-tok" {
		t.Errorf("fallback endpoint used %q, want Bearer default-tok", gotAuth)
	}
}

func TestClient_PerOrgToken_GlobalEndpoint(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"login":"bot"}`))
	}))
	defer srv.Close()

	tr := gh.NewTokenRouter("default-tok", map[string]string{
		"freepik-company": "fgpat-freepik",
	})
	client := gh.NewClient("default-tok", gh.WithBaseURL(srv.URL), gh.WithTokenRouter(tr))

	// Global endpoint (/user) should always use default token.
	_, _ = client.AuthenticatedUser()
	if gotAuth != "Bearer default-tok" {
		t.Errorf("/user used %q, want Bearer default-tok", gotAuth)
	}
}

func TestClient_PerOrgToken_DiscoveryUsesOrgToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"total_count":0,"items":[]}`))
	}))
	defer srv.Close()

	tr := gh.NewTokenRouter("default-tok", map[string]string{
		"freepik-company": "fgpat-freepik",
	})
	client := gh.NewClient("default-tok", gh.WithBaseURL(srv.URL), gh.WithTokenRouter(tr))

	_, _ = client.FetchReposByTopic("heimdallm-review", []string{"freepik-company"})
	if gotAuth != "Bearer fgpat-freepik" {
		t.Errorf("discovery for freepik-company used %q, want Bearer fgpat-freepik", gotAuth)
	}
}

func TestClient_NoTokenRouter_UsesDefaultToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"default_branch":"main"}`))
	}))
	defer srv.Close()

	client := gh.NewClient("classic-pat", gh.WithBaseURL(srv.URL))

	_, _ = client.GetDefaultBranch("freepik-company/ai-platform")
	if gotAuth != "Bearer classic-pat" {
		t.Errorf("without router used %q, want Bearer classic-pat", gotAuth)
	}
}

func TestClient_TokenForRepo(t *testing.T) {
	tr := gh.NewTokenRouter("default-tok", map[string]string{
		"freepik-company": "fgpat-freepik",
	})

	withRouter := gh.NewClient("default-tok", gh.WithTokenRouter(tr))
	if got := withRouter.TokenForRepo("freepik-company/repo"); got != "fgpat-freepik" {
		t.Errorf("TokenForRepo with router = %q, want fgpat-freepik", got)
	}
	if got := withRouter.TokenForRepo("other/repo"); got != "default-tok" {
		t.Errorf("TokenForRepo fallback = %q, want default-tok", got)
	}

	withoutRouter := gh.NewClient("classic-pat")
	if got := withoutRouter.TokenForRepo("freepik-company/repo"); got != "classic-pat" {
		t.Errorf("TokenForRepo without router = %q, want classic-pat", got)
	}
}
