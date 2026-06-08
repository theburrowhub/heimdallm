package github_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestGetPRSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/pulls/7" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"state":"open",
			"draft":true,
			"user":{"login":"alice"},
			"updated_at":"2026-04-22T10:00:00Z",
			"head":{"sha":"deadbeef"}
		}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	snap, err := c.GetPRSnapshot("org/repo", 7)
	if err != nil {
		t.Fatalf("GetPRSnapshot: %v", err)
	}
	if snap.State != "open" || !snap.Draft || snap.Author != "alice" || snap.HeadSHA != "deadbeef" {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestGetPRHeadInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/pulls/42" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"head":{"sha":"abc123"},
			"requested_reviewers":[
				{"login":"heimdallm-bot"},
				{"login":"Alice"}
			]
		}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	info, err := c.GetPRHeadInfo("org/repo", 42)
	if err != nil {
		t.Fatalf("GetPRHeadInfo: %v", err)
	}
	if info.HeadSHA != "abc123" {
		t.Errorf("HeadSHA = %q, want abc123", info.HeadSHA)
	}
	if !info.ReviewRequestedFor("heimdallm-bot") {
		t.Error("ReviewRequestedFor(heimdallm-bot) = false, want true")
	}
	if !info.ReviewRequestedFor("ALICE") {
		t.Error("ReviewRequestedFor(ALICE) = false, want true (case-insensitive)")
	}
	if info.ReviewRequestedFor("bob") {
		t.Error("ReviewRequestedFor(bob) = true, want false")
	}
}

func TestGetPRHeadInfo_EmptyReviewers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"head":{"sha":"def456"},
			"requested_reviewers":[]
		}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	info, err := c.GetPRHeadInfo("org/repo", 1)
	if err != nil {
		t.Fatalf("GetPRHeadInfo: %v", err)
	}
	if info.HeadSHA != "def456" {
		t.Errorf("HeadSHA = %q, want def456", info.HeadSHA)
	}
	if info.ReviewRequestedFor("anyone") {
		t.Error("empty requested_reviewers must return false for any login")
	}
}
