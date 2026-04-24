package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
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

func TestFetchComments_MergesAndSorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"user":          map[string]string{"login": "bob"},
					"body":          "inline comment",
					"created_at":    "2024-01-02T00:00:00Z",
					"path":          "main.go",
					"original_line": 10,
				},
			})
		case "/repos/org/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"user":       map[string]string{"login": "alice"},
					"body":       "general comment",
					"created_at": "2024-01-01T00:00:00Z",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	comments, err := client.FetchComments("org/repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	// Sorted by CreatedAt: alice first (2024-01-01), bob second (2024-01-02)
	if comments[0].Author != "alice" {
		t.Errorf("expected alice first, got %s", comments[0].Author)
	}
	if comments[1].Author != "bob" {
		t.Errorf("expected bob second, got %s", comments[1].Author)
	}
	if comments[1].File != "main.go" {
		t.Errorf("expected File=main.go for review comment, got %q", comments[1].File)
	}
	if comments[1].Line != 10 {
		t.Errorf("expected Line=10 for review comment, got %d", comments[1].Line)
	}
}

func TestFetchComments_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	comments, err := client.FetchComments("org/repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(comments))
	}
}

func TestFetchComments_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	_, err := client.FetchComments("org/repo", 1)
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}

// TestFetchIssueCommentsOnly_IgnoresPREndpoint locks in the fix for #292:
// the issue-triage path must NOT call /pulls/:n/comments on an issue
// number. A 404 from the PR endpoint used to abort the whole FetchComments
// call, which cascaded into the marker-scan fallthrough that produced 47
// re-triages on #264 in 46 minutes. FetchIssueCommentsOnly sidesteps the
// PR endpoint entirely, so even when /pulls/:n/comments would 404 the
// issue comments still come back.
func TestFetchIssueCommentsOnly_IgnoresPREndpoint(t *testing.T) {
	pullsHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/pulls/1/comments":
			pullsHit = true
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case "/repos/org/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"user":       map[string]string{"login": "alice"},
					"body":       "<!-- heimdallm:done -->\nfinished",
					"created_at": "2024-01-01T00:00:00Z",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	comments, err := client.FetchIssueCommentsOnly("org/repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pullsHit {
		t.Errorf("FetchIssueCommentsOnly must NOT call /pulls/:n/comments")
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Author != "alice" {
		t.Errorf("author mismatch: got %q", comments[0].Author)
	}
}

// TestFetchIssueCommentsOnly_PropagatesRealErrors makes sure we don't
// over-rotate: a genuine 5xx from /issues/:n/comments still surfaces so
// callers can log/retry. Only the PR-endpoint leg is bypassed.
func TestFetchIssueCommentsOnly_PropagatesRealErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"upstream"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	_, err := client.FetchIssueCommentsOnly("org/repo", 1)
	if err == nil {
		t.Fatal("expected error for 500 from issues endpoint, got nil")
	}
}
