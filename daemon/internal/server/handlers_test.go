package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

func setupServer(t *testing.T) (*server.Server, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.New(s, broker, nil, "")
	return srv, s
}

func TestHandlerHealth(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("health: status %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("health body: %v", body)
	}
}

func TestHandlerListPRs(t *testing.T) {
	srv, s := setupServer(t)
	s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})

	req := httptest.NewRequest("GET", "/prs", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("list prs: status %d", w.Code)
	}
	var prs []map[string]any
	json.NewDecoder(w.Body).Decode(&prs)
	if len(prs) != 1 {
		t.Errorf("expected 1 PR, got %d", len(prs))
	}
}

func TestHandlerGetPR(t *testing.T) {
	srv, s := setupServer(t)
	id, _ := s.UpsertPR(&store.PR{GithubID: 2, Repo: "org/r", Number: 2, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})

	req := httptest.NewRequest("GET", "/prs/"+itoa(id), nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("get pr: status %d", w.Code)
	}
}

func TestHandlerGetConfig(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("get config: status %d", w.Code)
	}
}

// TestHandlerGetConfig_ExposesRepoFirstSeenAt guards the response shape that
// main.go's configFn produces for auto-discovered repos: each entry in
// repo_overrides gets a first_seen_at Unix-seconds integer enriched from the
// repo_first_seen store row. The Flutter app reads this to show NEW badges,
// so a silent rename or re-nesting would break the UI. The store-reading path
// itself lives in cmd/heimdallm/main.go and is covered at runtime — this test
// pins the JSON contract so that contract cannot drift.
func TestHandlerGetConfig_ExposesRepoFirstSeenAt(t *testing.T) {
	srv, _ := setupServer(t)
	seen := int64(1713571200) // 2024-04-20T00:00:00Z, arbitrary but fixed
	srv.SetConfigFn(func() map[string]any {
		return map[string]any{
			"repo_overrides": map[string]any{
				"acme/api": map[string]any{
					"first_seen_at": seen,
				},
			},
		}
	})

	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, w.Body.String())
	}
	overrides, ok := body["repo_overrides"].(map[string]any)
	if !ok {
		t.Fatalf("repo_overrides missing or wrong type: %T: %v", body["repo_overrides"], body["repo_overrides"])
	}
	entry, ok := overrides["acme/api"].(map[string]any)
	if !ok {
		t.Fatalf("repo_overrides[acme/api] missing or wrong type: %T: %v", overrides["acme/api"], overrides["acme/api"])
	}
	// JSON numbers unmarshal to float64 when decoding into map[string]any.
	got, ok := entry["first_seen_at"].(float64)
	if !ok {
		t.Fatalf("first_seen_at missing or wrong type: %T: %v", entry["first_seen_at"], entry["first_seen_at"])
	}
	if int64(got) != seen {
		t.Errorf("first_seen_at = %d, want %d", int64(got), seen)
	}
}

func TestHandlerPutConfig(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"poll_interval":"5m"}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("put config: status %d, body: %s", w.Code, w.Body.String())
	}
}

func TestHandlerShutdown(t *testing.T) {
	srv, _ := setupServer(t)
	shutdown := make(chan struct{}, 1)
	srv.SetShutdownFn(func() {
		shutdown <- struct{}{}
	})

	req := httptest.NewRequest("POST", "/shutdown", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("shutdown: status %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "shutdown queued" {
		t.Fatalf("status = %q, want shutdown queued", body["status"])
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

func TestHandlerShutdownNotConfigured(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("POST", "/shutdown", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandlerShutdownRequiresAuthWhenTokenSet(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	shutdown := make(chan struct{}, 1)
	srv.SetShutdownFn(func() {
		shutdown <- struct{}{}
	})

	req := httptest.NewRequest("POST", "/shutdown", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /shutdown without token: status = %d, want 401", w.Code)
	}

	req = httptest.NewRequest("POST", "/shutdown", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /shutdown with token: status = %d, want 202", w.Code)
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}

func setupServerWithToken(t *testing.T, token string) *server.Server {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	return server.New(s, broker, nil, token)
}

func TestHandlerLogsStream_RequiresAuth(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	req := httptest.NewRequest("GET", "/logs/stream", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", w.Code)
	}
}

func TestHandlerLogsStream_WithToken(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")

	// Use a context with a short deadline so the polling loop exits.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req := httptest.NewRequest("GET", "/logs/stream", nil).WithContext(ctx)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	// Log file won't exist in CI/test env; endpoint should return 200 with SSE not-found message
	// and exit cleanly. If the log file DOES exist (dev machine), the handler exits when ctx is done.
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}
}

func TestPublicEndpointsRequireAuthWhenTokenSet(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")

	paths := []string{"/me", "/prs", "/stats"}
	for _, path := range paths {
		// Without token → 401
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token: expected 401, got %d", path, w.Code)
		}

		// With valid token → not 401
		req2 := httptest.NewRequest("GET", path, nil)
		req2.Header.Set("X-Heimdallm-Token", "secret-token")
		w2 := httptest.NewRecorder()
		srv.Router().ServeHTTP(w2, req2)
		if w2.Code == http.StatusUnauthorized {
			t.Errorf("GET %s with valid token: expected not-401, got 401", path)
		}
	}
}

func TestHandlerTriggerReviewRateLimit(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	// Server with max 2 concurrent reviews
	srv := server.NewWithOptions(s, broker, nil, "test-token", server.Options{MaxConcurrentReviews: 2})

	// Wire a review function that blocks until gate is closed
	gate := make(chan struct{})
	srv.SetTriggerReviewFn(func(prID int64) error {
		<-gate
		return nil
	})

	// Seed 3 PRs
	now := time.Now()
	for i := 1; i <= 3; i++ {
		s.UpsertPR(&store.PR{
			GithubID: int64(i), Repo: "org/r", Number: i,
			Title: "t", Author: "a", URL: "u", State: "open",
			UpdatedAt: now, FetchedAt: now,
		})
	}

	token := "test-token"

	// Fire 2 concurrent reviews — should succeed
	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest("POST", fmt.Sprintf("/prs/%d/review", i), nil)
		req.Header.Set("X-Heimdallm-Token", token)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Errorf("review %d: expected 202, got %d", i, w.Code)
		}
	}

	// Brief wait for goroutines to acquire semaphore
	time.Sleep(10 * time.Millisecond)

	// Third review should be rejected with 429
	req := httptest.NewRequest("POST", "/prs/3/review", nil)
	req.Header.Set("X-Heimdallm-Token", token)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 when semaphore full, got %d", w.Code)
	}

	// Release goroutines
	close(gate)
}

func TestHandlerListIssues(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, err := s.UpsertIssue(&store.Issue{
		GithubID: 100, Repo: "org/repo", Number: 7, Title: "bug: crash",
		Body: "details", Author: "alice", Assignees: `["bob"]`, Labels: `["bug"]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	s.InsertIssueReview(&store.IssueReview{
		IssueID: id, CLIUsed: "claude", Summary: "triage summary",
		Triage: `{"severity":"high","category":"bug"}`, Suggestions: `["fix it"]`,
		ActionTaken: "review_only", CreatedAt: now,
	})

	req := httptest.NewRequest("GET", "/issues", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list issues: status %d, body: %s", w.Code, w.Body.String())
	}
	var issues []map[string]any
	json.NewDecoder(w.Body).Decode(&issues)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	iss := issues[0]
	if iss["title"] != "bug: crash" {
		t.Errorf("title = %v", iss["title"])
	}
	// Verify assignees/labels are arrays, not strings
	if assignees, ok := iss["assignees"].([]any); !ok || len(assignees) != 1 {
		t.Errorf("assignees should be parsed array, got %T: %v", iss["assignees"], iss["assignees"])
	}
	if labels, ok := iss["labels"].([]any); !ok || len(labels) != 1 {
		t.Errorf("labels should be parsed array, got %T: %v", iss["labels"], iss["labels"])
	}
	// Verify latest_review is attached
	rev, ok := iss["latest_review"].(map[string]any)
	if !ok || rev == nil {
		t.Fatalf("expected latest_review, got %v", iss["latest_review"])
	}
	if rev["summary"] != "triage summary" {
		t.Errorf("review summary = %v", rev["summary"])
	}
	// Verify triage is parsed object, not string
	if _, ok := rev["triage"].(map[string]any); !ok {
		t.Errorf("triage should be parsed object, got %T: %v", rev["triage"], rev["triage"])
	}
}

func TestHandlerGetIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 200, Repo: "org/repo", Number: 8, Title: "feat request",
		Body: "details", Author: "bob", Assignees: `[]`, Labels: `["enhancement"]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	s.InsertIssueReview(&store.IssueReview{
		IssueID: id, CLIUsed: "gemini", Summary: "looks good",
		Triage: `{"severity":"low","category":"feature"}`, Suggestions: `[]`,
		ActionTaken: "review_only", CreatedAt: now,
	})

	req := httptest.NewRequest("GET", "/issues/"+itoa(id), nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get issue: status %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)
	iss, ok := body["issue"].(map[string]any)
	if !ok {
		t.Fatalf("expected issue key")
	}
	if iss["title"] != "feat request" {
		t.Errorf("title = %v", iss["title"])
	}
	reviews, ok := body["reviews"].([]any)
	if !ok || len(reviews) != 1 {
		t.Fatalf("expected 1 review, got %v", body["reviews"])
	}
}

func TestHandlerGetIssue_NotFound(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/issues/9999", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandlerDismissIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 300, Repo: "org/r", Number: 10, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/dismiss", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dismiss issue: status %d, body: %s", w.Code, w.Body.String())
	}

	issues, _ := s.ListIssues()
	if len(issues) != 0 {
		t.Errorf("expected 0 issues after dismiss, got %d", len(issues))
	}
}

func TestHandlerUndismissIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 400, Repo: "org/r", Number: 11, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	s.DismissIssue(id)

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/undismiss", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("undismiss issue: status %d, body: %s", w.Code, w.Body.String())
	}

	issues, _ := s.ListIssues()
	if len(issues) != 1 {
		t.Errorf("expected 1 issue after undismiss, got %d", len(issues))
	}
}

func TestHandlerTriggerIssueReview(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 500, Repo: "org/r", Number: 12, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	triggered := make(chan int64, 1)
	srv.SetTriggerIssueReviewFn(func(issueID int64) error {
		triggered <- issueID
		return nil
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/review", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("trigger issue review: status %d, body: %s", w.Code, w.Body.String())
	}

	select {
	case got := <-triggered:
		if got != id {
			t.Errorf("triggered with issue_id %d, expected %d", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Error("trigger callback not called within 2s")
	}
}

func TestHandlerTriggerIssueReview_NotConfigured(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 600, Repo: "org/r", Number: 13, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/review", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when trigger not configured, got %d", w.Code)
	}
}

func TestHandlerPromoteIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 800, Repo: "org/r", Number: 20, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	promoted := make(chan int64, 1)
	srv.SetTriggerPromoteFn(func(issueID int64) error {
		promoted <- issueID
		return nil
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/promote", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("promote issue: status %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "promote queued" {
		t.Errorf("promote issue: unexpected body %v", body)
	}

	select {
	case got := <-promoted:
		if got != id {
			t.Errorf("promoted with issue_id %d, expected %d", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Error("promote callback not called within 2s")
	}
}

func TestHandlerPromoteIssue_NotConfigured(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 801, Repo: "org/r", Number: 21, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/promote", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when promote not configured, got %d", w.Code)
	}
}

func TestIssueEndpointsRequireAuthWhenTokenSet(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	srv := server.New(s, broker, nil, "secret-token")

	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 700, Repo: "org/r", Number: 14, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	issueID := fmt.Sprintf("%d", id)

	// GET endpoints — protected via sensitiveGETPaths
	getPaths := []string{"/issues", "/issues/" + issueID}
	for _, path := range getPaths {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token: expected 401, got %d", path, w.Code)
		}

		req2 := httptest.NewRequest("GET", path, nil)
		req2.Header.Set("X-Heimdallm-Token", "secret-token")
		w2 := httptest.NewRecorder()
		srv.Router().ServeHTTP(w2, req2)
		if w2.Code == http.StatusUnauthorized {
			t.Errorf("GET %s with valid token: unexpected 401", path)
		}
	}

	// POST endpoints — protected via method-based auth (all POST requires token)
	postPaths := []string{
		"/issues/" + issueID + "/review",
		"/issues/" + issueID + "/promote",
		"/issues/" + issueID + "/dismiss",
		"/issues/" + issueID + "/undismiss",
	}
	for _, path := range postPaths {
		req := httptest.NewRequest("POST", path, nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without token: expected 401, got %d", path, w.Code)
		}

		req2 := httptest.NewRequest("POST", path, nil)
		req2.Header.Set("X-Heimdallm-Token", "secret-token")
		w2 := httptest.NewRecorder()
		srv.Router().ServeHTTP(w2, req2)
		if w2.Code == http.StatusUnauthorized {
			t.Errorf("POST %s with valid token: unexpected 401", path)
		}
	}
}

func TestHandlerPutConfig_IssueTracking_Accepted(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"issue_tracking":{"enabled":true,"filter_mode":"exclusive","default_action":"ignore","develop_labels":["feature","bug"],"review_only_labels":["question"],"skip_labels":["wontfix"],"organizations":[],"assignees":[]}}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_IssueTracking_InvalidFilterMode(t *testing.T) {
	srv, _ := setupServer(t)
	// filter_mode "weird" with enabled=true should trip validateIssueTracking.
	body := `{"issue_tracking":{"enabled":true,"filter_mode":"weird","default_action":"ignore"}}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_IssueTracking_InvalidDefaultAction(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"issue_tracking":{"enabled":true,"filter_mode":"exclusive","default_action":"delete_the_repo"}}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_IssueTracking_PersistsAndIsReadable(t *testing.T) {
	// End-to-end: PUT → ListConfigs → ApplyStore → cfg reflects the change.
	// This is the scenario that is broken on main today and the reason the
	// web UI's "Save & reload" silently loses values on refresh.
	srv, s := setupServer(t)
	body := `{"issue_tracking":{"enabled":true,"filter_mode":"inclusive","default_action":"review_only","develop_labels":["feature"]}}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	rows, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	raw, ok := rows["issue_tracking"]
	if !ok {
		t.Fatalf("store: expected issue_tracking row, got keys %v", rows)
	}

	cfg := newCfgWithPrimary()
	cfg.GitHub.PollInterval = "5m"
	if err := cfg.ApplyStore(map[string]string{"issue_tracking": raw}); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate after ApplyStore: %v", err)
	}
	it := cfg.GitHub.IssueTracking
	if !it.Enabled || it.FilterMode != config.FilterModeInclusive || it.DefaultAction != "review_only" {
		t.Errorf("round-trip: got %+v", it)
	}
	if len(it.DevelopLabels) != 1 || it.DevelopLabels[0] != "feature" {
		t.Errorf("DevelopLabels = %v, want [feature]", it.DevelopLabels)
	}
}

// newCfgWithPrimary builds a minimal valid Config for tests that want to run
// cfg.Validate() after ApplyStore (Validate requires ai.primary).
func newCfgWithPrimary() *config.Config {
	c := &config.Config{}
	c.AI.Primary = "claude"
	return c
}

// ── read-only round-tripping (#86) ─────────────────────────────────────────

func putConfigRequest(body string) *http.Request {
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestHandlerPutConfig_ReadOnlyKeys_Accepted(t *testing.T) {
	// The web UI round-trips these fields verbatim from GET /config as a
	// forward-compat safeguard; the handler must accept them (no 400) even
	// though it won't persist them.
	cases := []struct {
		name string
		body string
	}{
		{"non_monitored", `{"non_monitored":["org/archived"]}`},
		{"repo_overrides", `{"repo_overrides":{"org/a":{"primary":"claude"}}}`},
		{"agent_configs", `{"agent_configs":{"claude":{"model":"claude-opus-4-7"}}}`},
		{"server_port", `{"server_port":7842}`},
		{"all-at-once", `{"non_monitored":[],"repo_overrides":{},"agent_configs":{},"server_port":7842}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := setupServer(t)
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, putConfigRequest(tc.body))
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandlerPutConfig_ReadOnlyKeys_NotPersisted(t *testing.T) {
	// Accepted but never written: the configs table must stay untouched so
	// reload doesn't have to re-examine them and ApplyStore's
	// "unknown/bootstrap-only" branches stay dormant.
	srv, s := setupServer(t)
	body := `{"non_monitored":["org/x"],"repo_overrides":{"org/a":{"primary":"claude"}},"agent_configs":{"claude":{"model":"x"}},"server_port":7842}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	rows, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	for _, banned := range []string{"non_monitored", "repo_overrides", "agent_configs", "server_port"} {
		if _, leaked := rows[banned]; leaked {
			t.Errorf("read-only key %q was persisted (rows: %v)", banned, rows)
		}
	}
}

func TestHandlerPutConfig_WritableAndReadOnly_Mixed(t *testing.T) {
	// A realistic save: the Svelte UI sends editable fields next to the
	// round-tripped read-only ones. Only the editable fields land in the
	// store — nothing else bleeds in.
	srv, s := setupServer(t)
	body := `{"poll_interval":"30m","agent_configs":{"claude":{"model":"x"}},"non_monitored":["org/x"]}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	rows, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	if rows["poll_interval"] != "30m" {
		t.Errorf("poll_interval = %q, want 30m", rows["poll_interval"])
	}
	if _, ok := rows["agent_configs"]; ok {
		t.Errorf("agent_configs unexpectedly persisted")
	}
	if _, ok := rows["non_monitored"]; ok {
		t.Errorf("non_monitored unexpectedly persisted")
	}
}

func TestHandlerPutConfig_UnknownKey_StillRejected(t *testing.T) {
	// Security regression guard: the read-only escape hatch must NOT become
	// a catch-all. Keys that are neither writable nor read-only still 400.
	srv, _ := setupServer(t)
	body := `{"not_a_real_key":"whatever"}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown key, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_ServerPort_RangeStillValidated(t *testing.T) {
	// server_port moves from writable to read-only, but its numeric-range
	// pre-check (handlers.go:362) still fires — a client sending a
	// privileged port is still rejected so the bug-class "validate before
	// accept" stays intact even for read-only keys.
	srv, _ := setupServer(t)
	body := `{"server_port":80}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for port 80, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandleActivity_DefaultsToToday(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	yesterday := now.Add(-26 * time.Hour)
	if _, err := s.InsertActivity(yesterday, "acme", "acme/api", "pr", 1, "Old", "review", "minor", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertActivity(now, "acme", "acme/api", "pr", 2, "New", "review", "major", map[string]any{"cli_used": "claude"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/activity", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct {
			Repo    string         `json:"repo"`
			Action  string         `json:"action"`
			Outcome string         `json:"outcome"`
			Details map[string]any `json:"details"`
		} `json:"entries"`
		Count     int  `json:"count"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("want 1 entry (today only), got %d", len(resp.Entries))
	}
	if resp.Entries[0].Outcome != "major" {
		t.Errorf("outcome = %q", resp.Entries[0].Outcome)
	}
	if resp.Entries[0].Details["cli_used"] != "claude" {
		t.Errorf("details: %+v", resp.Entries[0].Details)
	}
}

func TestHandleActivity_ExplicitDate(t *testing.T) {
	srv, s := setupServer(t)
	loc := time.Now().Location()
	day := time.Date(2026, 4, 18, 12, 0, 0, 0, loc)
	if _, err := s.InsertActivity(day, "acme", "acme/api", "pr", 1, "Old", "review", "minor", nil); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/activity?date=2026-04-18", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Errorf("count = %d, want 1", resp.Count)
	}
}

func TestHandleActivity_BadDateFormat(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/activity?date=2026/04/20", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_DateAndRangeMutuallyExclusive(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/activity?date=2026-04-20&from=2026-04-19&to=2026-04-20", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_FromWithoutTo(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/activity?from=2026-04-19", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_ToBeforeFrom(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/activity?from=2026-04-20&to=2026-04-18", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_LimitOutOfRange(t *testing.T) {
	srv, _ := setupServer(t)
	for _, v := range []string{"0", "-1", "5001", "abc"} {
		req := httptest.NewRequest("GET", "/activity?limit="+v, nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("limit=%q: status = %d, want 400", v, w.Code)
		}
	}
}

func TestHandleActivity_DisabledReturns503(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetConfigFn(func() map[string]any {
		return map[string]any{"activity_log_enabled": false}
	})
	req := httptest.NewRequest("GET", "/activity", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleActivity_RequiresAuth(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	req := httptest.NewRequest("GET", "/activity", nil)
	// no token
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleActivity_FilterByRepoAndAction(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	_, _ = s.InsertActivity(now, "acme", "acme/api", "pr", 1, "t", "review", "minor", nil)
	_, _ = s.InsertActivity(now, "acme", "acme/api", "issue", 2, "t", "triage", "major", nil)
	_, _ = s.InsertActivity(now, "globex", "globex/w", "pr", 3, "t", "review", "minor", nil)

	req := httptest.NewRequest("GET", "/activity?repo=acme/api&action=review", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct{ Repo, Action string } `json:"entries"`
		Count   int                             `json:"count"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1", resp.Count)
	}
	if resp.Entries[0].Repo != "acme/api" || resp.Entries[0].Action != "review" {
		t.Errorf("wrong entry: %+v", resp.Entries[0])
	}
}

func TestHandlerPutConfigValueValidation(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "valid poll_interval 5m",
			body:       `{"poll_interval":"5m"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid poll_interval 2m",
			body:       `{"poll_interval":"2m"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid retention_days 90",
			body:       `{"retention_days":90}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "retention_days too high 9999",
			body:       `{"retention_days":9999}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid server_port 8080",
			body:       `{"server_port":8080}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "server_port too low 80",
			body:       `{"server_port":80}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid review_mode single",
			body:       `{"review_mode":"single"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid review_mode batch",
			body:       `{"review_mode":"batch"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/config",
				strings.NewReader(tc.body))
			req.Header.Set("X-Heimdallm-Token", "secret-token")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Errorf("%s: expected %d, got %d (body: %s)",
					tc.name, tc.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleStats_IncludesActivityCount24h(t *testing.T) {
	srv, s := setupServer(t)
	// Insert one recent activity and one old (>24h).
	now := time.Now()
	if _, err := s.InsertActivity(now.Add(-1*time.Hour), "a", "a/b", "pr", 1, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertActivity(now.Add(-30*time.Hour), "a", "a/b", "pr", 2, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/stats", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	v, ok := body["activity_count_24h"]
	if !ok {
		t.Fatalf("activity_count_24h missing from response: %v", body)
	}
	// JSON numbers unmarshal to float64 when decoding into map[string]any.
	got, ok := v.(float64)
	if !ok || got != 1 {
		t.Errorf("activity_count_24h = %v, want 1", v)
	}
}

// writeTempTOML creates a temp config.toml with the given content and returns
// its path. The file is cleaned up by t.Cleanup.
func writeTempTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp TOML: %v", err)
	}
	return path
}

func TestHandlePatchConfig(t *testing.T) {
	// Valid TOML with ai.primary set (required by Config.Validate).
	tomlContent := "[ai]\nprimary = \"claude\"\nfallback = \"gemini\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"ai":{"primary":"openai"}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Read the TOML back and verify primary was updated but fallback preserved.
	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section in TOML, got %v", m)
	}
	if ai["primary"] != "openai" {
		t.Errorf("primary = %v, want openai", ai["primary"])
	}
	if ai["fallback"] != "gemini" {
		t.Errorf("fallback = %v, want gemini (should be preserved)", ai["fallback"])
	}
}

func TestHandlePatchConfig_RejectsNull(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	// Sending null for a field should be rejected with 400.
	body := `{"ai":{"primary":null}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for null value, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlePatchRepoConfig(t *testing.T) {
	// TOML with a global primary and an existing repo override.
	tomlContent := "[ai]\nprimary = \"claude\"\n\n[ai.repos.\"org/repo1\"]\nprimary = \"gemini\"\nfallback = \"openai\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	// PATCH only primary — fallback must be preserved.
	body := `{"primary":"claude-new"}`
	req := httptest.NewRequest("PATCH", "/config/repos/"+url.PathEscape("org/repo1"), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Verify TOML on disk reflects the change.
	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	repos, ok := ai["repos"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos] section, got %v", ai)
	}
	repo1, ok := repos["org/repo1"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos.\"org/repo1\"] section, got %v", repos)
	}
	if repo1["primary"] != "claude-new" {
		t.Errorf("primary = %v, want claude-new", repo1["primary"])
	}
	if repo1["fallback"] != "openai" {
		t.Errorf("fallback = %v, want openai (should be preserved)", repo1["fallback"])
	}
}

func TestHandlePatchRepoConfig_CreatesNewSection(t *testing.T) {
	// TOML with only a global primary — no repo overrides yet.
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"pr_draft":true}`
	req := httptest.NewRequest("PATCH", "/config/repos/"+url.PathEscape("org/new-repo"), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Verify TOML now contains a section for org/new-repo with pr_draft.
	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	repos, ok := ai["repos"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos] section, got %v", ai)
	}
	newRepo, ok := repos["org/new-repo"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos.\"org/new-repo\"] section, got %v", repos)
	}
	if newRepo["pr_draft"] != true {
		t.Errorf("pr_draft = %v, want true", newRepo["pr_draft"])
	}
}

func TestHandleDeleteRepoField_TopLevel(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n\n[ai.repos.\"org/repo1\"]\nprimary = \"gemini\"\npr_draft = true\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	req := httptest.NewRequest("DELETE", "/config/repos/"+url.PathEscape("org/repo1")+"/pr_draft", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after DELETE: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	repos, ok := ai["repos"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos] section, got %v", ai)
	}
	repo1, ok := repos["org/repo1"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos.\"org/repo1\"] section, got %v", repos)
	}
	if _, found := repo1["pr_draft"]; found {
		t.Errorf("pr_draft should have been deleted, still present: %v", repo1)
	}
	if repo1["primary"] != "gemini" {
		t.Errorf("primary = %v, want gemini (should be preserved)", repo1["primary"])
	}
}

func TestHandleDeleteRepoField_NestedPath(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n\n[ai.repos.\"org/repo1\".issue_tracking]\ndevelop_labels = [\"ready\"]\nfilter_mode = \"exclusive\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	req := httptest.NewRequest("DELETE", "/config/repos/"+url.PathEscape("org/repo1")+"/issue_tracking/develop_labels", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after DELETE: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	repos, ok := ai["repos"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos] section, got %v", ai)
	}
	repo1, ok := repos["org/repo1"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos.\"org/repo1\"] section, got %v", repos)
	}
	issueTracking, ok := repo1["issue_tracking"].(map[string]any)
	if !ok {
		t.Fatalf("expected issue_tracking section, got %v", repo1)
	}
	if _, found := issueTracking["develop_labels"]; found {
		t.Errorf("develop_labels should have been deleted, still present: %v", issueTracking)
	}
	if issueTracking["filter_mode"] != "exclusive" {
		t.Errorf("filter_mode = %v, want exclusive (should be preserved)", issueTracking["filter_mode"])
	}
}

func TestHandleDeleteRepoField_Idempotent(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	// DELETE for a non-existent repo/field — should be idempotent and return 200.
	req := httptest.NewRequest("DELETE", "/config/repos/"+url.PathEscape("org/nonexistent")+"/pr_draft", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (idempotent), got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandleListPRs_StateFilter(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.New(s, broker, nil, "test-token")

	now := time.Now()
	s.UpsertPR(&store.PR{GithubID: 10, Repo: "org/r", Number: 10, Title: "open pr", Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now})
	s.UpsertPR(&store.PR{GithubID: 11, Repo: "org/r", Number: 11, Title: "closed pr", Author: "a", URL: "u", State: "closed", UpdatedAt: now, FetchedAt: now})

	doReq := func(path string) []map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Heimdallm-Token", "test-token")
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, body: %s", path, w.Code, w.Body.String())
		}
		var prs []map[string]any
		if err := json.NewDecoder(w.Body).Decode(&prs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return prs
	}

	// No filter → both PRs returned
	if got := doReq("/prs"); len(got) != 2 {
		t.Errorf("GET /prs: expected 2, got %d", len(got))
	}

	// state=open → only the open PR
	if got := doReq("/prs?state=open"); len(got) != 1 {
		t.Errorf("GET /prs?state=open: expected 1, got %d", len(got))
	} else if got[0]["state"] != "open" {
		t.Errorf("GET /prs?state=open: got state %v", got[0]["state"])
	}

	// state=closed → only the closed PR
	if got := doReq("/prs?state=closed"); len(got) != 1 {
		t.Errorf("GET /prs?state=closed: expected 1, got %d", len(got))
	} else if got[0]["state"] != "closed" {
		t.Errorf("GET /prs?state=closed: got state %v", got[0]["state"])
	}

	// state=open,closed → both PRs
	if got := doReq("/prs?state=open,closed"); len(got) != 2 {
		t.Errorf("GET /prs?state=open,closed: expected 2, got %d", len(got))
	}
}

func TestHandleListIssues_StateFilter(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.New(s, broker, nil, "test-token")

	now := time.Now()
	s.UpsertIssue(&store.Issue{
		GithubID: 20, Repo: "org/r", Number: 20, Title: "open issue",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	s.UpsertIssue(&store.Issue{
		GithubID: 21, Repo: "org/r", Number: 21, Title: "closed issue",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "closed", CreatedAt: now, FetchedAt: now,
	})

	doReq := func(path string) []map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Heimdallm-Token", "test-token")
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, body: %s", path, w.Code, w.Body.String())
		}
		var issues []map[string]any
		if err := json.NewDecoder(w.Body).Decode(&issues); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return issues
	}

	// No filter → both issues returned
	if got := doReq("/issues"); len(got) != 2 {
		t.Errorf("GET /issues: expected 2, got %d", len(got))
	}

	// state=open → only the open issue
	if got := doReq("/issues?state=open"); len(got) != 1 {
		t.Errorf("GET /issues?state=open: expected 1, got %d", len(got))
	} else if got[0]["state"] != "open" {
		t.Errorf("GET /issues?state=open: got state %v", got[0]["state"])
	}

	// state=closed → only the closed issue
	if got := doReq("/issues?state=closed"); len(got) != 1 {
		t.Errorf("GET /issues?state=closed: expected 1, got %d", len(got))
	} else if got[0]["state"] != "closed" {
		t.Errorf("GET /issues?state=closed: got state %v", got[0]["state"])
	}

	// state=open,closed → both issues
	if got := doReq("/issues?state=open,closed"); len(got) != 2 {
		t.Errorf("GET /issues?state=open,closed: expected 2, got %d", len(got))
	}
}
