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
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 42, "id": 12345, "html_url": "https://github.com/org/repo/pull/42"})
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	created, err := client.CreatePR("org/repo", "feat: fix", "Closes #7", "heimdallm/issue-7", "main", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.Number != 42 {
		t.Errorf("Number = %d, want 42", created.Number)
	}
	if created.ID != 12345 {
		t.Errorf("ID = %d, want 12345", created.ID)
	}
	if created.HTMLURL != "https://github.com/org/repo/pull/42" {
		t.Errorf("HTMLURL = %q, want https://github.com/org/repo/pull/42", created.HTMLURL)
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
	created, err := client.CreatePR("org/repo", "draft PR", "body", "branch", "main", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.Number != 99 {
		t.Errorf("Number = %d, want 99", created.Number)
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

// ── RemoveLabels ──────────────────────────────────────────────────────────────

func TestRemoveLabels(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	if err := c.RemoveLabels("org/repo", 42, []string{"blocked", "heimdallm-queued"}); err != nil {
		t.Fatalf("RemoveLabels: %v", err)
	}
	// GitHub requires one DELETE per label (no bulk endpoint).
	want := []string{
		"DELETE /repos/org/repo/issues/42/labels/blocked",
		"DELETE /repos/org/repo/issues/42/labels/heimdallm-queued",
	}
	if len(calls) != len(want) {
		t.Fatalf("expected %d calls, got %d: %v", len(want), len(calls), calls)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("call %d = %q, want %q", i, c, want[i])
		}
	}
}

func TestRemoveLabels_Noop(t *testing.T) {
	c := gh.NewClient("fake-token")
	if err := c.RemoveLabels("org/repo", 42, nil); err != nil {
		t.Fatalf("expected nil for empty labels, got: %v", err)
	}
}

func TestRemoveLabels_MissingLabelTolerated(t *testing.T) {
	// GitHub returns 404 when the label is not on the issue. Trying to
	// remove a label that was never applied should not fail the whole
	// promotion — it's the desired end state.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Label does not exist"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	if err := c.RemoveLabels("org/repo", 42, []string{"phantom"}); err != nil {
		t.Errorf("expected nil on 404, got: %v", err)
	}
}

func TestRemoveLabels_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	if err := c.RemoveLabels("org/repo", 42, []string{"blocked"}); err == nil {
		t.Fatal("expected error on 5xx, got nil")
	}
}

// ── GetIssue ─────────────────────────────────────────────────────────────────

func TestGetIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/repos/org/repo/issues/42" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"number":42,"state":"closed","pull_request":null}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := c.GetIssue("org/repo", 42)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Number != 42 {
		t.Errorf("Number = %d, want 42", got.Number)
	}
	if got.State != "closed" {
		t.Errorf("State = %q, want closed", got.State)
	}
}

func TestGetIssue_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	if _, err := c.GetIssue("org/repo", 42); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}

// ── ListSubIssues ─────────────────────────────────────────────────────────────

func TestListSubIssues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/repos/org/repo/issues/10/sub_issues" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"number": 1, "state": "closed", "title": "first child"},
			{"number": 2, "state": "open",   "title": "second child"}
		]`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := c.ListSubIssues("org/repo", 10)
	if err != nil {
		t.Fatalf("ListSubIssues: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Number != 1 || got[0].State != "closed" {
		t.Errorf("child[0] = %+v, want number=1 state=closed", got[0])
	}
	if got[1].Number != 2 || got[1].State != "open" {
		t.Errorf("child[1] = %+v, want number=2 state=open", got[1])
	}
	// Repo must be resolved so downstream consumers can use IssueRef keys.
	if got[0].Repo != "org/repo" {
		t.Errorf("Repo = %q, want org/repo", got[0].Repo)
	}
}

func TestListSubIssues_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := c.ListSubIssues("org/repo", 10)
	if err != nil {
		t.Fatalf("ListSubIssues: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

func TestListSubIssues_NotFound(t *testing.T) {
	// If the parent issue doesn't exist, the endpoint 404s. That's a real
	// error, not an empty list — surface it so the caller logs and the
	// next cycle retries.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	if _, err := c.ListSubIssues("org/repo", 10); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}

func TestListSubIssues_CrossRepoSameOwner(t *testing.T) {
	// GitHub's sub-issues endpoint returns the child issue with a
	// `repository.full_name` that may differ from the parent's repo (same
	// owner, different repo). The client must resolve Repo to the actual
	// child repo, not the parent's, so the promoter keys refs correctly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"number": 5, "state": "closed", "repository": {"full_name": "org/other-repo"}}
		]`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := c.ListSubIssues("org/parent-repo", 10)
	if err != nil {
		t.Fatalf("ListSubIssues: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Repo != "org/other-repo" {
		t.Errorf("Repo = %q, want org/other-repo (cross-repo same-owner sub-issue)", got[0].Repo)
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
