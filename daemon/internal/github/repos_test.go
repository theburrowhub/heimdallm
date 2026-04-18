package github_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// ── GetDefaultBranch ─────────────────────────────────────────────────────────

func TestGetDefaultBranch_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"default_branch": "main",
			"name":           "repo",
		})
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	got, err := client.GetDefaultBranch("org/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "main" {
		t.Errorf("got %q, want main", got)
	}
}

func TestGetDefaultBranch_EmptyRepo(t *testing.T) {
	client := gh.NewClient("fake")
	if _, err := client.GetDefaultBranch(""); err == nil {
		t.Fatal("expected error for empty repo")
	}
}

func TestGetDefaultBranch_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := client.GetDefaultBranch("org/repo")
	if err == nil {
		t.Fatal("expected 404 to surface")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestGetDefaultBranch_EmptyBranchInResponseRejected(t *testing.T) {
	// An empty default_branch in the API response is a bug upstream; the
	// pipeline must not fall back to "" silently (would create heimdallm/issue-N
	// against the zero branch and push would fail opaquely).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": ""})
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if _, err := client.GetDefaultBranch("org/repo"); err == nil {
		t.Fatal("expected error for empty default_branch")
	}
}

// ── CreatePR ─────────────────────────────────────────────────────────────────

func TestCreatePR_Success(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/org/repo/pulls" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 42})
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	num, err := client.CreatePR("org/repo", "feat: fix", "Closes #7", "heimdallm/issue-7", "main", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if num != 42 {
		t.Errorf("got %d, want 42", num)
	}

	// Verify the wire payload is what the auto_implement pipeline expects.
	if capturedBody["title"] != "feat: fix" {
		t.Errorf("title mismatch: %v", capturedBody["title"])
	}
	if capturedBody["head"] != "heimdallm/issue-7" {
		t.Errorf("head mismatch: %v", capturedBody["head"])
	}
	if capturedBody["base"] != "main" {
		t.Errorf("base mismatch: %v", capturedBody["base"])
	}
	if capturedBody["body"] != "Closes #7" {
		t.Errorf("body mismatch: %v", capturedBody["body"])
	}
}

func TestCreatePR_Draft(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 99})
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	num, err := client.CreatePR("org/repo", "draft PR", "body", "branch", "main", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if num != 99 {
		t.Errorf("got %d, want 99", num)
	}
	draft, ok := capturedBody["draft"].(bool)
	if !ok || !draft {
		t.Errorf("expected draft=true in request body, got %v", capturedBody["draft"])
	}
}

func TestCreatePR_MissingFields(t *testing.T) {
	client := gh.NewClient("fake")
	cases := []struct{ repo, title, head, base string }{
		{"", "t", "h", "b"},
		{"org/repo", "", "h", "b"},
		{"org/repo", "t", "", "b"},
		{"org/repo", "t", "h", ""},
	}
	for _, c := range cases {
		if _, err := client.CreatePR(c.repo, c.title, "body", c.head, c.base, false); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func TestCreatePR_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Validation Failed","errors":[{"message":"No commits between main and heimdallm/issue-7"}]}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := client.CreatePR("org/repo", "t", "b", "h", "m", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("error should mention status, got: %v", err)
	}
}

func TestCreatePR_MissingNumberInResponse(t *testing.T) {
	// If the API returns 201 but no `number`, the caller cannot persist
	// pr_created — treat as an error rather than pretending PR #0 exists.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 999})
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if _, err := client.CreatePR("org/repo", "t", "b", "h", "m", false); err == nil {
		t.Fatal("expected error when response has no number")
	}
}

// ── SetPRReviewers ────────────────────────────────────────────────────────────

func TestSetPRReviewers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/repos/org/repo/pulls/42/requested_reviewers" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string][]string
		json.NewDecoder(r.Body).Decode(&body)
		if len(body["reviewers"]) != 2 {
			t.Errorf("expected 2 reviewers, got %v", body["reviewers"])
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	err := c.SetPRReviewers("org/repo", 42, []string{"user1", "user2"})
	if err != nil {
		t.Fatalf("SetPRReviewers: %v", err)
	}
}

func TestSetPRReviewers_Noop(t *testing.T) {
	c := gh.NewClient("fake-token")
	// Should return nil without making any HTTP call
	if err := c.SetPRReviewers("org/repo", 42, nil); err != nil {
		t.Fatalf("expected nil for empty reviewers, got: %v", err)
	}
}

func TestSetPRReviewers_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Validation Failed"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	err := c.SetPRReviewers("org/repo", 42, []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for 422 response")
	}
}

// ── AddLabels ─────────────────────────────────────────────────────────────────

func TestAddLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/repos/org/repo/issues/42/labels" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	err := c.AddLabels("org/repo", 42, []string{"bug", "auto-generated"})
	if err != nil {
		t.Fatalf("AddLabels: %v", err)
	}
}

func TestAddLabels_Noop(t *testing.T) {
	c := gh.NewClient("fake-token")
	if err := c.AddLabels("org/repo", 42, nil); err != nil {
		t.Fatalf("expected nil for empty labels, got: %v", err)
	}
}

func TestAddLabels_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	err := c.AddLabels("org/repo", 42, []string{"nonexistent-label"})
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

// ── SetAssignees ──────────────────────────────────────────────────────────────

func TestSetAssignees(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/repos/org/repo/issues/42/assignees" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	err := c.SetAssignees("org/repo", 42, []string{"sergiotejon"})
	if err != nil {
		t.Fatalf("SetAssignees: %v", err)
	}
}

func TestSetAssignees_Noop(t *testing.T) {
	c := gh.NewClient("fake-token")
	if err := c.SetAssignees("org/repo", 42, nil); err != nil {
		t.Fatalf("expected nil for empty assignees, got: %v", err)
	}
}

func TestSetAssignees_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Validation Failed"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	err := c.SetAssignees("org/repo", 42, []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for 422 response")
	}
}
