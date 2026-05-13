package github_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
)

// TestGetPRReviews_ParsesFields pins the wire-format mapping for
// GET /repos/{repo}/pulls/{n}/reviews — Tier 3's review-state vigilance
// (#482) relies on state/user.login/submitted_at being populated so the
// aggregator can collapse the list into a single decision.
func TestGetPRReviews_ParsesFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/pulls/7/reviews" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"id":1,"user":{"login":"alice"},"state":"COMMENTED","body":"q","submitted_at":"2026-05-13T09:00:00Z"},
			{"id":2,"user":{"login":"bob"},"state":"APPROVED","body":"lgtm","submitted_at":"2026-05-13T10:00:00Z"},
			{"id":3,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"rename","submitted_at":"2026-05-13T11:00:00Z"}
		]`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	revs, err := c.GetPRReviews("org/repo", 7)
	if err != nil {
		t.Fatalf("GetPRReviews: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("len(revs) = %d, want 3", len(revs))
	}
	if revs[0].User.Login != "alice" || revs[0].State != "COMMENTED" {
		t.Errorf("revs[0] = %+v", revs[0])
	}
	if revs[2].State != "CHANGES_REQUESTED" || revs[2].Body != "rename" {
		t.Errorf("revs[2] = %+v", revs[2])
	}
	wantTS, _ := time.Parse(time.RFC3339, "2026-05-13T11:00:00Z")
	if !revs[2].SubmittedAt.Equal(wantTS) {
		t.Errorf("revs[2].SubmittedAt = %v, want %v", revs[2].SubmittedAt, wantTS)
	}
}
