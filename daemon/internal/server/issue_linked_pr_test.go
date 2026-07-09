package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

// TestHandleGetIssue_EmbedsLinkedPRReviewState pins that GET
// /issues/{id} surfaces the external_review_state of the PR auto_implement
// created from this issue (#482 phase 1). Flutter reads this through the
// `linked_pr` field on the response and renders the NEEDS-REVIEW chip.
func TestHandleGetIssue_EmbedsLinkedPRReviewState(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now().UTC().Truncate(time.Second)

	issueID, err := s.UpsertIssue(&store.Issue{
		GithubID: 7001, Repo: "org/repo", Number: 42, Title: "issue",
		Author: "alice", State: "open", CreatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	const prNumber = 99
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 8001, Repo: "org/repo", Number: prNumber,
		Title: "PR", Author: "heimdallm-bot", URL: "https://example/pr/99",
		State: "open", UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if err := s.MarkPRAutoImplementOrigin(prID, issueID); err != nil {
		t.Fatalf("mark origin: %v", err)
	}
	if err := s.UpdatePRReviewState(prID, "CHANGES_REQUESTED", "carol", now); err != nil {
		t.Fatalf("update pr review state: %v", err)
	}

	if _, err := s.InsertIssueReview(&store.IssueReview{
		IssueID: issueID, CLIUsed: "claude", Summary: "auto",
		Triage: "{}", NextSteps: "[]",
		ActionTaken: "auto_implement", PRCreated: prNumber, CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	req := httptest.NewRequest("GET", fmt.Sprintf("/issues/%d", issueID), nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	iss, ok := body["issue"].(map[string]any)
	if !ok {
		t.Fatalf("missing issue object: %v", body)
	}
	linked, ok := iss["linked_pr"].(map[string]any)
	if !ok {
		t.Fatalf("expected linked_pr object on issue, got %T (%v)", iss["linked_pr"], iss["linked_pr"])
	}
	if linked["external_review_state"] != "CHANGES_REQUESTED" {
		t.Errorf("linked_pr.external_review_state = %v, want CHANGES_REQUESTED", linked["external_review_state"])
	}
	if linked["external_reviewer"] != "carol" {
		t.Errorf("linked_pr.external_reviewer = %v, want carol", linked["external_reviewer"])
	}
	if got, want := int(linked["number"].(float64)), prNumber; got != want {
		t.Errorf("linked_pr.number = %d, want %d", got, want)
	}
}

// TestHandleGetIssue_NoLinkedPRWhenNotAutoImplement asserts that an
// issue whose latest review is a plain review_only never carries a
// linked_pr block — the field is exclusive to auto_implement-origin
// PRs so the Flutter chip never appears on triage-only flows.
func TestHandleGetIssue_NoLinkedPRWhenNotAutoImplement(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	issueID, err := s.UpsertIssue(&store.Issue{
		GithubID: 7002, Repo: "org/repo", Number: 43, Title: "issue",
		Author: "alice", State: "open", CreatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	s.InsertIssueReview(&store.IssueReview{
		IssueID: issueID, CLIUsed: "claude", Summary: "triage",
		Triage: "{}", NextSteps: "[]",
		ActionTaken: "review_only", CreatedAt: now,
	})

	req := httptest.NewRequest("GET", fmt.Sprintf("/issues/%d", issueID), nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	iss := body["issue"].(map[string]any)
	if _, exists := iss["linked_pr"]; exists {
		t.Errorf("linked_pr should be omitted for review_only flow, got %v", iss["linked_pr"])
	}
}
