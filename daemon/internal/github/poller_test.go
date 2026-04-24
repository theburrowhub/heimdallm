package github_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
