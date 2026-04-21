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
	if got.action != "implement" || got.outcome != "pr_opened" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.details["pr_number"] != 99 {
		t.Errorf("details pr_number: %v", got.details["pr_number"])
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

func TestRecorder_UnknownEventIsIgnored(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	events <- sse.Event{Type: "review_started", Data: "{}"}
	time.Sleep(50 * time.Millisecond)
	if fs.count() != 0 {
		t.Errorf("expected 0 inserts, got %d", fs.count())
	}
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
