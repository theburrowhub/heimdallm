package github_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// timelineServer returns an httptest server that serves the given raw
// timeline JSON for GET /repos/{repo}/issues/{n}/timeline (single page).
func timelineServer(t *testing.T, raw string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/issues/7/timeline" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(raw))
	}))
}

// TestGetPRTimelineEventsForReviewer_SameSecondReRequestWins is the
// regression guard for the intermittent re-review drop (#602). When a
// review_dismissed and a review_requested for the bot share the same
// whole-second created_at (GitHub timestamps and our stored anchor are
// both RFC3339 second-granularity), the slice MUST be ordered so the
// review_requested is last — i.e. the operator's explicit re-request
// wins the tie. pipeline.shouldBypassSHASkipForReReview inspects only
// the most recent (last) relevant event, so a non-deterministic order
// here silently skipped legitimate re-reviews about half the time.
//
// Both raw input orderings are exercised to prove the result is
// order-independent (a non-stable sort would pass one and fail the
// other).
func TestGetPRTimelineEventsForReviewer_SameSecondReRequestWins(t *testing.T) {
	const ts = "2026-06-24T11:00:00Z"
	reqEvent := fmt.Sprintf(`{"event":"review_requested","created_at":%q,"actor":{"login":"alice"},"requested_reviewer":{"login":"heimdallm-bot"}}`, ts)
	disEvent := fmt.Sprintf(`{"event":"review_dismissed","created_at":%q,"actor":{"login":"alice"},"dismissed_review":{"user":{"login":"heimdallm-bot"}}}`, ts)

	cases := []struct {
		name string
		raw  string
	}{
		{"request_then_dismiss_in_payload", "[" + reqEvent + "," + disEvent + "]"},
		{"dismiss_then_request_in_payload", "[" + disEvent + "," + reqEvent + "]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := timelineServer(t, tc.raw)
			defer srv.Close()

			c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
			events, err := c.GetPRTimelineEventsForReviewer("org/repo", 7, "heimdallm-bot")
			if err != nil {
				t.Fatalf("GetPRTimelineEventsForReviewer: %v", err)
			}
			if len(events) != 2 {
				t.Fatalf("len(events) = %d, want 2", len(events))
			}
			if last := events[len(events)-1]; last.Event != "review_requested" {
				t.Errorf("most-recent event = %q, want review_requested to win the same-second tie", last.Event)
			}
		})
	}
}
