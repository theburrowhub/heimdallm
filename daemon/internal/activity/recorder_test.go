package activity_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/activity"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

type recordedInsert struct {
	ts                         time.Time
	org, repo, itemType        string
	itemNumber                 int
	itemTitle, action, outcome string
	details                    map[string]any
}

type fakeStore struct {
	mu       sync.Mutex
	inserts  []recordedInsert
	failNext bool
}

func (f *fakeStore) InsertActivity(
	ts time.Time, org, repo, itemType string, itemNumber int,
	itemTitle, action, outcome string, details map[string]any,
) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return 0, assertErr("store full")
	}
	f.inserts = append(f.inserts, recordedInsert{ts, org, repo, itemType, itemNumber, itemTitle, action, outcome, details})
	return int64(len(f.inserts)), nil
}

func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inserts)
}

func (f *fakeStore) at(i int) recordedInsert {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inserts[i]
}

func (f *fakeStore) setFailNext(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = v
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func newTestRecorder(t *testing.T) (*activity.Recorder, *fakeStore, chan sse.Event) {
	t.Helper()
	fs := &fakeStore{}
	events := make(chan sse.Event, 4)
	r := activity.NewWithChannel(fs, events)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.Start(ctx)
	return r, fs, events
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// Compile-time check: real *store.Store satisfies the recorder's Store interface.
var _ activity.Store = (*store.Store)(nil)

func TestRecorder_ReviewCompleted(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	payload, _ := json.Marshal(map[string]any{
		"repo":                "acme/api",
		"pr_number":           42,
		"pr_title":            "Fix rate limiter",
		"cli_used":            "claude",
		"severity":            "major",
		"review_id":           789,
		"github_review_state": "COMMENTED",
	})
	events <- sse.Event{Type: sse.EventReviewCompleted, Data: string(payload)}

	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.repo != "acme/api" || got.itemType != "pr" || got.itemNumber != 42 {
		t.Errorf("row basics: %+v", got)
	}
	if got.action != "review" || got.outcome != "major" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.org != "acme" {
		t.Errorf("org = %q, want acme", got.org)
	}
	if got.details["cli_used"] != "claude" {
		t.Errorf("details: %+v", got.details)
	}
}

func TestRecorder_ReviewError(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":      "acme/api",
		"pr_number": 7,
		"pr_title":  "WIP",
		"cli_used":  "claude",
		"error":     "cli_not_found",
	})
	events <- sse.Event{Type: sse.EventReviewError, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.action != "error" || got.outcome != "cli_not_found" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.itemType != "pr" {
		t.Errorf("item_type = %q, want pr", got.itemType)
	}
	if got.details["item_type"] != "pr" {
		t.Errorf("details item_type: %v", got.details["item_type"])
	}
}

func TestRecorder_IssueReviewCompleted(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":          "acme/api",
		"issue_number":  12,
		"issue_title":   "Refactor auth",
		"cli_used":      "claude",
		"severity":      "major",
		"category":      "develop",
		"chosen_action": "auto_implement",
	})
	events <- sse.Event{Type: sse.EventIssueReviewCompleted, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.itemType != "issue" || got.itemNumber != 12 {
		t.Errorf("row basics: %+v", got)
	}
	if got.action != "triage" || got.outcome != "major" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.details["category"] != "develop" || got.details["chosen_action"] != "auto_implement" {
		t.Errorf("details: %+v", got.details)
	}
}

func TestRecorder_IssueTriage_EmptySeverityOutcomeIsIgnored(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":          "acme/api",
		"issue_number":  20,
		"issue_title":   "Noise",
		"cli_used":      "claude",
		"severity":      "",
		"category":      "review_only",
		"chosen_action": "ignore",
	})
	events <- sse.Event{Type: sse.EventIssueReviewCompleted, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	if fs.at(0).outcome != "ignored" {
		t.Errorf("outcome = %q, want ignored", fs.at(0).outcome)
	}
}

func TestRecorder_IssueImplemented(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":         "acme/api",
		"issue_number": 12,
		"issue_title":  "Refactor auth",
		"cli_used":     "claude",
		"pr_number":    99,
		"pr_url":       "https://github.com/acme/api/pull/99",
	})
	events <- sse.Event{Type: sse.EventIssueImplemented, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.itemType != "issue" || got.itemNumber != 12 || got.itemTitle != "Refactor auth" {
		t.Errorf("row basics: %+v", got)
	}
	if got.action != "implement" || got.outcome != "pr_opened" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.details["cli_used"] != "claude" {
		t.Errorf("details cli_used: %v", got.details["cli_used"])
	}
	if got.details["pr_number"] != 99 {
		t.Errorf("details pr_number: %v", got.details["pr_number"])
	}
}

func TestRecorder_IssueRefinementDone(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":         "acme/api",
		"issue_number": 12,
		"issue_title":  "Refactor auth",
		"cli_used":     "claude",
		"review_id":    321,
		"post_ok":      true,
		"truncated":    false,
	})
	events <- sse.Event{Type: sse.EventIssueRefinementDone, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.itemType != "issue" || got.itemNumber != 12 || got.itemTitle != "Refactor auth" {
		t.Errorf("row basics: %+v", got)
	}
	if got.action != "refinement" || got.outcome != "completed" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.details["cli_used"] != "claude" || got.details["review_id"] != int64(321) {
		t.Errorf("details: %+v", got.details)
	}
	if got.details["post_ok"] != true || got.details["truncated"] != false {
		t.Errorf("post/truncation details: %+v", got.details)
	}
}

func TestRecorder_IssueImplemented_Failed(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":         "acme/api",
		"issue_number": 12,
		"issue_title":  "Refactor auth",
		"cli_used":     "claude",
		"pr_number":    0,
	})
	events <- sse.Event{Type: sse.EventIssueImplemented, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	if fs.at(0).outcome != "pr_failed" {
		t.Errorf("outcome = %q, want pr_failed", fs.at(0).outcome)
	}
}

func TestRecorder_IssueReviewError(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":         "acme/api",
		"issue_number": 3,
		"issue_title":  "Bad data",
		"cli_used":     "claude",
		"error":        "parse_failed",
	})
	events <- sse.Event{Type: sse.EventIssueReviewError, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.action != "error" || got.outcome != "parse_failed" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.details["item_type"] != "issue" {
		t.Errorf("details item_type: %v", got.details["item_type"])
	}
}

func TestRecorder_IssuePromoted(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":         "acme/api",
		"issue_number": 42,
		"issue_title":  "Schema migration",
		"from_label":   "blocked",
		"to_label":     "develop",
		"reason":       "dependencies closed",
	})
	events <- sse.Event{Type: sse.EventIssuePromoted, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.action != "promote" || got.outcome != "blocked → develop" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.details["reason"] != "dependencies closed" {
		t.Errorf("details: %+v", got.details)
	}
}

func TestRecorder_IssueStagePromoted(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":         "acme/api",
		"issue_number": 43,
		"issue_title":  "Plan implementation",
		"from_stage":   "triage",
		"to_stage":     "refinement",
		"trigger":      "manual API",
	})
	events <- sse.Event{Type: sse.EventIssuePromoted, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.action != "promote" || got.outcome != "triage → refinement" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.details["trigger"] != "manual API" {
		t.Errorf("details: %+v", got.details)
	}
	if _, ok := got.details["from_label"]; ok {
		t.Errorf("stage event should not persist empty legacy label details: %+v", got.details)
	}
}

func TestRecorder_IssueStagePromotedPrefersStageDetailsOverLegacyLabels(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	payload, _ := json.Marshal(map[string]any{
		"repo":         "acme/api",
		"issue_number": 44,
		"issue_title":  "Plan implementation",
		"from_label":   "blocked",
		"to_label":     "ready",
		"from_stage":   "triage",
		"to_stage":     "refinement",
	})
	events <- sse.Event{Type: sse.EventIssuePromoted, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.outcome != "triage → refinement" {
		t.Errorf("outcome: %s", got.outcome)
	}
	if got.details["from_stage"] != "triage" || got.details["to_stage"] != "refinement" {
		t.Errorf("stage details: %+v", got.details)
	}
	if _, ok := got.details["from_label"]; ok {
		t.Errorf("stage event should omit legacy label details even when payload has them: %+v", got.details)
	}
}

func TestRecorder_UnknownEventIsIgnored(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	events <- sse.Event{Type: "review_started", Data: "{}"}
	time.Sleep(50 * time.Millisecond)
	if fs.count() != 0 {
		t.Errorf("expected 0 inserts, got %d", fs.count())
	}
}

func TestRecorder_ReviewSkipped(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	events <- sse.Event{
		Type: sse.EventReviewSkipped,
		Data: `{"repo":"org/name","pr_number":42,"pr_title":"Fix X","reason":"draft"}`,
	}

	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.action != "review_skipped" {
		t.Errorf("action = %q, want review_skipped", got.action)
	}
	if got.outcome != "draft" {
		t.Errorf("outcome = %q, want draft", got.outcome)
	}
	if got.itemType != "pr" || got.itemNumber != 42 {
		t.Errorf("item = %s#%d, want pr#42", got.itemType, got.itemNumber)
	}
	if got.repo != "org/name" || got.org != "org" {
		t.Errorf("repo/org = %q/%q, want org/name + org", got.repo, got.org)
	}
}

// TestRecorder_ReviewSkippedDedupReasonsAreNotRecorded locks in the
// fix from theburrowhub/heimdallm#322 review feedback: dedup-flavoured
// skips (sha_unchanged, legacy_backfill, retry cooldowns) MUST NOT generate
// activity_log rows even though they fire as review_skipped SSE events. They run on
// every poll cycle on stable PRs — recording them would produce one
// spam row per minute per stable PR, which is the activity-log spam
// regression Bug 4 was supposed to close.
func TestRecorder_ReviewSkippedDedupReasonsAreNotRecorded(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	for _, reason := range []string{
		"sha_unchanged", "legacy_backfill", "retry_cooldown", "retry_repo_limit",
		// self_authored joined this list once Heimdallm started authenticating
		// as the operator's own account: the guard then fires on every PR they
		// open, on every cycle, and the operator's own PRs have the Merge tab
		// of their own. See TestRecorder_SelfAuthoredSkipsAreNotRecorded.
		"self_authored",
	} {
		events <- sse.Event{
			Type: sse.EventReviewSkipped,
			Data: `{"repo":"org/name","pr_number":42,"pr_title":"Fix X","reason":"` + reason + `"}`,
		}
	}
	// Give the recorder a beat to drain any rows it might have inserted.
	time.Sleep(50 * time.Millisecond)
	if got := fs.count(); got != 0 {
		t.Errorf("dedup skips should NOT produce activity_log rows; got %d rows", got)
	}
}

// Heimdallm authenticates as the operator's own GitHub account, so the
// "self-authored" review guard fires on every pull request the operator opens,
// on every poll cycle, for as long as it stays open. Recording those buried the
// activity log under identical rows — the same PR, the same reason, once a
// cycle — for PRs the operator manages from the Merge tab instead.
func TestRecorder_SelfAuthoredSkipsAreNotRecorded(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	// Three cycles over the same PR, exactly what the poller produces.
	for i := 0; i < 3; i++ {
		events <- sse.Event{
			Type: sse.EventReviewSkipped,
			Data: `{"repo":"org/name","pr_number":1224,"pr_title":"chore(pro): right-size resources","reason":"self_authored"}`,
		}
	}
	time.Sleep(50 * time.Millisecond)
	if got := fs.count(); got != 0 {
		t.Errorf("a self-authored skip must not reach the activity log; got %d rows", got)
	}

	// A real policy decision still gets its row.
	events <- sse.Event{
		Type: sse.EventReviewSkipped,
		Data: `{"repo":"org/name","pr_number":7,"pr_title":"Fix X","reason":"draft"}`,
	}
	waitFor(t, func() bool { return fs.count() == 1 })
}

func TestRecorder_StoreFailureIsLoggedAndDropped(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	fs.setFailNext(true)

	payload, _ := json.Marshal(map[string]any{
		"repo": "acme/api", "pr_number": 1, "pr_title": "t",
		"cli_used": "claude", "severity": "minor",
	})
	events <- sse.Event{Type: sse.EventReviewCompleted, Data: string(payload)}

	time.Sleep(30 * time.Millisecond)
	events <- sse.Event{Type: sse.EventReviewCompleted, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })
}

func TestRecorder_PollingEventsAreIgnored(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	// Feed both polling event types through the event channel that
	// the recorder's Start loop consumes.
	for _, ev := range []sse.Event{
		{Type: sse.EventPollingStarted, Data: `{"kind":"prs","repos":["acme/foo"]}`},
		{Type: sse.EventPollingCompleted, Data: `{"kind":"issues","count":3,"duration_ms":42}`},
	} {
		events <- ev
	}

	// Give the recorder a beat to process any rows it might have inserted.
	time.Sleep(50 * time.Millisecond)

	if got := fs.count(); got != 0 {
		t.Errorf("polling events should not produce rows; got %d", got)
	}
}

// ── Merge tracking ─────────────────────────────────────────────────────────

func TestRecorder_MergeTrackMerged(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	payload, _ := json.Marshal(map[string]any{
		"pr_id": 7, "repo": "acme/api", "number": 42,
		"method": "squash", "sha": "abc123",
	})
	events <- sse.Event{Type: sse.EventMergeTrackMerged, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.repo != "acme/api" || got.org != "acme" || got.itemNumber != 42 {
		t.Errorf("row basics: %+v", got)
	}
	if got.action != "merge_track_merged" || got.outcome != "squash" {
		t.Errorf("action/outcome = %q/%q", got.action, got.outcome)
	}
	if got.details["sha"] != "abc123" {
		t.Errorf("details should record the merged sha: %v", got.details)
	}
}

func TestRecorder_MergeTrackAutoMergeArmed(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	payload, _ := json.Marshal(map[string]any{
		"repo": "acme/api", "number": 42, "method": "rebase",
	})
	events <- sse.Event{Type: sse.EventMergeTrackAutoMergeArmed, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.action != "merge_track_auto_merge_armed" || got.outcome != "rebase" {
		t.Errorf("action/outcome = %q/%q", got.action, got.outcome)
	}
}

func TestRecorder_MergeTrackBranchUpdated(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	payload, _ := json.Marshal(map[string]any{
		"repo": "acme/api", "number": 42, "mode": "local_rebase",
	})
	events <- sse.Event{Type: sse.EventMergeTrackBranchUpdated, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.action != "merge_track_branch_updated" || got.outcome != "local_rebase" {
		t.Errorf("action/outcome = %q/%q", got.action, got.outcome)
	}
}

// Both outcomes are recorded. A resolution that was NOT pushed matters at
// least as much: it means the agent looked at the conflicts and declined.
func TestRecorder_MergeTrackConflictResolved(t *testing.T) {
	for _, tc := range []struct {
		pushed bool
		want   string
	}{{true, "pushed"}, {false, "not pushed"}} {
		t.Run(tc.want, func(t *testing.T) {
			_, fs, events := newTestRecorder(t)

			payload, _ := json.Marshal(map[string]any{
				"repo": "acme/api", "number": 42,
				"pushed": tc.pushed, "files": []string{"a.go"},
				"pre_rebase_sha": "abc123",
			})
			events <- sse.Event{Type: sse.EventMergeTrackConflictResolved, Data: string(payload)}
			waitFor(t, func() bool { return fs.count() == 1 })

			got := fs.at(0)
			if got.outcome != tc.want {
				t.Errorf("outcome = %q, want %q", got.outcome, tc.want)
			}
			// The pre-rebase SHA is the recovery path; losing it here loses it.
			if got.details["pre_rebase_sha"] != "abc123" {
				t.Errorf("details should record the pre-rebase sha: %v", got.details)
			}
		})
	}
}

func TestRecorder_MergeTrackBlocked(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	payload, _ := json.Marshal(map[string]any{
		"repo": "acme/api", "number": 42,
		"reason": "checks_failing",
		"detail": "1 required check is failing: build (GitHub Actions)",
	})
	events <- sse.Event{Type: sse.EventMergeTrackBlocked, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.action != "merge_track_blocked" || got.outcome != "checks_failing" {
		t.Errorf("action/outcome = %q/%q", got.action, got.outcome)
	}
	// The detail names the check; a reason code alone is not actionable.
	if got.details["detail"] != "1 required check is failing: build (GitHub Actions)" {
		t.Errorf("details should carry the specific text: %v", got.details)
	}
}

func TestRecorder_MergeTrackError(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	payload, _ := json.Marshal(map[string]any{
		"repo": "acme/api", "number": 42,
		"action": "merge", "err": "connection reset",
	})
	events <- sse.Event{Type: sse.EventMergeTrackError, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	got := fs.at(0)
	if got.action != "error" || got.outcome != "connection reset" {
		t.Errorf("action/outcome = %q/%q", got.action, got.outcome)
	}
	if got.details["action"] != "merge" {
		t.Errorf("details should record which action failed: %v", got.details)
	}
}

// merge_track_evaluated fires for every PR on every cycle. Persisting it would
// mean one activity row per PR per cycle, which drowns the log.
func TestRecorder_MergeTrackEvaluatedIsNotPersisted(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	payload, _ := json.Marshal(map[string]any{"repo": "acme/api", "number": 42})
	events <- sse.Event{Type: sse.EventMergeTrackEvaluated, Data: string(payload)}
	// Followed by an event that IS recorded, so we can tell "not yet" from
	// "never" without sleeping on a negative.
	events <- sse.Event{Type: sse.EventMergeTrackBlocked, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	if got := fs.at(0).action; got != "merge_track_blocked" {
		t.Errorf("first recorded row = %q, want the blocked event only", got)
	}
	if fs.count() != 1 {
		t.Errorf("recorded %d rows, want 1", fs.count())
	}
}

func TestRecorder_MergeTrackMalformedPayloadIsSkipped(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	events <- sse.Event{Type: sse.EventMergeTrackMerged, Data: "{not json"}
	payload, _ := json.Marshal(map[string]any{"repo": "acme/api", "number": 1})
	events <- sse.Event{Type: sse.EventMergeTrackMerged, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	if got := fs.at(0).repo; got != "acme/api" {
		t.Errorf("a malformed payload must not stop the next event: got %q", got)
	}
}

// A repo slug with no owner still has to produce a row rather than panic.
func TestRecorder_MergeTrackHandlesAnOwnerlessRepo(t *testing.T) {
	_, fs, events := newTestRecorder(t)

	payload, _ := json.Marshal(map[string]any{"repo": "widgets", "number": 1, "reason": "draft"})
	events <- sse.Event{Type: sse.EventMergeTrackBlocked, Data: string(payload)}
	waitFor(t, func() bool { return fs.count() == 1 })

	if got := fs.at(0).org; got != "widgets" {
		t.Errorf("org = %q, want the slug itself when there is no owner", got)
	}
}
