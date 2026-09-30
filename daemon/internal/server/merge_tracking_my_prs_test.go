package server_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func listEntries(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var got []map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	return got
}

// The listing carries who each PR is waiting on and whether it went stale, so
// the My PRs tab can group and flag rows without calling GitHub.
func TestHandleListMergeTracking_CarriesAttentionAndStale(t *testing.T) {
	srv, s, prID := newMergeTrackingServer(t)
	srv.SetMyPRsStaleAfterFn(func() time.Duration { return 72 * time.Hour })
	activity := time.Now().UTC().Add(-96 * time.Hour)
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase: store.MergePhaseBlocked, BlockReason: "changes_requested",
		Attention: "action", LastActivityAt: activity, At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	code, body := doJSON(t, srv, "GET", "/merge-tracking")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	e := listEntries(t, body)[0]
	if e["attention"] != "action" {
		t.Errorf("attention = %v, want action", e["attention"])
	}
	if e["stale"] != true {
		t.Errorf("stale = %v, want true for a PR idle 4 days with a 3-day threshold", e["stale"])
	}
	if e["last_activity_at"] != activity.Truncate(time.Second).Format(time.RFC3339) {
		t.Errorf("last_activity_at = %v", e["last_activity_at"])
	}

	// A raised threshold is honoured at read time, without re-evaluating.
	srv.SetMyPRsStaleAfterFn(func() time.Duration { return 7 * 24 * time.Hour })
	_, body = doJSON(t, srv, "GET", "/merge-tracking")
	if e := listEntries(t, body)[0]; e["stale"] != false {
		t.Errorf("stale = %v, want false under a 7-day threshold", e["stale"])
	}
}

func TestHandleListMergeTracking_TerminalRowsAreNeverStale(t *testing.T) {
	srv, s, prID := newMergeTrackingServer(t)
	srv.SetMyPRsStaleAfterFn(func() time.Duration { return time.Hour })
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase: store.MergePhaseIdle, LastActivityAt: time.Now().UTC().Add(-48 * time.Hour), At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	mergedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := s.MarkMergeTrackingMerged(prID, mergedAt); err != nil {
		t.Fatalf("merged: %v", err)
	}
	_, body := doJSON(t, srv, "GET", "/merge-tracking")
	e := listEntries(t, body)[0]
	if e["stale"] != false {
		t.Errorf("a merged PR is not stale, got %v", e["stale"])
	}
	if e["attention"] != "none" {
		t.Errorf("attention = %v, want none", e["attention"])
	}
	if e["terminal_at"] != mergedAt.Format(time.RFC3339) {
		t.Errorf("terminal_at = %v, want %s", e["terminal_at"], mergedAt.Format(time.RFC3339))
	}
}

// Unwired (no [my_prs] accessor), the flag is simply false.
func TestHandleListMergeTracking_StaleOffWhenUnwired(t *testing.T) {
	srv, s, prID := newMergeTrackingServer(t)
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase: store.MergePhaseIdle, LastActivityAt: time.Now().UTC().Add(-365 * 24 * time.Hour), At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	_, body := doJSON(t, srv, "GET", "/merge-tracking")
	if e := listEntries(t, body)[0]; e["stale"] != false || e["attention"] != "none" {
		t.Errorf("unwired entry: stale=%v attention=%v", e["stale"], e["attention"])
	}
}
