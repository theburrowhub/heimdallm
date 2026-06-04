package github_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestFetchPRsToReviewFiltersSelfAuthored(t *testing.T) {
	prs := []gh.PullRequest{
		{ID: 1, Number: 41, Title: "Own PR", HTMLURL: "https://github.com/org/repo/pull/41",
			User: gh.User{Login: "alice"}, State: "open",
			Head: gh.Branch{Repo: gh.Repo{FullName: "org/repo"}},
		},
		{ID: 2, Number: 42, Title: "Team PR", HTMLURL: "https://github.com/org/repo/pull/42",
			User: gh.User{Login: "bob"}, State: "open",
			Head: gh.Branch{Repo: gh.Repo{FullName: "org/repo"}},
		},
	}
	var searchQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			json.NewEncoder(w).Encode(map[string]string{"login": "alice"})
		case "/search/issues":
			searchQuery = r.URL.Query().Get("q")
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
	got, err := client.FetchPRsToReview()
	if err != nil {
		t.Fatalf("FetchPRsToReview: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 PR after self-author filter, got %d", len(got))
	}
	if got[0].User.Login != "bob" {
		t.Fatalf("remaining author = %q, want bob", got[0].User.Login)
	}
	if !strings.Contains(searchQuery, "review-requested:alice") {
		t.Fatalf("search query = %q, want review-requested:alice", searchQuery)
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

// TestFetchComments_PaginatesReviewAndIssueComments locks in the fix for
// theburrowhub/heimdallm#512: GitHub returns 30 comments per page sorted
// ascending by created_at, so without explicit pagination any PR with >30
// review or issue comments silently lost the newest entries — exactly the
// ones a re-review depends on. This test mocks both endpoints to return
// 100+50 (full + partial page) and asserts FetchComments returns all 300
// merged entries.
func TestFetchComments_PaginatesReviewAndIssueComments(t *testing.T) {
	const fullPage = 100
	const tailPage = 50

	var reviewPages, issuePages int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := parsePageParam(t, r)
		if pp := r.URL.Query().Get("per_page"); pp != "100" {
			t.Errorf("expected per_page=100, got %q on path %s", pp, r.URL.Path)
		}
		switch r.URL.Path {
		case "/repos/org/repo/pulls/1/comments":
			atomic.AddInt32(&reviewPages, 1)
			emitReviewCommentsPage(w, page, fullPage, tailPage, "review")
		case "/repos/org/repo/issues/1/comments":
			atomic.AddInt32(&issuePages, 1)
			emitIssueCommentsPage(w, page, fullPage, tailPage, "issue")
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
	if got, want := len(comments), 2*(fullPage+tailPage); got != want {
		t.Fatalf("expected %d total comments, got %d", want, got)
	}
	// Both endpoints fetched 2 pages each (1 full + 1 partial).
	if got := atomic.LoadInt32(&reviewPages); got != 2 {
		t.Errorf("review endpoint hit count: got %d, want 2", got)
	}
	if got := atomic.LoadInt32(&issuePages); got != 2 {
		t.Errorf("issue endpoint hit count: got %d, want 2", got)
	}
	// Spot-check that comments from the tail page (only available via
	// pagination) actually made it through.
	foundTail := false
	for _, c := range comments {
		if c.Body == "review-2-49" {
			foundTail = true
			break
		}
	}
	if !foundTail {
		t.Errorf("expected to find a comment from the second review page (body=review-2-49); pagination dropped it")
	}
}

// TestFetchComments_ShortFirstPageStopsPaging guards against the inverse
// regression: a single short page must NOT trigger a redundant second
// request. Cheap PRs (<100 comments) should still cost exactly one round
// trip per endpoint.
func TestFetchComments_ShortFirstPageStopsPaging(t *testing.T) {
	var reviewPages, issuePages int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/pulls/1/comments":
			atomic.AddInt32(&reviewPages, 1)
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"user":          map[string]string{"login": "bob"},
					"body":          "inline",
					"created_at":    "2024-01-02T00:00:00Z",
					"path":          "main.go",
					"original_line": 10,
				},
			})
		case "/repos/org/repo/issues/1/comments":
			atomic.AddInt32(&issuePages, 1)
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"user":       map[string]string{"login": "alice"},
					"body":       "general",
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
	if got := atomic.LoadInt32(&reviewPages); got != 1 {
		t.Errorf("review endpoint hit %d times, want 1 (short page must not trigger another request)", got)
	}
	if got := atomic.LoadInt32(&issuePages); got != 1 {
		t.Errorf("issue endpoint hit %d times, want 1 (short page must not trigger another request)", got)
	}
}

// TestFetchComments_FullPageThenEmptyStopsCleanly covers the edge case
// where total_count is an exact multiple of per_page=100: the first page
// is full (so we paginate), the second page is empty, and pagination must
// terminate cleanly without surfacing an error.
func TestFetchComments_FullPageThenEmptyStopsCleanly(t *testing.T) {
	var reviewPages, issuePages int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := parsePageParam(t, r)
		switch r.URL.Path {
		case "/repos/org/repo/pulls/1/comments":
			atomic.AddInt32(&reviewPages, 1)
			if page == 1 {
				emitReviewCommentsPage(w, page, 100, 0, "review")
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
			}
		case "/repos/org/repo/issues/1/comments":
			atomic.AddInt32(&issuePages, 1)
			if page == 1 {
				emitIssueCommentsPage(w, page, 100, 0, "issue")
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
			}
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
	// 100 review + 100 issue, all from the first page.
	if got, want := len(comments), 200; got != want {
		t.Fatalf("expected %d comments, got %d", want, got)
	}
	if got := atomic.LoadInt32(&reviewPages); got != 2 {
		t.Errorf("review endpoint hits: got %d, want 2 (page 1 full + page 2 empty)", got)
	}
	if got := atomic.LoadInt32(&issuePages); got != 2 {
		t.Errorf("issue endpoint hits: got %d, want 2 (page 1 full + page 2 empty)", got)
	}
}

// TestFetchComments_MidPaginationErrorPropagates verifies that an HTTP 500
// on page 2 of /pulls/:n/comments surfaces as a hard error rather than a
// silently-truncated slice. Returning partial results would defeat the
// point of paginating in the first place.
func TestFetchComments_MidPaginationErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := parsePageParam(t, r)
		switch r.URL.Path {
		case "/repos/org/repo/pulls/1/comments":
			if page == 1 {
				emitReviewCommentsPage(w, page, 100, 0, "review")
				return
			}
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
		case "/repos/org/repo/issues/1/comments":
			// Issue side is well-behaved so the failure is unambiguously the review leg.
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchComments("org/repo", 1)
	if err == nil {
		t.Fatalf("expected error from mid-pagination 500, got nil and %d comments", len(got))
	}
	if !strings.Contains(err.Error(), "fetch review comments") {
		t.Errorf("expected error to mention 'fetch review comments', got: %v", err)
	}
}

// TestFetchComments_MidPaginationErrorOnIssuesPropagates is the symmetric
// guard for the issue-comments leg. Same contract: mid-pagination failure
// must NOT return a partial slice.
func TestFetchComments_MidPaginationErrorOnIssuesPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := parsePageParam(t, r)
		switch r.URL.Path {
		case "/repos/org/repo/pulls/1/comments":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/repos/org/repo/issues/1/comments":
			if page == 1 {
				emitIssueCommentsPage(w, page, 100, 0, "issue")
				return
			}
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchIssueCommentsOnly("org/repo", 1)
	if err == nil {
		t.Fatalf("expected error from mid-pagination 500, got nil and %d comments", len(got))
	}
	if !strings.Contains(err.Error(), "fetch issue comments") {
		t.Errorf("expected error to mention 'fetch issue comments', got: %v", err)
	}
}

// TestFetchComments_PaginationCapReturnsError locks in the contract that
// hitting maxPaginationPages with every page full surfaces a hard error
// rather than a silently-truncated slice. Silent partial results here
// would re-introduce the exact failure mode #512 exists to prevent
// (dropping the newest comments) and would be inconsistent with the
// mid-pagination 500 contract that TestFetchComments_MidPaginationError*
// already enforce. Also asserts the production code does NOT walk past
// the cap (no 51st request).
func TestFetchComments_PaginationCapReturnsError(t *testing.T) {
	t.Run("review_comments", func(t *testing.T) {
		var reviewPages int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page := parsePageParam(t, r)
			switch r.URL.Path {
			case "/repos/org/repo/pulls/1/comments":
				atomic.AddInt32(&reviewPages, 1)
				// Always emit a full page so the loop never short-circuits
				// on len<perPage and is forced to hit the cap.
				emitReviewCommentsPage(w, page, 100, 100, "review")
			case "/repos/org/repo/issues/1/comments":
				_ = json.NewEncoder(w).Encode([]map[string]any{})
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
		got, err := client.FetchComments("org/repo", 1)
		if err == nil {
			t.Fatalf("expected pagination cap error, got nil and %d comments", len(got))
		}
		if !strings.Contains(err.Error(), "comment pagination exceeded") {
			t.Errorf("expected error to mention 'comment pagination exceeded', got: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil slice on cap-hit error, got %d comments", len(got))
		}
		// The loop runs `for page := 1; page <= maxPaginationPages; page++`,
		// so on cap-hit exactly maxPaginationPages (50) requests should be
		// made and no 51st request should be attempted.
		if got, want := atomic.LoadInt32(&reviewPages), int32(50); got != want {
			t.Errorf("review endpoint hit count: got %d, want %d (must not walk past cap)", got, want)
		}
	})

	t.Run("issue_comments", func(t *testing.T) {
		var issuePages int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page := parsePageParam(t, r)
			switch r.URL.Path {
			case "/repos/org/repo/pulls/1/comments":
				_ = json.NewEncoder(w).Encode([]map[string]any{})
			case "/repos/org/repo/issues/1/comments":
				atomic.AddInt32(&issuePages, 1)
				emitIssueCommentsPage(w, page, 100, 100, "issue")
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
		got, err := client.FetchIssueCommentsOnly("org/repo", 1)
		if err == nil {
			t.Fatalf("expected pagination cap error, got nil and %d comments", len(got))
		}
		if !strings.Contains(err.Error(), "comment pagination exceeded") {
			t.Errorf("expected error to mention 'comment pagination exceeded', got: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil slice on cap-hit error, got %d comments", len(got))
		}
		if got, want := atomic.LoadInt32(&issuePages), int32(50); got != want {
			t.Errorf("issue endpoint hit count: got %d, want %d (must not walk past cap)", got, want)
		}
	})
}

// parsePageParam reads ?page=N from r and returns it, defaulting to 1 when
// the param is absent. A malformed value is a test bug — the production
// code only ever emits integers — so we fail loudly via t.Fatalf rather
// than silently coercing to 1 and masking the mistake.
func parsePageParam(t *testing.T, r *http.Request) int {
	t.Helper()
	raw := r.URL.Query().Get("page")
	if raw == "" {
		return 1
	}
	page := 0
	if _, err := fmt.Sscanf(raw, "%d", &page); err != nil {
		t.Fatalf("test bug: malformed page param %q on %s: %v", raw, r.URL.Path, err)
	}
	return page
}

// emitReviewCommentsPage writes a JSON page of /pulls/:n/comments shaped
// items. Page 1 emits fullSize items; subsequent pages emit tailSize and
// signal end-of-pagination by being short. Each body is tagged "<tag>-<page>-<index>"
// so tests can assert specific entries survived pagination.
func emitReviewCommentsPage(w http.ResponseWriter, page, fullSize, tailSize int, tag string) {
	n := fullSize
	if page > 1 {
		n = tailSize
	}
	raw := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		raw = append(raw, map[string]any{
			"user":          map[string]string{"login": fmt.Sprintf("u%d", i)},
			"body":          fmt.Sprintf("%s-%d-%d", tag, page, i),
			"created_at":    "2024-01-01T00:00:00Z",
			"path":          "main.go",
			"original_line": i,
		})
	}
	_ = json.NewEncoder(w).Encode(raw)
}

// emitIssueCommentsPage is the issue-comments counterpart: no file/line
// fields, since /issues/:n/comments doesn't return them.
func emitIssueCommentsPage(w http.ResponseWriter, page, fullSize, tailSize int, tag string) {
	n := fullSize
	if page > 1 {
		n = tailSize
	}
	raw := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		raw = append(raw, map[string]any{
			"user":       map[string]string{"login": fmt.Sprintf("u%d", i)},
			"body":       fmt.Sprintf("%s-%d-%d", tag, page, i),
			"created_at": "2024-01-01T00:00:00Z",
		})
	}
	_ = json.NewEncoder(w).Encode(raw)
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

// TestGetPRTimelineEventsForReviewer_FiltersByLogin locks in the
// behaviour the SHA-skip bypass in #322 Bug 5 depends on: the method
// must only return events that target the given reviewer login. Mixed
// payload exercises (a) a review_requested for the bot, (b) a
// review_requested for a different user (must be ignored), (c) a
// review_dismissed for the bot, and (d) an unrelated event type
// (commented).
func TestGetPRTimelineEventsForReviewer_FiltersByLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/issues/7/timeline" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"event":      "review_requested",
				"created_at": "2026-04-24T07:00:00Z",
				"actor":      map[string]string{"login": "alice"},
				"requested_reviewer": map[string]string{"login": "heimdallm-bot"},
			},
			{
				"event":      "review_requested",
				"created_at": "2026-04-24T07:01:00Z",
				"actor":      map[string]string{"login": "alice"},
				"requested_reviewer": map[string]string{"login": "someone-else"},
			},
			{
				"event":      "review_dismissed",
				"created_at": "2026-04-24T07:02:00Z",
				"actor":      map[string]string{"login": "alice"},
				"dismissed_review": map[string]any{
					"user": map[string]string{"login": "heimdallm-bot"},
				},
			},
			{
				"event":      "commented",
				"created_at": "2026-04-24T07:03:00Z",
				"actor":      map[string]string{"login": "alice"},
			},
		})
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	events, err := client.GetPRTimelineEventsForReviewer("org/repo", 7, "heimdallm-bot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events for heimdallm-bot, got %d: %+v", len(events), events)
	}
	if events[0].Event != "review_requested" || !events[0].CreatedAt.Equal(mustTime("2026-04-24T07:00:00Z")) {
		t.Errorf("event[0] mismatch: %+v", events[0])
	}
	if events[1].Event != "review_dismissed" || !events[1].CreatedAt.Equal(mustTime("2026-04-24T07:02:00Z")) {
		t.Errorf("event[1] mismatch: %+v", events[1])
	}
}

// TestGetPRTimelineEventsForReviewer_RejectsEmptyLogin guards against
// callers that forget to set the bot login: without a target login the
// filter would let through every review_requested / review_dismissed in
// the timeline, defeating the point.
func TestGetPRTimelineEventsForReviewer_RejectsEmptyLogin(t *testing.T) {
	client := gh.NewClient("fake-token", gh.WithBaseURL("http://invalid"))
	_, err := client.GetPRTimelineEventsForReviewer("org/repo", 7, "")
	if err == nil {
		t.Fatal("expected error on empty login, got nil")
	}
}

// TestGetPRTimelineEventsForReviewer_PaginationCapReturnsError mirrors
// TestFetchComments_PaginationCapReturnsError for the timeline endpoint
// (#519): hitting the pagination cap with every page full must surface a
// hard error rather than a silently-truncated slice. The tail events are
// exactly what pipeline.shouldBypassSHASkipForReReview walks backward to
// detect re-review intent (#322 Bug 5), so silent truncation here could
// skip a review the operator explicitly asked for. The caller already
// fails closed on error, so the contract change is safe. Also asserts
// the production code does NOT walk past the cap (no 51st request).
func TestGetPRTimelineEventsForReviewer_PaginationCapReturnsError(t *testing.T) {
	var pages int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/issues/7/timeline" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&pages, 1)
		// Always emit a full page so the loop never short-circuits on
		// len<perPage and is forced to hit the cap.
		emitTimelinePage(w, 100, "heimdallm-bot")
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.GetPRTimelineEventsForReviewer("org/repo", 7, "heimdallm-bot")
	if err == nil {
		t.Fatalf("expected pagination cap error, got nil and %d events", len(got))
	}
	if !strings.Contains(err.Error(), "timeline pagination exceeded") {
		t.Errorf("expected error to mention 'timeline pagination exceeded', got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil slice on cap-hit error, got %d events", len(got))
	}
	// The loop runs `for page := 1; page <= maxPaginationPages; page++`,
	// so on cap-hit exactly maxPaginationPages (50) requests should be
	// made and no 51st request should be attempted.
	if got, want := atomic.LoadInt32(&pages), int32(50); got != want {
		t.Errorf("timeline endpoint hit count: got %d, want %d (must not walk past cap)", got, want)
	}
}

// emitTimelinePage writes a JSON page of /issues/:n/timeline shaped items:
// n review_requested events targeting the given reviewer login, so the
// production filter keeps them and the raw page length drives the
// short-page termination check.
func emitTimelinePage(w http.ResponseWriter, n int, login string) {
	raw := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		raw = append(raw, map[string]any{
			"event":              "review_requested",
			"created_at":         "2026-04-24T07:00:00Z",
			"actor":              map[string]string{"login": "alice"},
			"requested_reviewer": map[string]string{"login": login},
		})
	}
	_ = json.NewEncoder(w).Encode(raw)
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestSubmitReview_LockedPRReturnsPermanentSubmitError locks in the
// fix from theburrowhub/heimdallm#325: when GitHub returns 422 with a
// "lock prevents review" body, the daemon must surface a typed
// *PermanentSubmitError so PublishPending can mark the row orphan
// instead of retrying every poll cycle forever.
func TestSubmitReview_LockedPRReturnsPermanentSubmitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/pulls/1/reviews" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"unprocessable","message":"lock prevents review"}]}`))
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	_, _, err := client.SubmitReview("org/repo", 1, "body", "COMMENT")
	if err == nil {
		t.Fatal("expected PermanentSubmitError, got nil")
	}
	var permErr *gh.PermanentSubmitError
	if !errors.As(err, &permErr) {
		t.Fatalf("expected *PermanentSubmitError, got %T: %v", err, err)
	}
	if permErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want 422", permErr.StatusCode)
	}
	if permErr.Reason != "pr_locked" {
		t.Errorf("Reason = %q, want pr_locked", permErr.Reason)
	}
	if permErr.Body == "" {
		t.Errorf("Body should carry the truncated response for diagnostics, got empty")
	}
}

// TestSubmitReview_TransientErrorIsNotPermanent guards against
// over-classification: a 5xx (or any non-422 status) MUST keep the
// generic-error path so the retry loop still runs. Otherwise a
// transient outage would wipe legitimate reviews.
func TestSubmitReview_TransientErrorIsNotPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"upstream"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	_, _, err := client.SubmitReview("org/repo", 1, "body", "COMMENT")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var permErr *gh.PermanentSubmitError
	if errors.As(err, &permErr) {
		t.Errorf("503 must NOT classify as PermanentSubmitError, got %+v", permErr)
	}
}

// TestSubmitReview_422WithoutLockIsNotPermanent ensures we don't
// classify every 422 as permanent — only the specific lock-related
// substrings. A 422 from a malformed body or wrong event value should
// still surface as a generic error so callers can iterate.
func TestSubmitReview_422WithoutLockIsNotPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Validation Failed","errors":[{"code":"invalid","field":"event"}]}`))
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	_, _, err := client.SubmitReview("org/repo", 1, "body", "BAD_EVENT")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var permErr *gh.PermanentSubmitError
	if errors.As(err, &permErr) {
		t.Errorf("422 without lock body must NOT classify as PermanentSubmitError, got %+v", permErr)
	}
}

// TestSubmitReview_LockedPRBodyIsNotTruncated covers the
// post-review-feedback fix to #325: when SubmitReview returns a
// *PermanentSubmitError, the Body field must carry the FULL response
// body (not safe-truncated) so an operator inspecting the error can
// see the complete GitHub payload — the lock substring may live past
// the maxErrBodyLen cutoff used by the generic-error path.
func TestSubmitReview_LockedPRBodyIsNotTruncated(t *testing.T) {
	// Build a body where the lock substring sits well past 200 bytes
	// (maxErrBodyLen) so a truncation regression would lose it.
	padding := strings.Repeat("x", 500)
	bigBody := `{"message":"Validation Failed","padding":"` + padding + `","errors":[{"message":"lock prevents review"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(bigBody))
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	_, _, err := client.SubmitReview("org/repo", 1, "body", "COMMENT")
	var permErr *gh.PermanentSubmitError
	if !errors.As(err, &permErr) {
		t.Fatalf("expected PermanentSubmitError on big locked body, got %v", err)
	}
	if !strings.Contains(permErr.Body, "lock prevents review") {
		t.Errorf("permErr.Body lost the lock substring (truncated at boundary?); body=%q", permErr.Body)
	}
}
