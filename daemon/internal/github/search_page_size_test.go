package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// searchServer answers the Search API with n PRs whose descriptions are
// bodyLen bytes each, under whichever qualifier was asked for.
func searchServer(t *testing.T, n, bodyLen int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "alice"})
		case "/search/issues":
			q := r.URL.Query().Get("q")
			items := make([]map[string]any, 0, n)
			for i := 0; i < n; i++ {
				author := "alice"
				if strings.Contains(q, "assignee:") {
					author = "bob"
				}
				items = append(items, map[string]any{
					"id": 1000 + i, "number": i + 1, "title": fmt.Sprintf("PR %d", i),
					"html_url":       fmt.Sprintf("https://github.com/org/repo/pull/%d", i+1),
					"repository_url": "https://api.github.com/repos/org/repo",
					"state":          "open",
					"user":           map[string]string{"login": author},
					"assignees":      []map[string]string{{"login": "alice"}},
					"body":           strings.Repeat("x", bodyLen),
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A real `assignee:` search for someone on many PRs with long descriptions
// returned 1.25 MB. Cut at the generic 1 MiB body limit the JSON failed to
// decode, and every assigned PR silently vanished from merge tracking.
func TestFetchMergeTrackingPRs_SearchPageLargerThanOneMiB(t *testing.T) {
	srv := searchServer(t, 40, 50*1024) // ~2 MB per page
	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))

	prs, err := client.FetchMergeTrackingPRs(true)
	if err != nil {
		t.Fatalf("a 2 MB search page must decode, got: %v", err)
	}
	assigned := 0
	for _, pr := range prs {
		if pr.IsAssignee {
			assigned++
		}
	}
	if assigned == 0 {
		t.Fatalf("assigned PRs were dropped: %d PRs, none assigned", len(prs))
	}
}

// A page past the ceiling is reported as oversized, not as a JSON error on a
// silently truncated body.
func TestFetchPRsToReview_OversizedSearchPageIsNamed(t *testing.T) {
	srv := searchServer(t, 11, 1024*1024) // ~11 MB
	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))

	_, err := client.FetchPRsToReview()
	if err == nil {
		t.Fatal("an 11 MB page must be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should say the page is too large, got: %v", err)
	}
}
