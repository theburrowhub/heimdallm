package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestAddAssignees(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/widget/issues/7/assignees" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Assignees []string `json:"assignees"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if !reflect.DeepEqual(payload.Assignees, []string{"bot"}) {
			t.Fatalf("assignees payload = %v, want [bot]", payload.Assignees)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if err := c.AddAssignees("acme/widget", 7, []string{"bot"}); err != nil {
		t.Fatalf("AddAssignees: %v", err)
	}
}

func TestAddAssignees_NoOp(t *testing.T) {
	// Empty assignees list should be a no-op (no HTTP call).
	c := gh.NewClient("fake", gh.WithBaseURL("http://127.0.0.1:0"))
	if err := c.AddAssignees("acme/widget", 7, nil); err != nil {
		t.Fatalf("AddAssignees with nil: %v", err)
	}
	if err := c.AddAssignees("acme/widget", 7, []string{}); err != nil {
		t.Fatalf("AddAssignees with empty: %v", err)
	}
}

func TestBranchExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/widget/branches/exists" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"exists"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Branch not found"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	ok, err := c.BranchExists("acme/widget", "exists")
	if err != nil || !ok {
		t.Errorf("exists: want ok,nil got %v,%v", ok, err)
	}
	ok, err = c.BranchExists("acme/widget", "missing")
	if err != nil || ok {
		t.Errorf("missing: want false,nil got %v,%v", ok, err)
	}
}

func TestMergePR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/repos/acme/widget/pulls/7/merge" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			MergeMethod string `json:"merge_method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload.MergeMethod != "squash" {
			t.Fatalf("merge_method = %q, want squash", payload.MergeMethod)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"merged":true}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if err := c.MergePR("acme/widget", 7, "squash"); err != nil {
		t.Fatalf("MergePR: %v", err)
	}
}

func TestMergePR_DefaultMethod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			MergeMethod string `json:"merge_method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload.MergeMethod != "squash" {
			t.Fatalf("merge_method = %q, want squash (default)", payload.MergeMethod)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"merged":true}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	// Empty method should default to "squash" without error.
	if err := c.MergePR("acme/widget", 7, ""); err != nil {
		t.Fatalf("MergePR with empty method: %v", err)
	}
}
