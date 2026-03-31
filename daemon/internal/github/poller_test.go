package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/heimdallr/daemon/internal/github"
)

func TestFetchPRs(t *testing.T) {
	prs := []gh.PullRequest{
		{ID: 1, Number: 42, Title: "Fix bug", HTMLURL: "https://github.com/org/repo/pull/42",
			User: gh.User{Login: "alice"}, State: "open",
			Head: gh.Branch{Repo: gh.Repo{FullName: "org/repo"}},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			json.NewEncoder(w).Encode(map[string]string{"login": "alice"})
		case "/search/issues":
			result := struct {
				Items []gh.PullRequest `json:"items"`
			}{Items: prs}
			json.NewEncoder(w).Encode(result)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchPRs([]string{"org/repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(got))
	}
	if got[0].Title != "Fix bug" {
		t.Errorf("title mismatch: %q", got[0].Title)
	}
}

func TestFetchDiff(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n+added line\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.github.v3.diff")
		w.Write([]byte(diff))
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchDiff("org/repo", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != diff {
		t.Errorf("diff mismatch: %q", got)
	}
}
