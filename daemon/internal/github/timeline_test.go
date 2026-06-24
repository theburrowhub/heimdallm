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
		w.Header().Set("Content-Type", "application/json")
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
// reqEventAt / disEventAt build raw timeline JSON for a review_requested /
// review_dismissed event targeting heimdallm-bot at the given created_at.
func reqEventAt(ts string) string {
	return fmt.Sprintf(`{"event":"review_requested","created_at":%q,"actor":{"login":"alice"},"requested_reviewer":{"login":"heimdallm-bot"}}`, ts)
}

func disEventAt(ts string) string {
	return fmt.Sprintf(`{"event":"review_dismissed","created_at":%q,"actor":{"login":"alice"},"dismissed_review":{"user":{"login":"heimdallm-bot"}}}`, ts)
}

func TestGetPRTimelineEventsForReviewer_SameSecondReRequestWins(t *testing.T) {
	const ts = "2026-06-24T11:00:00Z"
	reqEvent := reqEventAt(ts)
	disEvent := disEventAt(ts)

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

// TestGetPRTimelineEventsForReviewer_SameSecondDuplicateTypes locks in the
// totality of the same-second tiebreak (#602 review follow-up). Two events
// of the SAME type at the same created_at must compare as equal — not as
// both i<j and j<i — so the comparator stays a strict weak ordering. A
// non-total comparator (returning true for less(i,j) AND less(j,i)) can
// make sort produce undefined output or panic. The expected last event is
// still driven by the policy: a review_requested at the tie always wins.
func TestGetPRTimelineEventsForReviewer_SameSecondDuplicateTypes(t *testing.T) {
	const ts = "2026-06-24T11:00:00Z"

	cases := []struct {
		name     string
		raw      string
		wantLen  int
		wantLast string
	}{
		{
			name:     "two_dismissed_then_request",
			raw:      "[" + disEventAt(ts) + "," + disEventAt(ts) + "," + reqEventAt(ts) + "]",
			wantLen:  3,
			wantLast: "review_requested",
		},
		{
			name:     "two_requests_only",
			raw:      "[" + reqEventAt(ts) + "," + reqEventAt(ts) + "]",
			wantLen:  2,
			wantLast: "review_requested",
		},
		{
			name:     "request_between_dismissals",
			raw:      "[" + disEventAt(ts) + "," + reqEventAt(ts) + "," + disEventAt(ts) + "]",
			wantLen:  3,
			wantLast: "review_requested",
		},
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
			if len(events) != tc.wantLen {
				t.Fatalf("len(events) = %d, want %d", len(events), tc.wantLen)
			}
			if last := events[len(events)-1]; last.Event != tc.wantLast {
				t.Errorf("most-recent event = %q, want %q", last.Event, tc.wantLast)
			}
		})
	}
}
