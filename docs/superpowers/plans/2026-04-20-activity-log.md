# Activity Log Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a daily activity log that records every significant Heimdallm action (PR reviews, issue triages, auto-implement runs, label promotions, errors), expose it through `GET /activity`, and render it in a new Flutter "Activity" tab with date/org/repo/action filters.

**Architecture:** Append-only `activity_log` SQLite table, written by an `ActivityRecorder` that subscribes to the existing SSE broker. New `issue_promoted` event added to the promoter. HTTP handler `GET /activity` exposes a filtered, paginated query. Flutter ships a new top-level tab with date picker, filter chips, and a grouped-by-hour timeline.

**Tech Stack:** Go 1.21 (SQLite via `modernc.org/sqlite`, chi router, existing SSE broker), Flutter 3.8+ (Riverpod, GoRouter — match patterns in `lib/features/dashboard/`).

**Spec:** `docs/superpowers/specs/2026-04-20-activity-log-design.md`

**Out of scope (deferred to a follow-up spec):** AI "Generate Report" button, 4th agent prompt type ("Report"), report display dialog.

---

## File Structure

### Daemon (Go)

| File | Responsibility |
|------|---------------|
| `daemon/internal/store/store.go` | Add `activity_log` `CREATE TABLE` + indexes to `schema` constant |
| `daemon/internal/store/activity.go` | **Create** — `Activity` type, `InsertActivity`, `ListActivity`, `PurgeOldActivity` |
| `daemon/internal/store/activity_test.go` | **Create** — CRUD, filter combinations, purge cutoff |
| `daemon/internal/sse/broker.go` | Add `EventIssuePromoted` constant |
| `daemon/internal/issues/promoter.go` | Inject `Publisher`, emit `issue_promoted` after successful label flip |
| `daemon/internal/issues/promoter_test.go` | Assert event emitted with correct payload |
| `daemon/internal/activity/recorder.go` | **Create** — `Recorder` struct, event loop, event→row mapping |
| `daemon/internal/activity/recorder_test.go` | **Create** — one test per event type; store-failure path |
| `daemon/internal/config/config.go` | Add `ActivityLogConfig` + `Config.ActivityLog` field |
| `daemon/internal/config/store.go` | Add `activity_log_enabled`, `activity_log_retention_days` cases to `ApplyStore` |
| `daemon/internal/config/config_test.go` | Tests for new config values |
| `daemon/internal/server/handlers.go` | Add `GET /activity` handler, register route, add to `sensitiveGETPaths` |
| `daemon/internal/server/handlers_test.go` | Happy path + 400 cases + 503 when disabled |
| `daemon/cmd/heimdallm/main.go` | Construct `ActivityRecorder`, wire to broker, startup purge, 24h ticker |

### Flutter (Dart)

| File | Responsibility |
|------|---------------|
| `flutter_app/lib/features/activity/activity_models.dart` | **Create** — `ActivityEntry`, `ActivityQuery`, JSON mapping |
| `flutter_app/lib/features/activity/activity_api.dart` | **Create** — HTTP client for `GET /activity` |
| `flutter_app/lib/features/activity/activity_providers.dart` | **Create** — Riverpod providers: query state, entries future |
| `flutter_app/lib/features/activity/activity_screen.dart` | **Create** — screen scaffold, date picker, filter chips, timeline |
| `flutter_app/lib/features/activity/widgets/activity_entry_tile.dart` | **Create** — one entry row |
| `flutter_app/lib/features/activity/widgets/activity_filter_chips.dart` | **Create** — org/repo/action multi-select chips |
| `flutter_app/lib/shared/router.dart` | Add `/activity` route |
| `flutter_app/lib/shared/nav_shell.dart` *(or wherever bottom-nav is defined)* | Add "Activity" tab between Issues and Stats |
| `flutter_app/test/features/activity/activity_screen_test.dart` | **Create** — widget tests |

---

## Phase A — Store + schema + promoter event (PRs 1 & 2)

Self-contained backend foundation. Produces a migrated table, new broker event, and passing unit tests. No runtime effect until Phase B wires the recorder.

---

### Task 1: Add `activity_log` table and indexes

**Files:**
- Modify: `daemon/internal/store/store.go`

- [ ] **Step 1: Add the table + indexes to the `schema` constant**

In `daemon/internal/store/store.go`, append to the `schema` const (after the last existing `CREATE INDEX`, before the closing backtick):

```go
CREATE TABLE IF NOT EXISTS activity_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          DATETIME NOT NULL,
  org         TEXT NOT NULL,
  repo        TEXT NOT NULL,
  item_type   TEXT NOT NULL,
  item_number INTEGER NOT NULL,
  item_title  TEXT NOT NULL,
  action      TEXT NOT NULL,
  outcome     TEXT NOT NULL DEFAULT '',
  details     TEXT NOT NULL DEFAULT '{}',
  created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_activity_ts      ON activity_log(ts DESC);
CREATE INDEX IF NOT EXISTS idx_activity_repo_ts ON activity_log(repo, ts DESC);
```

- [ ] **Step 2: Run existing store tests to confirm schema still applies cleanly**

Run: `make test-docker GO_TEST_ARGS="./internal/store/..."`
Expected: all existing store tests PASS. The schema is applied on every `Open`, so a typo here would break unrelated tests immediately.

- [ ] **Step 3: Commit**

```bash
git add daemon/internal/store/store.go
git commit -m "feat(daemon): add activity_log table and indexes"
```

---

### Task 2: Activity store operations (`activity.go`) — TDD

**Files:**
- Create: `daemon/internal/store/activity.go`
- Create: `daemon/internal/store/activity_test.go`

- [ ] **Step 1: Write failing test for `InsertActivity` + round-trip**

Create `daemon/internal/store/activity_test.go`:

```go
package store

import (
	"encoding/json"
	"testing"
	"time"
)

func newActivityStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInsertActivity_RoundTrip(t *testing.T) {
	s := newActivityStore(t)
	ts := time.Now().UTC().Truncate(time.Second)

	id, err := s.InsertActivity(ts, "acme", "acme/api", "pr", 42, "Fix bug",
		"review", "major", map[string]any{"cli_used": "claude"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	entries, truncated, err := s.ListActivity(ActivityQuery{From: ts.Add(-time.Hour), To: ts.Add(time.Hour), Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if truncated {
		t.Error("unexpected truncation")
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Repo != "acme/api" || e.Action != "review" || e.Outcome != "major" {
		t.Errorf("unexpected entry: %+v", e)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(e.DetailsJSON), &details); err != nil {
		t.Fatalf("details unmarshal: %v", err)
	}
	if details["cli_used"] != "claude" {
		t.Errorf("details cli_used = %v", details["cli_used"])
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

Run: `make test-docker GO_TEST_ARGS="-run TestInsertActivity_RoundTrip ./internal/store/..."`
Expected: FAIL with `undefined: InsertActivity` / `undefined: ActivityQuery`.

- [ ] **Step 3: Implement `activity.go`**

Create `daemon/internal/store/activity.go`:

```go
package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Activity is one row in the activity_log timeline.
type Activity struct {
	ID          int64     `json:"id"`
	Timestamp   time.Time `json:"ts"`
	Org         string    `json:"org"`
	Repo        string    `json:"repo"`
	ItemType    string    `json:"item_type"`    // "pr" | "issue"
	ItemNumber  int       `json:"item_number"`
	ItemTitle   string    `json:"item_title"`
	Action      string    `json:"action"`       // "review" | "triage" | "implement" | "promote" | "error"
	Outcome     string    `json:"outcome"`
	DetailsJSON string    `json:"-"`            // raw JSON payload; handler decodes before sending
	CreatedAt   time.Time `json:"-"`
}

// ActivityQuery bounds a ListActivity call.
// Zero values for From/To mean "no lower/upper bound" but the handler always
// supplies a bounded window, so unbounded queries only happen in tests.
type ActivityQuery struct {
	From    time.Time
	To      time.Time
	Orgs    []string
	Repos   []string
	Actions []string
	Limit   int // 0 → default 500; upper-bounded by caller
}

const defaultActivityLimit = 500
const maxActivityLimit = 5000

// InsertActivity writes one event row. `details` is marshalled to JSON; pass
// nil or an empty map to store "{}".
func (s *Store) InsertActivity(
	ts time.Time, org, repo, itemType string, itemNumber int,
	itemTitle, action, outcome string, details map[string]any,
) (int64, error) {
	payload := "{}"
	if len(details) > 0 {
		b, err := json.Marshal(details)
		if err != nil {
			return 0, fmt.Errorf("store: marshal activity details: %w", err)
		}
		payload = string(b)
	}
	now := time.Now().UTC().Format(sqliteTimeFormat)
	res, err := s.db.Exec(`
		INSERT INTO activity_log (ts, org, repo, item_type, item_number, item_title, action, outcome, details, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ts.UTC().Format(sqliteTimeFormat), org, repo, itemType, itemNumber,
		itemTitle, action, outcome, payload, now)
	if err != nil {
		return 0, fmt.Errorf("store: insert activity: %w", err)
	}
	return res.LastInsertId()
}

// ListActivity returns entries matching the query, newest first.
// Second return value is `truncated` — true when the result hit the limit.
func (s *Store) ListActivity(q ActivityQuery) ([]*Activity, bool, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}

	var (
		where []string
		args  []any
	)
	if !q.From.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, q.From.UTC().Format(sqliteTimeFormat))
	}
	if !q.To.IsZero() {
		where = append(where, "ts <= ?")
		args = append(args, q.To.UTC().Format(sqliteTimeFormat))
	}
	if len(q.Orgs) > 0 {
		where = append(where, "org IN ("+placeholders(len(q.Orgs))+")")
		for _, o := range q.Orgs {
			args = append(args, o)
		}
	}
	if len(q.Repos) > 0 {
		where = append(where, "repo IN ("+placeholders(len(q.Repos))+")")
		for _, r := range q.Repos {
			args = append(args, r)
		}
	}
	if len(q.Actions) > 0 {
		where = append(where, "action IN ("+placeholders(len(q.Actions))+")")
		for _, a := range q.Actions {
			args = append(args, a)
		}
	}

	// Over-fetch by 1 to detect truncation without a second COUNT query.
	args = append(args, limit+1)
	query := `
		SELECT id, ts, org, repo, item_type, item_number, item_title, action, outcome, details, created_at
		FROM activity_log
	`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY ts DESC LIMIT ?"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("store: list activity: %w", err)
	}
	defer rows.Close()

	var out []*Activity
	for rows.Next() {
		var (
			a              Activity
			tsStr, createdStr string
		)
		if err := rows.Scan(&a.ID, &tsStr, &a.Org, &a.Repo, &a.ItemType,
			&a.ItemNumber, &a.ItemTitle, &a.Action, &a.Outcome,
			&a.DetailsJSON, &createdStr); err != nil {
			return nil, false, fmt.Errorf("store: scan activity: %w", err)
		}
		if a.Timestamp, err = time.Parse(sqliteTimeFormat, tsStr); err != nil {
			return nil, false, fmt.Errorf("store: parse ts %q: %w", tsStr, err)
		}
		if a.CreatedAt, err = time.Parse(sqliteTimeFormat, createdStr); err != nil {
			return nil, false, fmt.Errorf("store: parse created_at %q: %w", createdStr, err)
		}
		out = append(out, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, nil
}

// PurgeOldActivity deletes activity rows older than maxDays. No-op if maxDays == 0.
func (s *Store) PurgeOldActivity(maxDays int) error {
	if maxDays == 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(maxDays) * 24 * time.Hour).Format(sqliteTimeFormat)
	_, err := s.db.Exec("DELETE FROM activity_log WHERE ts < ?", cutoff)
	if err != nil {
		return fmt.Errorf("store: purge old activity: %w", err)
	}
	return nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
```

- [ ] **Step 4: Run the initial test, confirm it passes**

Run: `make test-docker GO_TEST_ARGS="-run TestInsertActivity_RoundTrip ./internal/store/..."`
Expected: PASS.

- [ ] **Step 5: Write failing tests for filters**

Append to `activity_test.go`:

```go
func TestListActivity_FilterByOrgAndAction(t *testing.T) {
	s := newActivityStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	must := func(ts time.Time, org, repo, action string) {
		t.Helper()
		if _, err := s.InsertActivity(ts, org, repo, "pr", 1, "t", action, "", nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	must(base.Add(-3*time.Minute), "acme", "acme/api", "review")
	must(base.Add(-2*time.Minute), "acme", "acme/api", "triage")
	must(base.Add(-1*time.Minute), "globex", "globex/web", "review")

	entries, _, err := s.ListActivity(ActivityQuery{
		Orgs:    []string{"acme"},
		Actions: []string{"review"},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Repo != "acme/api" || entries[0].Action != "review" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
}

func TestListActivity_Truncation(t *testing.T) {
	s := newActivityStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		if _, err := s.InsertActivity(base.Add(time.Duration(i)*time.Second),
			"acme", "acme/api", "pr", i, "t", "review", "", nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	entries, truncated, err := s.ListActivity(ActivityQuery{Limit: 3})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !truncated {
		t.Error("expected truncated=true")
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	// Newest first.
	if entries[0].ItemNumber != 4 {
		t.Errorf("want newest item_number=4, got %d", entries[0].ItemNumber)
	}
}

func TestPurgeOldActivity_CutoffBoundary(t *testing.T) {
	s := newActivityStore(t)
	now := time.Now().UTC()
	old := now.Add(-100 * 24 * time.Hour)
	recent := now.Add(-1 * 24 * time.Hour)

	if _, err := s.InsertActivity(old, "a", "a/b", "pr", 1, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertActivity(recent, "a", "a/b", "pr", 2, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}

	if err := s.PurgeOldActivity(90); err != nil {
		t.Fatalf("purge: %v", err)
	}
	entries, _, err := s.ListActivity(ActivityQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 remaining, got %d", len(entries))
	}
	if entries[0].ItemNumber != 2 {
		t.Errorf("wrong entry survived: %+v", entries[0])
	}
}

func TestPurgeOldActivity_ZeroIsNoOp(t *testing.T) {
	s := newActivityStore(t)
	if _, err := s.InsertActivity(time.Now().UTC().Add(-365*24*time.Hour),
		"a", "a/b", "pr", 1, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeOldActivity(0); err != nil {
		t.Fatalf("purge: %v", err)
	}
	entries, _, _ := s.ListActivity(ActivityQuery{})
	if len(entries) != 1 {
		t.Fatalf("want 1 remaining (no-op), got %d", len(entries))
	}
}
```

- [ ] **Step 6: Run all tests, confirm they pass**

Run: `make test-docker GO_TEST_ARGS="./internal/store/..."`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add daemon/internal/store/activity.go daemon/internal/store/activity_test.go
git commit -m "feat(daemon): activity store operations with filtered list and retention purge"
```

---

### Task 3: New `issue_promoted` SSE event + promoter emission — TDD

**Files:**
- Modify: `daemon/internal/sse/broker.go`
- Modify: `daemon/internal/issues/promoter.go`
- Modify: `daemon/internal/issues/promoter_test.go`

- [ ] **Step 1: Add the event constant**

In `daemon/internal/sse/broker.go`, extend the issue-tracking event block:

```go
	// Issue tracking pipeline (#26 onward).
	EventIssueDetected        = "issue_detected"
	EventIssueReviewStarted   = "issue_review_started"
	EventIssueReviewCompleted = "issue_review_completed"
	EventIssueImplemented     = "issue_implemented" // reserved for #27 (auto_implement PR created)
	EventIssueReviewError     = "issue_review_error"
	EventIssuePromoted        = "issue_promoted" // #113: promoter flipped blocked → promote-to label
```

- [ ] **Step 2: Write failing promoter test for event emission**

Locate `TestPromoteReady_*` tests in `daemon/internal/issues/promoter_test.go` — find the one that exercises a successful promotion (e.g. deps closed → label flip). Add a new test modelled on it:

```go
func TestPromoteReady_EmitsIssuePromotedEvent(t *testing.T) {
	// Use whatever fixture helper the existing success test uses — e.g.:
	client := newFakePromoteClient()
	client.seedOpenIssue("acme/api", 42, "Fix bug", []string{"blocked"})
	client.seedDep("acme/api", 42, "acme/api", 7, "closed")

	broker := &fakeBroker{}
	cfg := config.IssueTrackingConfig{
		Enabled:       true,
		BlockedLabels: []string{"blocked"},
		PromoteTo:     "develop",
	}

	// NOTE: PromoteReady does not take a broker today. Task 3 changes its
	// signature to accept one (see Step 3). Update the call accordingly.
	n, err := PromoteReady(context.Background(), client, cfg, []string{"acme/api"}, broker)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 promotion, got %d", n)
	}
	if len(broker.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(broker.events))
	}
	ev := broker.events[0]
	if ev.Type != sse.EventIssuePromoted {
		t.Errorf("event type = %q, want issue_promoted", ev.Type)
	}
	var payload struct {
		Repo         string `json:"repo"`
		IssueNumber  int    `json:"issue_number"`
		IssueTitle   string `json:"issue_title"`
		FromLabel    string `json:"from_label"`
		ToLabel      string `json:"to_label"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Repo != "acme/api" || payload.IssueNumber != 42 {
		t.Errorf("payload repo/number: %+v", payload)
	}
	if payload.FromLabel != "blocked" || payload.ToLabel != "develop" {
		t.Errorf("payload labels: %+v", payload)
	}
}
```

If `fakeBroker` is not already defined in `promoter_test.go`, copy the definition from `daemon/internal/issues/pipeline_test.go` (lines ~165–180, the helper with `events []sse.Event` and a `Publish` method).

- [ ] **Step 3: Run test to verify it fails**

Run: `make test-docker GO_TEST_ARGS="-run TestPromoteReady_EmitsIssuePromotedEvent ./internal/issues/..."`
Expected: FAIL — compile error (`PromoteReady` signature mismatch) or missing `EventIssuePromoted` reference (it's defined but unused).

- [ ] **Step 4: Add `Publisher` dependency to `PromoteReady` and emit the event**

In `daemon/internal/issues/promoter.go`:

1. Add the `Publisher` type alias (same as `pipeline.go`'s `Publisher` interface). Near the top of the file:

```go
// Publisher is the subset of sse.Broker used to emit promote events.
type Publisher interface {
	Publish(e sse.Event)
}
```

Add `"encoding/json"` and `"github.com/heimdallm/daemon/internal/sse"` to the imports.

2. Change the `PromoteReady` signature:

```go
func PromoteReady(ctx context.Context, c PromoteIssueClient, cfg config.IssueTrackingConfig, repos []string, broker Publisher) (int, error) {
```

3. After the existing `slog.Info("issues promote: promoted issue", ...)` call (inside the per-issue success branch), emit the event:

```go
if broker != nil {
	// One event per successful label flip. Payload schema is stable —
	// consumers (ActivityRecorder) rely on these field names.
	payload := map[string]any{
		"repo":         repo,
		"issue_number": issue.Number,
		"issue_title":  issue.Title,
		"from_label":   strings.Join(blockedOnIssue, ","),
		"to_label":     promoteTo,
		"reason":       "dependencies closed",
	}
	if b, err := json.Marshal(payload); err == nil {
		broker.Publish(sse.Event{Type: sse.EventIssuePromoted, Data: string(b)})
	}
}
```

The `nil` check lets legacy callers (tests that pass `nil`) keep working without a broker; in production `main.go` always supplies one.

- [ ] **Step 5: Update all callers of `PromoteReady` in the codebase**

```bash
grep -rn "PromoteReady(" daemon/
```

Expected callers to update:
- `daemon/cmd/heimdallm/main.go` — add the existing `broker` variable as the new last arg.
- any other test files that call `PromoteReady` — pass `nil` for the broker to preserve existing behaviour.

- [ ] **Step 6: Run tests, confirm pass**

Run: `make test-docker GO_TEST_ARGS="./internal/issues/..."`
Expected: all PASS, including the new test.

Run: `make test-docker`
Expected: full suite PASS (sanity check that no caller was missed).

- [ ] **Step 7: Commit**

```bash
git add daemon/internal/sse/broker.go daemon/internal/issues/promoter.go daemon/internal/issues/promoter_test.go daemon/cmd/heimdallm/main.go
git commit -m "feat(daemon): emit issue_promoted SSE event from promoter"
```

---

## Phase B — ActivityRecorder (PR 3 part 1)

Bridge from SSE events to `activity_log` rows. No config wiring yet; that's Phase C.

---

### Task 4: `ActivityRecorder` skeleton + one event type (`review_completed`) — TDD

**Files:**
- Create: `daemon/internal/activity/recorder.go`
- Create: `daemon/internal/activity/recorder_test.go`

- [ ] **Step 1: Write failing test for `review_completed` → row**

Create `daemon/internal/activity/recorder_test.go`:

```go
package activity_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/activity"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

type recordedInsert struct {
	ts                             time.Time
	org, repo, itemType            string
	itemNumber                     int
	itemTitle, action, outcome     string
	details                        map[string]any
}

type fakeStore struct {
	inserts []recordedInsert
	failNext bool
}

func (f *fakeStore) InsertActivity(
	ts time.Time, org, repo, itemType string, itemNumber int,
	itemTitle, action, outcome string, details map[string]any,
) (int64, error) {
	if f.failNext {
		f.failNext = false
		return 0, assertErr("store full")
	}
	f.inserts = append(f.inserts, recordedInsert{ts, org, repo, itemType, itemNumber, itemTitle, action, outcome, details})
	return int64(len(f.inserts)), nil
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

	// Give the goroutine a tick.
	waitFor(t, func() bool { return len(fs.inserts) == 1 })

	got := fs.inserts[0]
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

// Ensure the store interface the recorder accepts is compatible with the real
// Store type (compile-time check).
var _ activity.Store = (*store.Store)(nil)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test-docker GO_TEST_ARGS="-run TestRecorder_ReviewCompleted ./internal/activity/..."`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `recorder.go`**

Create `daemon/internal/activity/recorder.go`:

```go
// Package activity records a row in the activity_log table for every
// significant Heimdallm action emitted on the SSE broker. The recorder
// subscribes once on Start and runs until its context is cancelled.
//
// Failure mode: log + drop. The activity log is observability, so a
// failed insert (disk full, locked DB) must never block the publisher.
package activity

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/sse"
)

// Store is the subset of *store.Store the recorder uses. Kept as a local
// interface so tests can inject fakes without importing the real store.
type Store interface {
	InsertActivity(ts time.Time, org, repo, itemType string, itemNumber int,
		itemTitle, action, outcome string, details map[string]any) (int64, error)
}

// Recorder consumes SSE events and writes activity rows.
type Recorder struct {
	store  Store
	events chan sse.Event
}

// New subscribes to the broker and returns a recorder ready to Start.
// Returns nil if the broker has reached its subscriber limit; the caller
// should log and continue without the recorder (activity is optional).
func New(s Store, broker *sse.Broker) *Recorder {
	ch := broker.Subscribe()
	if ch == nil {
		return nil
	}
	return &Recorder{store: s, events: ch}
}

// NewWithChannel is a test hook. Production code uses New.
func NewWithChannel(s Store, ch chan sse.Event) *Recorder {
	return &Recorder{store: s, events: ch}
}

// Start runs the event loop. Returns when ctx is cancelled or the event
// channel is closed.
func (r *Recorder) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-r.events:
			if !ok {
				return
			}
			if err := r.handle(ev); err != nil {
				slog.Warn("activity: record failed", "err", err, "event", ev.Type)
			}
		}
	}
}

func (r *Recorder) handle(ev sse.Event) error {
	switch ev.Type {
	case sse.EventReviewCompleted:
		return r.recordReviewCompleted(ev)
	default:
		return nil // unknown/ignored event type
	}
}

// payload helpers ----------------------------------------------------------

func decode(data string, v any) error {
	return json.Unmarshal([]byte(data), v)
}

func orgOf(repo string) string {
	i := strings.IndexByte(repo, '/')
	if i < 0 {
		return repo
	}
	return repo[:i]
}

// event handlers -----------------------------------------------------------

func (r *Recorder) recordReviewCompleted(ev sse.Event) error {
	var p struct {
		Repo              string `json:"repo"`
		PRNumber          int    `json:"pr_number"`
		PRTitle           string `json:"pr_title"`
		CLIUsed           string `json:"cli_used"`
		Severity          string `json:"severity"`
		ReviewID          int64  `json:"review_id"`
		GitHubReviewState string `json:"github_review_state"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	details := map[string]any{
		"cli_used":            p.CLIUsed,
		"review_id":           p.ReviewID,
		"github_review_state": p.GitHubReviewState,
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.PRNumber, p.PRTitle, "review", p.Severity, details)
	return err
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `make test-docker GO_TEST_ARGS="./internal/activity/..."`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/activity/recorder.go daemon/internal/activity/recorder_test.go
git commit -m "feat(daemon): activity recorder with review_completed handler"
```

---

### Task 5: Handlers for remaining event types — TDD

**Files:**
- Modify: `daemon/internal/activity/recorder.go`
- Modify: `daemon/internal/activity/recorder_test.go`

- [ ] **Step 1: Write failing tests for all remaining event types**

Append to `recorder_test.go`:

```go
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
	waitFor(t, func() bool { return len(fs.inserts) == 1 })

	got := fs.inserts[0]
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
		"repo":           "acme/api",
		"issue_number":   12,
		"issue_title":    "Refactor auth",
		"cli_used":       "claude",
		"severity":       "major",
		"category":       "develop",
		"chosen_action":  "auto_implement",
	})
	events <- sse.Event{Type: sse.EventIssueReviewCompleted, Data: string(payload)}
	waitFor(t, func() bool { return len(fs.inserts) == 1 })

	got := fs.inserts[0]
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
	waitFor(t, func() bool { return len(fs.inserts) == 1 })

	got := fs.inserts[0]
	if got.action != "implement" || got.outcome != "pr_opened" {
		t.Errorf("action/outcome: %s/%s", got.action, got.outcome)
	}
	if got.details["pr_number"] != float64(99) {
		t.Errorf("details pr_number: %v", got.details["pr_number"])
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
	waitFor(t, func() bool { return len(fs.inserts) == 1 })

	got := fs.inserts[0]
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
	waitFor(t, func() bool { return len(fs.inserts) == 1 })

	got := fs.inserts[0]
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
	// Sleep a bit — no row should appear.
	time.Sleep(50 * time.Millisecond)
	if len(fs.inserts) != 0 {
		t.Errorf("expected 0 inserts, got %d", len(fs.inserts))
	}
}

func TestRecorder_StoreFailureIsLoggedAndDropped(t *testing.T) {
	_, fs, events := newTestRecorder(t)
	fs.failNext = true

	payload, _ := json.Marshal(map[string]any{
		"repo": "acme/api", "pr_number": 1, "pr_title": "t",
		"cli_used": "claude", "severity": "minor",
	})
	events <- sse.Event{Type: sse.EventReviewCompleted, Data: string(payload)}

	// The next event should still succeed — recorder must not have panicked.
	time.Sleep(30 * time.Millisecond)
	events <- sse.Event{Type: sse.EventReviewCompleted, Data: string(payload)}
	waitFor(t, func() bool { return len(fs.inserts) == 1 })
}
```

- [ ] **Step 2: Run tests, confirm they fail**

Run: `make test-docker GO_TEST_ARGS="./internal/activity/..."`
Expected: FAIL for the new tests (no handlers yet); `TestRecorder_ReviewCompleted` still passes.

- [ ] **Step 3: Add the remaining handlers to `recorder.go`**

In `recorder.go`, extend `handle`:

```go
func (r *Recorder) handle(ev sse.Event) error {
	switch ev.Type {
	case sse.EventReviewCompleted:
		return r.recordReviewCompleted(ev)
	case sse.EventReviewError:
		return r.recordReviewError(ev)
	case sse.EventIssueReviewCompleted:
		return r.recordIssueTriage(ev)
	case sse.EventIssueImplemented:
		return r.recordIssueImplemented(ev)
	case sse.EventIssueReviewError:
		return r.recordIssueReviewError(ev)
	case sse.EventIssuePromoted:
		return r.recordIssuePromoted(ev)
	default:
		return nil
	}
}
```

Add the handlers:

```go
func (r *Recorder) recordReviewError(ev sse.Event) error {
	var p struct {
		Repo     string `json:"repo"`
		PRNumber int    `json:"pr_number"`
		PRTitle  string `json:"pr_title"`
		CLIUsed  string `json:"cli_used"`
		Error    string `json:"error"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.PRNumber, p.PRTitle, "error", p.Error, map[string]any{
			"item_type": "pr",
			"cli_used":  p.CLIUsed,
			"error":     p.Error,
		})
	return err
}

func (r *Recorder) recordIssueTriage(ev sse.Event) error {
	var p struct {
		Repo         string `json:"repo"`
		IssueNumber  int    `json:"issue_number"`
		IssueTitle   string `json:"issue_title"`
		CLIUsed      string `json:"cli_used"`
		Severity     string `json:"severity"`
		Category     string `json:"category"`
		ChosenAction string `json:"chosen_action"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	outcome := p.Severity
	if outcome == "" {
		outcome = "ignored"
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		p.IssueNumber, p.IssueTitle, "triage", outcome, map[string]any{
			"cli_used":      p.CLIUsed,
			"category":      p.Category,
			"chosen_action": p.ChosenAction,
		})
	return err
}

func (r *Recorder) recordIssueImplemented(ev sse.Event) error {
	var p struct {
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
		IssueTitle  string `json:"issue_title"`
		CLIUsed     string `json:"cli_used"`
		PRNumber    int    `json:"pr_number"`
		PRURL       string `json:"pr_url"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	outcome := "pr_opened"
	if p.PRNumber == 0 {
		outcome = "pr_failed"
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		p.IssueNumber, p.IssueTitle, "implement", outcome, map[string]any{
			"cli_used":  p.CLIUsed,
			"pr_number": p.PRNumber,
			"pr_url":    p.PRURL,
		})
	return err
}

func (r *Recorder) recordIssueReviewError(ev sse.Event) error {
	var p struct {
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
		IssueTitle  string `json:"issue_title"`
		CLIUsed     string `json:"cli_used"`
		Error       string `json:"error"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		p.IssueNumber, p.IssueTitle, "error", p.Error, map[string]any{
			"item_type": "issue",
			"cli_used":  p.CLIUsed,
			"error":     p.Error,
		})
	return err
}

func (r *Recorder) recordIssuePromoted(ev sse.Event) error {
	var p struct {
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
		IssueTitle  string `json:"issue_title"`
		FromLabel   string `json:"from_label"`
		ToLabel     string `json:"to_label"`
		Reason      string `json:"reason"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		p.IssueNumber, p.IssueTitle, "promote",
		p.FromLabel+" → "+p.ToLabel, map[string]any{
			"from_label": p.FromLabel,
			"to_label":   p.ToLabel,
			"reason":     p.Reason,
		})
	return err
}
```

- [ ] **Step 4: Run all tests, confirm pass**

Run: `make test-docker GO_TEST_ARGS="./internal/activity/..."`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/activity/recorder.go daemon/internal/activity/recorder_test.go
git commit -m "feat(daemon): activity recorder handlers for all broker events"
```

---

## Phase C — Config + retention + wiring (PR 3 part 2)

Adds the config surface, retention, and the main.go wiring that actually starts the recorder.

---

### Task 6: `ActivityLogConfig` + parsing + store layer

**Files:**
- Modify: `daemon/internal/config/config.go`
- Modify: `daemon/internal/config/store.go`
- Modify: `daemon/internal/config/config_test.go`

- [ ] **Step 1: Write failing tests**

Add to `config_test.go`:

```go
func TestActivityLogConfig_Defaults(t *testing.T) {
	c, err := Load(writeTempTOML(t, `
[server]
port = 9180
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !c.ActivityLog.Enabled {
		t.Error("ActivityLog.Enabled should default to true")
	}
	if c.ActivityLog.RetentionDays != 90 {
		t.Errorf("RetentionDays = %d, want 90", c.ActivityLog.RetentionDays)
	}
}

func TestActivityLogConfig_TOML(t *testing.T) {
	c, err := Load(writeTempTOML(t, `
[activity_log]
enabled = false
retention_days = 30
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.ActivityLog.Enabled {
		t.Error("ActivityLog.Enabled should be false")
	}
	if c.ActivityLog.RetentionDays != 30 {
		t.Errorf("RetentionDays = %d, want 30", c.ActivityLog.RetentionDays)
	}
}

func TestActivityLogConfig_StoreLayer(t *testing.T) {
	c := &Config{}
	c.ActivityLog.Enabled = true
	c.ActivityLog.RetentionDays = 90

	if err := c.ApplyStore(map[string]string{
		"activity_log_enabled":        "false",
		"activity_log_retention_days": "45",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.ActivityLog.Enabled {
		t.Error("enabled should be false after store override")
	}
	if c.ActivityLog.RetentionDays != 45 {
		t.Errorf("retention_days = %d, want 45", c.ActivityLog.RetentionDays)
	}
}

func TestActivityLogConfig_RetentionValidation(t *testing.T) {
	tests := []struct {
		days    int
		wantErr bool
	}{
		{0, false},       // 0 is no-op, valid
		{1, false},
		{3650, false},
		{-1, true},
		{3651, true},
	}
	for _, tt := range tests {
		c := &Config{}
		c.ActivityLog.Enabled = true
		c.ActivityLog.RetentionDays = tt.days
		err := c.Validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("days=%d: err=%v wantErr=%v", tt.days, err, tt.wantErr)
		}
	}
}
```

Use the same `writeTempTOML` helper that other tests in this file use. If it doesn't exist, look for the idiomatic load helper and reuse it.

- [ ] **Step 2: Run tests, confirm fail**

Run: `make test-docker GO_TEST_ARGS="-run TestActivityLog ./internal/config/..."`
Expected: FAIL — `ActivityLog` field undefined.

- [ ] **Step 3: Add `ActivityLogConfig` type and `Config.ActivityLog` field**

In `daemon/internal/config/config.go`:

1. Add to the `Config` struct:

```go
type Config struct {
	Server      ServerConfig      `toml:"server"`
	GitHub      GitHubConfig      `toml:"github"`
	AI          AIConfig          `toml:"ai"`
	Retention   RetentionConfig   `toml:"retention"`
	ActivityLog ActivityLogConfig `toml:"activity_log"`
}
```

2. Add the config type — place it near `RetentionConfig`:

```go
// ActivityLogConfig controls the daily activity log feature (#113).
// When enabled, the daemon records a row per significant action
// (review, triage, implement, promote, error) into activity_log.
type ActivityLogConfig struct {
	Enabled       bool `toml:"enabled"`
	RetentionDays int  `toml:"retention_days"`
}
```

3. Populate defaults in whichever helper normalises zero-values after `toml.Decode` (commonly a `SetDefaults` or `applyDefaults` function). If defaults are applied at the top of `Load` instead, add both lines there. Look for the analogous lines that default `Retention.MaxDays`:

```go
if !c.ActivityLog.explicitlySet() {
	// TOML absence → default on.
	c.ActivityLog.Enabled = true
}
if c.ActivityLog.RetentionDays == 0 {
	c.ActivityLog.RetentionDays = 90
}
```

Note: the "enabled defaults to true" behaviour is tricky because `bool` zero-value is `false`. The pattern that works cleanly in this codebase is to use a `*bool` pointer OR to track presence via a parallel "set" flag. Inspect how other bool-with-true-default fields (if any) are handled. If there isn't an existing pattern, introduce a small helper:

```go
// Use a "defaults after decode" helper that treats a missing [activity_log] table as present-with-defaults.
func (c *Config) applyActivityLogDefaults(raw toml.MetaData) {
	if !raw.IsDefined("activity_log", "enabled") {
		c.ActivityLog.Enabled = true
	}
	if !raw.IsDefined("activity_log", "retention_days") {
		c.ActivityLog.RetentionDays = 90
	}
}
```

Call it from `Load` after `toml.Decode`, passing the returned `MetaData`. (The BurntSushi/toml package returns this from `Decode`.)

4. Add to `Validate()`:

```go
if c.ActivityLog.RetentionDays < 0 || c.ActivityLog.RetentionDays > 3650 {
	return fmt.Errorf("activity_log.retention_days must be between 0 and 3650, got %d", c.ActivityLog.RetentionDays)
}
```

- [ ] **Step 4: Add store-layer cases in `config/store.go`**

In the `ApplyStore` switch, add:

```go
case "activity_log_enabled":
	var enabled bool
	if err := json.Unmarshal([]byte(raw), &enabled); err != nil {
		return fmt.Errorf("config: apply store key %q: %w", key, err)
	}
	shadow.ActivityLog.Enabled = enabled
case "activity_log_retention_days":
	var days int
	if err := json.Unmarshal([]byte(raw), &days); err != nil {
		return fmt.Errorf("config: apply store key %q: %w", key, err)
	}
	shadow.ActivityLog.RetentionDays = days
```

- [ ] **Step 5: Run tests, confirm pass**

Run: `make test-docker GO_TEST_ARGS="./internal/config/..."`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add daemon/internal/config/config.go daemon/internal/config/store.go daemon/internal/config/config_test.go
git commit -m "feat(daemon): activity_log config section with defaults and store layer"
```

---

### Task 7: Wire `ActivityRecorder` into `main.go` + startup purge + ticker

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`
- Modify: `daemon/cmd/heimdallm/main.go` (same file, two logical edits)

- [ ] **Step 1: Construct recorder after broker, start it**

In `daemon/cmd/heimdallm/main.go`, right after `broker.Start()` (around line 116):

```go
// ActivityRecorder subscribes to the broker and writes a row into
// activity_log for every significant event. Disabled → not constructed.
// A nil broker subscription (subscriber cap reached) is treated as a
// warning and the daemon continues without the recorder.
var activityRecorder *activity.Recorder
if cfg.ActivityLog.Enabled {
	activityRecorder = activity.New(s, broker)
	if activityRecorder == nil {
		slog.Warn("activity: broker subscriber cap reached; activity log will not record this session")
	} else {
		activityCtx, activityCancel := context.WithCancel(context.Background())
		defer activityCancel()
		go activityRecorder.Start(activityCtx)
		slog.Info("activity recorder started")
	}
}
_ = activityRecorder // keep for Stop-on-shutdown wiring if added later
```

Add the import `"github.com/heimdallm/daemon/internal/activity"` to the import block.

- [ ] **Step 2: Add startup purge + 24h ticker**

Add after the existing `PurgeOldReviews` call:

```go
if err := s.PurgeOldActivity(cfg.ActivityLog.RetentionDays); err != nil {
	slog.Warn("activity retention purge failed", "err", err)
}
```

After the existing `sched` construction/start (search for `startScheduler`), add a dedicated ticker for activity purge. A minimal approach reusing the existing `scheduler.Scheduler`:

```go
activityPurge := scheduler.New(24*time.Hour, func() {
	if err := s.PurgeOldActivity(cfg.ActivityLog.RetentionDays); err != nil {
		slog.Warn("activity retention purge failed", "err", err)
	}
})
activityPurge.Start()
defer activityPurge.Stop()
```

Add `"time"` to imports if not already present.

- [ ] **Step 3: Build + run integration test (the existing `integration_test.go`)**

Run: `make test-docker GO_TEST_ARGS="./..."`
Expected: all PASS. Any build failure here means a wiring mistake (wrong receiver, missing import).

- [ ] **Step 4: Smoke-run the daemon locally (optional; skip in worktrees without creds)**

Only if you have credentials set up: run `make` or `go run ./daemon/cmd/heimdallm` and watch for `activity recorder started` in the logs. Hit a real PR or issue to see a row appear in `~/.config/heimdallm/heimdallm.db` (or wherever `dataDir()` resolves to — check `main.go`).

- [ ] **Step 5: Commit**

```bash
git add daemon/cmd/heimdallm/main.go
git commit -m "feat(daemon): wire ActivityRecorder, startup purge, and 24h retention ticker"
```

At this point PR 3 is complete: recorder produces rows whenever the daemon runs.

---

## Phase D — HTTP endpoint (PR 4)

Expose the table through `GET /activity` so clients (the Flutter tab, curl, any other consumer) can query it.

---

### Task 8: `GET /activity` handler — TDD

**Files:**
- Modify: `daemon/internal/server/handlers.go`
- Modify: `daemon/internal/server/handlers_test.go`

- [ ] **Step 1: Write failing test for happy path**

Add to `handlers_test.go`:

```go
func TestHandleActivity_HappyPath(t *testing.T) {
	s := newTestStore(t) // use whatever store helper other handler tests use
	// Seed two rows: today and yesterday.
	today := time.Now()
	yesterday := today.Add(-24 * time.Hour)
	if _, err := s.InsertActivity(yesterday, "acme", "acme/api", "pr", 1, "Old", "review", "minor", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertActivity(today, "acme", "acme/api", "pr", 2, "New", "review", "major", map[string]any{"cli_used": "claude"}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.ActivityLog.Enabled = true
	srv := newTestServerWithConfig(t, s, cfg)

	req := httptest.NewRequest("GET", "/activity", nil)
	req.Header.Set("X-Heimdallm-Token", testAPIToken)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct {
			Repo    string                 `json:"repo"`
			Action  string                 `json:"action"`
			Outcome string                 `json:"outcome"`
			Details map[string]any         `json:"details"`
		} `json:"entries"`
		Count     int  `json:"count"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// Default is "today" only → one entry.
	if len(resp.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(resp.Entries))
	}
	if resp.Entries[0].Outcome != "major" {
		t.Errorf("outcome = %q", resp.Entries[0].Outcome)
	}
	if resp.Entries[0].Details["cli_used"] != "claude" {
		t.Errorf("details: %+v", resp.Entries[0].Details)
	}
}

func TestHandleActivity_BadDateFormat(t *testing.T) {
	s := newTestStore(t)
	cfg := &config.Config{}
	cfg.ActivityLog.Enabled = true
	srv := newTestServerWithConfig(t, s, cfg)

	req := httptest.NewRequest("GET", "/activity?date=2026/04/20", nil)
	req.Header.Set("X-Heimdallm-Token", testAPIToken)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_MixedDateAndRange(t *testing.T) {
	// date + from/to together is invalid.
	s := newTestStore(t)
	cfg := &config.Config{}
	cfg.ActivityLog.Enabled = true
	srv := newTestServerWithConfig(t, s, cfg)

	req := httptest.NewRequest("GET", "/activity?date=2026-04-20&from=2026-04-19&to=2026-04-20", nil)
	req.Header.Set("X-Heimdallm-Token", testAPIToken)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_DisabledReturns503(t *testing.T) {
	s := newTestStore(t)
	cfg := &config.Config{}
	cfg.ActivityLog.Enabled = false
	srv := newTestServerWithConfig(t, s, cfg)

	req := httptest.NewRequest("GET", "/activity", nil)
	req.Header.Set("X-Heimdallm-Token", testAPIToken)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != 503 {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleActivity_RequiresAuth(t *testing.T) {
	s := newTestStore(t)
	cfg := &config.Config{}
	cfg.ActivityLog.Enabled = true
	srv := newTestServerWithConfig(t, s, cfg)

	req := httptest.NewRequest("GET", "/activity", nil)
	// no token
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
```

If `newTestServerWithConfig` does not exist yet, look for the equivalent helper (probably `newTestServer` taking only the store). Add a variant that threads a `*config.Config` through so the handler can consult `ActivityLog.Enabled`. Thread it via a `configFn` closure, matching the existing pattern (`srv.configFn` already exists per `handlers.go` line 39).

- [ ] **Step 2: Run tests, confirm fail**

Run: `make test-docker GO_TEST_ARGS="-run TestHandleActivity ./internal/server/..."`
Expected: FAIL — route not registered.

- [ ] **Step 3: Add the handler**

In `daemon/internal/server/handlers.go`:

1. Add `/activity` to `sensitiveGETPaths`:

```go
var sensitiveGETPaths = []string{
	"/activity",
	"/config",
	"/agents",
	// ... existing entries
}
```

2. Register the route in `buildRouter` (near the other `r.Get` calls, around line 198):

```go
r.Get("/activity", srv.handleActivity)
```

3. Add the handler — place it after `handleStats`:

```go
// handleActivity returns rows from activity_log matching the query.
// Params (all optional, combined with AND):
//   date=YYYY-MM-DD                   — single day window in daemon local TZ
//   from=YYYY-MM-DD & to=YYYY-MM-DD   — inclusive range in daemon local TZ
//   org=...  (repeatable)             — org filter
//   repo=... (repeatable)             — repo filter
//   action=review|triage|implement|promote|error (repeatable)
//   limit=N (default 500, max 5000)
//
// Default when neither date nor from/to is supplied: today.
func (srv *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	// 503 when disabled.
	if cfg := srv.configFn(); cfg != nil {
		enabled, _ := cfg["activity_log_enabled"].(bool)
		if !enabled {
			http.Error(w, `{"error":"activity log disabled"}`, http.StatusServiceUnavailable)
			return
		}
	}

	q := r.URL.Query()

	date := q.Get("date")
	from := q.Get("from")
	to := q.Get("to")
	if date != "" && (from != "" || to != "") {
		httpErr(w, http.StatusBadRequest, "date cannot be combined with from/to")
		return
	}
	if (from == "") != (to == "") {
		httpErr(w, http.StatusBadRequest, "from and to must be supplied together")
		return
	}

	var window time.Time
	var start, end time.Time
	loc := time.Now().Location() // daemon local TZ

	parseDay := func(s string) (time.Time, error) {
		return time.ParseInLocation("2006-01-02", s, loc)
	}

	switch {
	case date != "":
		d, err := parseDay(date)
		if err != nil {
			httpErr(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
			return
		}
		start = d
		end = d.Add(24 * time.Hour).Add(-time.Second)
	case from != "":
		f, err := parseDay(from)
		if err != nil {
			httpErr(w, http.StatusBadRequest, "from must be YYYY-MM-DD")
			return
		}
		t2, err := parseDay(to)
		if err != nil {
			httpErr(w, http.StatusBadRequest, "to must be YYYY-MM-DD")
			return
		}
		if t2.Before(f) {
			httpErr(w, http.StatusBadRequest, "to must not be before from")
			return
		}
		start = f
		end = t2.Add(24 * time.Hour).Add(-time.Second)
	default:
		// Default: today.
		now := time.Now()
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		end = start.Add(24 * time.Hour).Add(-time.Second)
	}
	_ = window // placeholder to satisfy unused var rule if compiler complains

	limit := 500
	if ls := q.Get("limit"); ls != "" {
		n, err := strconv.Atoi(ls)
		if err != nil || n < 1 || n > 5000 {
			httpErr(w, http.StatusBadRequest, "limit must be 1..5000")
			return
		}
		limit = n
	}

	entries, truncated, err := srv.store.ListActivity(store.ActivityQuery{
		From:    start,
		To:      end,
		Orgs:    q["org"],
		Repos:   q["repo"],
		Actions: q["action"],
		Limit:   limit,
	})
	if err != nil {
		slog.Error("activity: list failed", "err", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	// Build response. Keep `details` as parsed JSON, not a string.
	type entryOut struct {
		ID         int64          `json:"id"`
		TS         string         `json:"ts"`
		Org        string         `json:"org"`
		Repo       string         `json:"repo"`
		ItemType   string         `json:"item_type"`
		ItemNumber int            `json:"item_number"`
		ItemTitle  string         `json:"item_title"`
		Action     string         `json:"action"`
		Outcome    string         `json:"outcome"`
		Details    map[string]any `json:"details"`
	}
	out := make([]entryOut, 0, len(entries))
	for _, a := range entries {
		var details map[string]any
		if a.DetailsJSON != "" {
			_ = json.Unmarshal([]byte(a.DetailsJSON), &details)
		}
		if details == nil {
			details = map[string]any{}
		}
		out = append(out, entryOut{
			ID:         a.ID,
			TS:         a.Timestamp.In(loc).Format(time.RFC3339),
			Org:        a.Org,
			Repo:       a.Repo,
			ItemType:   a.ItemType,
			ItemNumber: a.ItemNumber,
			ItemTitle:  a.ItemTitle,
			Action:     a.Action,
			Outcome:    a.Outcome,
			Details:    details,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":   out,
		"count":     len(out),
		"truncated": truncated,
	})
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
```

If `writeJSON` already exists in `handlers.go`, use it. If the helper is named differently, match the existing idiom. If `httpErr` conflicts with an existing helper, rename.

4. Ensure `configFn` populates `activity_log_enabled`. Find where the existing `configFn` returns its map (search for the handler/factory that sets `srv.configFn = ...` — probably in `main.go`). Add:

```go
"activity_log_enabled":         c.ActivityLog.Enabled,
"activity_log_retention_days":  c.ActivityLog.RetentionDays,
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `make test-docker GO_TEST_ARGS="-run TestHandleActivity ./internal/server/..."`
Expected: all PASS.

Run: `make test-docker`
Expected: full suite PASS (including `integration_test.go`).

- [ ] **Step 5: Manually hit the endpoint (optional)**

If the daemon is running locally:

```bash
curl -s -H "X-Heimdallm-Token: $(cat ~/.config/heimdallm/api-token)" \
  "http://127.0.0.1:9180/activity?date=$(date +%Y-%m-%d)" | jq '.count'
```

Expected: numeric count (0 is fine on a fresh install).

- [ ] **Step 6: Commit**

```bash
git add daemon/internal/server/handlers.go daemon/internal/server/handlers_test.go daemon/cmd/heimdallm/main.go
git commit -m "feat(daemon): GET /activity endpoint with filters and truncation"
```

PR 4 complete.

---

## Phase E — Flutter Activity tab (PR 5)

Consumes `GET /activity` in a new top-level tab.

Flutter tests run natively (no Docker required) per `AGENTS.md`: `cd flutter_app && flutter test`.

---

### Task 9: Models and API client — TDD

**Files:**
- Create: `flutter_app/lib/features/activity/activity_models.dart`
- Create: `flutter_app/lib/features/activity/activity_api.dart`
- Create: `flutter_app/test/features/activity/activity_models_test.dart`

- [ ] **Step 1: Write failing model test**

Create `flutter_app/test/features/activity/activity_models_test.dart`:

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm_app/features/activity/activity_models.dart';

void main() {
  test('ActivityEntry.fromJson parses a full entry', () {
    final json = {
      'id': 1,
      'ts': '2026-04-20T09:34:12+02:00',
      'org': 'acme',
      'repo': 'acme/api',
      'item_type': 'pr',
      'item_number': 42,
      'item_title': 'Fix bug',
      'action': 'review',
      'outcome': 'major',
      'details': {'cli_used': 'claude'},
    };
    final e = ActivityEntry.fromJson(json);
    expect(e.id, 1);
    expect(e.repo, 'acme/api');
    expect(e.itemNumber, 42);
    expect(e.action, ActivityAction.review);
    expect(e.outcome, 'major');
    expect(e.details['cli_used'], 'claude');
    expect(e.timestamp.isUtc, false);
  });

  test('ActivityAction.unknown for unexpected values', () {
    final e = ActivityEntry.fromJson({
      'id': 1, 'ts': '2026-04-20T09:34:12+02:00', 'org': 'a', 'repo': 'a/b',
      'item_type': 'pr', 'item_number': 1, 'item_title': 't',
      'action': 'frobnicate', 'outcome': '', 'details': {},
    });
    expect(e.action, ActivityAction.unknown);
  });
}
```

Note: the import path `package:heimdallm_app/...` — verify by opening `flutter_app/pubspec.yaml` and using the correct `name:` value.

- [ ] **Step 2: Run test, confirm fail**

Run: `cd flutter_app && flutter test test/features/activity/activity_models_test.dart`
Expected: FAIL — file does not exist.

- [ ] **Step 3: Create `activity_models.dart`**

```dart
/// Known activity actions. Keeps switch statements exhaustive and keeps the
/// timeline UI from rendering a bare enum toString() to the user.
enum ActivityAction { review, triage, implement, promote, error, unknown }

ActivityAction _parseAction(String s) {
  switch (s) {
    case 'review':    return ActivityAction.review;
    case 'triage':    return ActivityAction.triage;
    case 'implement': return ActivityAction.implement;
    case 'promote':   return ActivityAction.promote;
    case 'error':     return ActivityAction.error;
    default:          return ActivityAction.unknown;
  }
}

class ActivityEntry {
  final int id;
  final DateTime timestamp;
  final String org;
  final String repo;
  final String itemType; // 'pr' | 'issue'
  final int itemNumber;
  final String itemTitle;
  final ActivityAction action;
  final String outcome;
  final Map<String, dynamic> details;

  const ActivityEntry({
    required this.id,
    required this.timestamp,
    required this.org,
    required this.repo,
    required this.itemType,
    required this.itemNumber,
    required this.itemTitle,
    required this.action,
    required this.outcome,
    required this.details,
  });

  factory ActivityEntry.fromJson(Map<String, dynamic> json) {
    return ActivityEntry(
      id:         json['id'] as int,
      timestamp:  DateTime.parse(json['ts'] as String).toLocal(),
      org:        json['org'] as String,
      repo:       json['repo'] as String,
      itemType:   json['item_type'] as String,
      itemNumber: json['item_number'] as int,
      itemTitle:  json['item_title'] as String,
      action:     _parseAction(json['action'] as String),
      outcome:    json['outcome'] as String? ?? '',
      details:    (json['details'] as Map?)?.cast<String, dynamic>() ?? {},
    );
  }
}

/// Query used by the providers and API layer. All fields optional.
class ActivityQuery {
  final DateTime? date;
  final DateTime? from;
  final DateTime? to;
  final Set<String> orgs;
  final Set<String> repos;
  final Set<ActivityAction> actions;
  final int limit;

  const ActivityQuery({
    this.date, this.from, this.to,
    this.orgs = const {}, this.repos = const {}, this.actions = const {},
    this.limit = 500,
  });

  Map<String, List<String>> toQueryParameters() {
    final String Function(DateTime) ymd = (d) =>
      '${d.year.toString().padLeft(4, '0')}-${d.month.toString().padLeft(2, '0')}-${d.day.toString().padLeft(2, '0')}';
    final params = <String, List<String>>{};
    if (date != null) {
      params['date'] = [ymd(date!)];
    } else if (from != null && to != null) {
      params['from'] = [ymd(from!)];
      params['to']   = [ymd(to!)];
    }
    if (orgs.isNotEmpty)   params['org']   = orgs.toList();
    if (repos.isNotEmpty)  params['repo']  = repos.toList();
    if (actions.isNotEmpty) {
      params['action'] = actions.map((a) => a.name).toList();
    }
    params['limit'] = [limit.toString()];
    return params;
  }
}
```

- [ ] **Step 4: Run test, confirm pass**

Run: `cd flutter_app && flutter test test/features/activity/activity_models_test.dart`
Expected: PASS.

- [ ] **Step 5: Create `activity_api.dart`**

Match the pattern used by other API clients — look at `lib/core/api/` for existing HTTP wrappers and reuse the token/base-URL source.

```dart
import 'dart:convert';
import 'package:http/http.dart' as http;

import '../../core/api/api_client.dart';   // adapt to the real import path
import 'activity_models.dart';

class ActivityPage {
  final List<ActivityEntry> entries;
  final bool truncated;
  final int count;
  ActivityPage({required this.entries, required this.truncated, required this.count});
}

class ActivityApi {
  final ApiClient _client;
  ActivityApi(this._client);

  Future<ActivityPage> list(ActivityQuery q) async {
    final uri = _client.baseUri.replace(
      path: '/activity',
      queryParameters: q.toQueryParameters(),
    );
    final res = await _client.get(uri);
    if (res.statusCode == 503) {
      throw ActivityDisabledException();
    }
    if (res.statusCode != 200) {
      throw http.ClientException('activity: HTTP ${res.statusCode} — ${res.body}', uri);
    }
    final body = jsonDecode(res.body) as Map<String, dynamic>;
    final list = (body['entries'] as List)
      .map((e) => ActivityEntry.fromJson(e as Map<String, dynamic>))
      .toList();
    return ActivityPage(
      entries: list,
      truncated: body['truncated'] as bool? ?? false,
      count: body['count'] as int? ?? list.length,
    );
  }
}

class ActivityDisabledException implements Exception {
  @override
  String toString() => 'Activity log is disabled in daemon config';
}
```

Inspect `api_client.dart` (or equivalent) to confirm the method names (`get`, `baseUri`, token header handling) and adjust. Do not re-invent token injection — reuse the project's pattern.

- [ ] **Step 6: Commit**

```bash
git add flutter_app/lib/features/activity/activity_models.dart flutter_app/lib/features/activity/activity_api.dart flutter_app/test/features/activity/activity_models_test.dart
git commit -m "feat(flutter): activity models and API client"
```

---

### Task 10: Providers (Riverpod) — TDD

**Files:**
- Create: `flutter_app/lib/features/activity/activity_providers.dart`
- Create: `flutter_app/test/features/activity/activity_providers_test.dart`

- [ ] **Step 1: Inspect existing provider pattern**

Open `lib/features/dashboard/dashboard_providers.dart`. Match the exact Riverpod flavour (plain `Provider`, `StateNotifierProvider`, `AsyncNotifierProvider`, etc.). Below uses `StateNotifier`; adjust if the codebase uses `AsyncNotifier`.

- [ ] **Step 2: Write failing provider test**

Create `flutter_app/test/features/activity/activity_providers_test.dart`:

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm_app/features/activity/activity_models.dart';
import 'package:heimdallm_app/features/activity/activity_providers.dart';

void main() {
  test('default query is "today"', () {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    final q = container.read(activityQueryProvider);
    final today = DateTime.now();
    expect(q.date?.year,  today.year);
    expect(q.date?.month, today.month);
    expect(q.date?.day,   today.day);
  });

  test('toggleAction adds and removes', () {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    final notifier = container.read(activityQueryProvider.notifier);

    notifier.toggleAction(ActivityAction.review);
    expect(container.read(activityQueryProvider).actions, {ActivityAction.review});

    notifier.toggleAction(ActivityAction.review);
    expect(container.read(activityQueryProvider).actions, isEmpty);
  });
}
```

- [ ] **Step 3: Run test, confirm fail**

Run: `cd flutter_app && flutter test test/features/activity/activity_providers_test.dart`
Expected: FAIL — file does not exist.

- [ ] **Step 4: Implement providers**

```dart
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'activity_api.dart';
import 'activity_models.dart';
// import '../../core/api/api_client.dart';   // adapt path

/// Holds the current query. Widgets read this to build the request;
/// the entries provider watches it and refetches on change.
class ActivityQueryNotifier extends StateNotifier<ActivityQuery> {
  ActivityQueryNotifier() : super(_today());

  static ActivityQuery _today() {
    final now = DateTime.now();
    return ActivityQuery(date: DateTime(now.year, now.month, now.day));
  }

  void setDate(DateTime day) =>
    state = ActivityQuery(date: DateTime(day.year, day.month, day.day),
                          orgs: state.orgs, repos: state.repos,
                          actions: state.actions, limit: state.limit);

  void setRange(DateTime from, DateTime to) =>
    state = ActivityQuery(from: DateTime(from.year, from.month, from.day),
                          to:   DateTime(to.year, to.month, to.day),
                          orgs: state.orgs, repos: state.repos,
                          actions: state.actions, limit: state.limit);

  void toggleOrg(String org) =>
    _replace(orgs: _toggled(state.orgs, org));
  void toggleRepo(String repo) =>
    _replace(repos: _toggled(state.repos, repo));
  void toggleAction(ActivityAction a) =>
    _replace(actions: _toggled(state.actions, a));
  void clearFilters() =>
    _replace(orgs: const {}, repos: const {}, actions: const {});

  void _replace({
    Set<String>? orgs, Set<String>? repos, Set<ActivityAction>? actions,
  }) {
    state = ActivityQuery(
      date: state.date, from: state.from, to: state.to,
      orgs: orgs ?? state.orgs,
      repos: repos ?? state.repos,
      actions: actions ?? state.actions,
      limit: state.limit,
    );
  }

  static Set<T> _toggled<T>(Set<T> set, T v) {
    final next = Set<T>.from(set);
    if (!next.add(v)) next.remove(v);
    return next;
  }
}

final activityQueryProvider =
  StateNotifierProvider<ActivityQueryNotifier, ActivityQuery>((ref) {
    return ActivityQueryNotifier();
  });

/// Wire up the API via a Provider that depends on the shared ApiClient.
/// Replace this with the real client lookup for this codebase.
final activityApiProvider = Provider<ActivityApi>((ref) {
  throw UnimplementedError('wire ApiClient here');
});

/// Entries for the current query.
final activityEntriesProvider = FutureProvider<ActivityPage>((ref) async {
  final q = ref.watch(activityQueryProvider);
  return ref.watch(activityApiProvider).list(q);
});
```

Wire `activityApiProvider` to whatever global `ApiClient` provider already exists — search `lib/core/api/` or `lib/shared/` for it.

- [ ] **Step 5: Run tests, confirm pass**

Run: `cd flutter_app && flutter test test/features/activity/activity_providers_test.dart`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add flutter_app/lib/features/activity/activity_providers.dart flutter_app/test/features/activity/activity_providers_test.dart
git commit -m "feat(flutter): activity query and entries providers"
```

---

### Task 11: Timeline widgets — TDD

**Files:**
- Create: `flutter_app/lib/features/activity/widgets/activity_entry_tile.dart`
- Create: `flutter_app/lib/features/activity/widgets/activity_filter_chips.dart`
- Create: `flutter_app/test/features/activity/activity_entry_tile_test.dart`

- [ ] **Step 1: Write failing widget test for the entry tile**

Create `flutter_app/test/features/activity/activity_entry_tile_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm_app/features/activity/activity_models.dart';
import 'package:heimdallm_app/features/activity/widgets/activity_entry_tile.dart';

void main() {
  testWidgets('renders repo, number, title, time, outcome', (tester) async {
    final entry = ActivityEntry(
      id: 1,
      timestamp: DateTime(2026, 4, 20, 9, 34, 12),
      org: 'acme',
      repo: 'acme/api',
      itemType: 'pr',
      itemNumber: 42,
      itemTitle: 'Fix rate limiter race',
      action: ActivityAction.review,
      outcome: 'major',
      details: {'cli_used': 'claude'},
    );

    await tester.pumpWidget(MaterialApp(
      home: Scaffold(body: ActivityEntryTile(entry: entry, onTap: () {})),
    ));

    expect(find.textContaining('acme/api'), findsOneWidget);
    expect(find.textContaining('#42'), findsOneWidget);
    expect(find.textContaining('Fix rate limiter race'), findsOneWidget);
    expect(find.textContaining('09:34:12'), findsOneWidget);
    expect(find.textContaining('major'), findsOneWidget);
    expect(find.byIcon(Icons.rate_review), findsOneWidget);
  });

  testWidgets('error action shows error icon', (tester) async {
    final entry = ActivityEntry(
      id: 1,
      timestamp: DateTime(2026, 4, 20, 10, 0),
      org: 'acme', repo: 'acme/api',
      itemType: 'pr', itemNumber: 1, itemTitle: 't',
      action: ActivityAction.error,
      outcome: 'cli_not_found', details: const {},
    );
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(body: ActivityEntryTile(entry: entry, onTap: () {})),
    ));
    expect(find.byIcon(Icons.error_outline), findsOneWidget);
  });
}
```

- [ ] **Step 2: Run test, confirm fail**

Run: `cd flutter_app && flutter test test/features/activity/activity_entry_tile_test.dart`
Expected: FAIL — `ActivityEntryTile` does not exist.

- [ ] **Step 3: Implement `activity_entry_tile.dart`**

```dart
import 'package:flutter/material.dart';

import '../activity_models.dart';

class ActivityEntryTile extends StatelessWidget {
  final ActivityEntry entry;
  final VoidCallback? onTap;

  const ActivityEntryTile({super.key, required this.entry, this.onTap});

  @override
  Widget build(BuildContext context) {
    final icon = _iconFor(entry.action);
    final iconColor = entry.action == ActivityAction.error
      ? Theme.of(context).colorScheme.error
      : Theme.of(context).colorScheme.primary;

    return ListTile(
      leading: Icon(icon, color: iconColor),
      title: Text(
        '${entry.repo} · #${entry.itemNumber} · ${entry.itemTitle}',
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
      subtitle: Text(_subtitle(entry)),
      trailing: Text(
        _hhmmss(entry.timestamp),
        style: const TextStyle(fontFamily: 'monospace'),
      ),
      onTap: onTap,
    );
  }

  static IconData _iconFor(ActivityAction a) {
    switch (a) {
      case ActivityAction.review:    return Icons.rate_review;
      case ActivityAction.triage:    return Icons.label;
      case ActivityAction.implement: return Icons.build;
      case ActivityAction.promote:   return Icons.swap_horiz;
      case ActivityAction.error:     return Icons.error_outline;
      case ActivityAction.unknown:   return Icons.help_outline;
    }
  }

  static String _subtitle(ActivityEntry e) {
    switch (e.action) {
      case ActivityAction.review:
        final cli = e.details['cli_used'] ?? '';
        return '${e.outcome} review${cli is String && cli.isNotEmpty ? ' by $cli' : ''}';
      case ActivityAction.triage:
        final cat = e.details['category'] ?? '';
        return 'triaged${e.outcome.isEmpty ? '' : ': ${e.outcome}'}${cat is String && cat.isNotEmpty ? ' ($cat)' : ''}';
      case ActivityAction.implement:
        final n = e.details['pr_number'];
        return n is int && n > 0 ? 'opened PR #$n' : 'implement failed';
      case ActivityAction.promote:
        return 'promoted: ${e.outcome}';
      case ActivityAction.error:
        return e.outcome;
      case ActivityAction.unknown:
        return '';
    }
  }

  static String _hhmmss(DateTime t) =>
    '${t.hour.toString().padLeft(2, '0')}:${t.minute.toString().padLeft(2, '0')}:${t.second.toString().padLeft(2, '0')}';
}
```

- [ ] **Step 4: Run test, confirm pass**

Run: `cd flutter_app && flutter test test/features/activity/activity_entry_tile_test.dart`
Expected: PASS.

- [ ] **Step 5: Implement `activity_filter_chips.dart`**

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../activity_models.dart';
import '../activity_providers.dart';

class ActivityFilterChips extends ConsumerWidget {
  final List<String> availableOrgs;
  final List<String> availableRepos;

  const ActivityFilterChips({
    super.key,
    required this.availableOrgs,
    required this.availableRepos,
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final q = ref.watch(activityQueryProvider);
    final n = ref.read(activityQueryProvider.notifier);
    final anyActive = q.orgs.isNotEmpty || q.repos.isNotEmpty || q.actions.isNotEmpty;

    return Wrap(
      spacing: 8,
      children: [
        _multiChip(context, 'Organization', q.orgs.length,
          onTap: () => _pick(context, availableOrgs, q.orgs,
            (s) => n.toggleOrg(s))),
        _multiChip(context, 'Repository', q.repos.length,
          onTap: () => _pick(context, availableRepos, q.repos,
            (s) => n.toggleRepo(s))),
        _multiChip(context, 'Action', q.actions.length,
          onTap: () => _pickActions(context, q.actions, n.toggleAction)),
        if (anyActive)
          TextButton(
            onPressed: n.clearFilters,
            child: const Text('Clear filters'),
          ),
      ],
    );
  }

  Widget _multiChip(BuildContext c, String label, int count, {required VoidCallback onTap}) {
    return ActionChip(
      label: Text(count == 0 ? label : '$label · $count'),
      onPressed: onTap,
    );
  }

  Future<void> _pick(BuildContext c, List<String> options, Set<String> selected,
                     void Function(String) toggle) async {
    await showModalBottomSheet<void>(
      context: c,
      builder: (_) => StatefulBuilder(builder: (ctx, setState) {
        return ListView(
          children: options.map((o) => CheckboxListTile(
            value: selected.contains(o),
            title: Text(o),
            onChanged: (_) { toggle(o); setState(() {}); },
          )).toList(),
        );
      }),
    );
  }

  Future<void> _pickActions(BuildContext c, Set<ActivityAction> selected,
                            void Function(ActivityAction) toggle) async {
    await showModalBottomSheet<void>(
      context: c,
      builder: (_) => StatefulBuilder(builder: (ctx, setState) {
        return ListView(
          children: [
            ActivityAction.review,
            ActivityAction.triage,
            ActivityAction.implement,
            ActivityAction.promote,
            ActivityAction.error,
          ].map((a) => CheckboxListTile(
            value: selected.contains(a),
            title: Text(a.name),
            onChanged: (_) { toggle(a); setState(() {}); },
          )).toList(),
        );
      }),
    );
  }
}
```

- [ ] **Step 6: Commit**

```bash
git add flutter_app/lib/features/activity/widgets/ flutter_app/test/features/activity/activity_entry_tile_test.dart
git commit -m "feat(flutter): activity entry tile and filter chip widgets"
```

---

### Task 12: Activity screen + route + nav tab — TDD

**Files:**
- Create: `flutter_app/lib/features/activity/activity_screen.dart`
- Modify: `flutter_app/lib/shared/router.dart`
- Modify: `flutter_app/lib/shared/nav_shell.dart` *(or the actual nav file — identify below)*
- Create: `flutter_app/test/features/activity/activity_screen_test.dart`

- [ ] **Step 1: Locate the nav shell**

```bash
grep -rn "BottomNavigationBar\|NavigationRail\|NavigationDestination" flutter_app/lib
```

Open that file — it's likely `lib/shared/nav_shell.dart` or `lib/shared/app_shell.dart`. Note which list of destinations you need to extend.

- [ ] **Step 2: Write failing widget test**

Create `flutter_app/test/features/activity/activity_screen_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm_app/features/activity/activity_api.dart';
import 'package:heimdallm_app/features/activity/activity_models.dart';
import 'package:heimdallm_app/features/activity/activity_providers.dart';
import 'package:heimdallm_app/features/activity/activity_screen.dart';

class _FakeApi implements ActivityApi {
  final ActivityPage page;
  _FakeApi(this.page);
  @override Future<ActivityPage> list(ActivityQuery q) async => page;
}

ActivityEntry _mk(int n, DateTime ts, {ActivityAction a = ActivityAction.review}) =>
  ActivityEntry(id: n, timestamp: ts, org: 'acme', repo: 'acme/api',
    itemType: 'pr', itemNumber: n, itemTitle: 'Title $n',
    action: a, outcome: 'minor', details: const {});

void main() {
  testWidgets('empty state when no entries', (tester) async {
    final api = _FakeApi(ActivityPage(entries: [], truncated: false, count: 0));
    await tester.pumpWidget(ProviderScope(
      overrides: [activityApiProvider.overrideWithValue(api)],
      child: const MaterialApp(home: ActivityScreen()),
    ));
    await tester.pumpAndSettle();
    expect(find.textContaining('No activity'), findsOneWidget);
  });

  testWidgets('groups entries by hour', (tester) async {
    final base = DateTime(2026, 4, 20, 9);
    final api = _FakeApi(ActivityPage(
      entries: [
        _mk(1, base.add(const Duration(minutes: 5))),
        _mk(2, base.add(const Duration(minutes: 30))),
        _mk(3, base.add(const Duration(hours: 1, minutes: 10))),
      ],
      truncated: false,
      count: 3,
    ));
    await tester.pumpWidget(ProviderScope(
      overrides: [activityApiProvider.overrideWithValue(api)],
      child: const MaterialApp(home: ActivityScreen()),
    ));
    await tester.pumpAndSettle();
    expect(find.text('09:00'), findsOneWidget);
    expect(find.text('10:00'), findsOneWidget);
  });

  testWidgets('shows truncation banner when truncated', (tester) async {
    final api = _FakeApi(ActivityPage(
      entries: [_mk(1, DateTime.now())],
      truncated: true,
      count: 1,
    ));
    await tester.pumpWidget(ProviderScope(
      overrides: [activityApiProvider.overrideWithValue(api)],
      child: const MaterialApp(home: ActivityScreen()),
    ));
    await tester.pumpAndSettle();
    expect(find.textContaining('Showing'), findsOneWidget);
  });
}
```

- [ ] **Step 3: Run tests, confirm fail**

Run: `cd flutter_app && flutter test test/features/activity/activity_screen_test.dart`
Expected: FAIL — `ActivityScreen` does not exist.

- [ ] **Step 4: Implement `activity_screen.dart`**

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'activity_models.dart';
import 'activity_providers.dart';
import 'widgets/activity_entry_tile.dart';
import 'widgets/activity_filter_chips.dart';

class ActivityScreen extends ConsumerWidget {
  const ActivityScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final q = ref.watch(activityQueryProvider);
    final async = ref.watch(activityEntriesProvider);

    return Scaffold(
      appBar: AppBar(
        title: const Text('Activity'),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            onPressed: () => ref.invalidate(activityEntriesProvider),
          ),
        ],
      ),
      body: Column(
        children: [
          _DatePickerBar(query: q),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
            child: ActivityFilterChips(
              availableOrgs: _orgsFrom(async),
              availableRepos: _reposFrom(async),
            ),
          ),
          const Divider(height: 1),
          Expanded(
            child: async.when(
              data: (page) => _Timeline(page: page),
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (err, _) => Center(child: Text('Error: $err')),
            ),
          ),
        ],
      ),
    );
  }

  List<String> _orgsFrom(AsyncValue<ActivityPage> a) =>
    a.valueOrNull?.entries.map((e) => e.org).toSet().toList() ?? const [];
  List<String> _reposFrom(AsyncValue<ActivityPage> a) =>
    a.valueOrNull?.entries.map((e) => e.repo).toSet().toList() ?? const [];
}

class _DatePickerBar extends ConsumerWidget {
  final ActivityQuery query;
  const _DatePickerBar({required this.query});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final notifier = ref.read(activityQueryProvider.notifier);
    final today = DateTime.now();
    final yesterday = today.subtract(const Duration(days: 1));
    final isToday = _isSameDay(query.date, today);
    final isYesterday = _isSameDay(query.date, yesterday);

    return SingleChildScrollView(
      scrollDirection: Axis.horizontal,
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
      child: Row(children: [
        ChoiceChip(
          label: const Text('Today'),
          selected: isToday,
          onSelected: (_) => notifier.setDate(today),
        ),
        const SizedBox(width: 8),
        ChoiceChip(
          label: const Text('Yesterday'),
          selected: isYesterday,
          onSelected: (_) => notifier.setDate(yesterday),
        ),
        const SizedBox(width: 8),
        ActionChip(
          label: const Text('Pick day'),
          onPressed: () async {
            final picked = await showDatePicker(
              context: context,
              initialDate: query.date ?? today,
              firstDate: today.subtract(const Duration(days: 365)),
              lastDate: today,
            );
            if (picked != null) notifier.setDate(picked);
          },
        ),
        const SizedBox(width: 8),
        ActionChip(
          label: const Text('Pick range'),
          onPressed: () async {
            final picked = await showDateRangePicker(
              context: context,
              firstDate: today.subtract(const Duration(days: 365)),
              lastDate: today,
            );
            if (picked != null) {
              notifier.setRange(picked.start, picked.end);
            }
          },
        ),
      ]),
    );
  }

  bool _isSameDay(DateTime? a, DateTime b) =>
    a != null && a.year == b.year && a.month == b.month && a.day == b.day;
}

class _Timeline extends StatelessWidget {
  final ActivityPage page;
  const _Timeline({required this.page});

  @override
  Widget build(BuildContext context) {
    if (page.entries.isEmpty) {
      return const Center(child: Text('No activity for this period.'));
    }
    // Group by hour; entries are already DESC.
    final items = <Widget>[];
    if (page.truncated) {
      items.add(Container(
        padding: const EdgeInsets.all(12),
        color: Theme.of(context).colorScheme.surfaceVariant,
        child: Text(
          'Showing ${page.entries.length} most recent entries. Narrow filters to see more.',
        ),
      ));
    }
    int? currentHour;
    for (final e in page.entries) {
      if (e.timestamp.hour != currentHour) {
        currentHour = e.timestamp.hour;
        items.add(Padding(
          padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
          child: Text(
            '${currentHour.toString().padLeft(2, '0')}:00',
            style: Theme.of(context).textTheme.titleSmall,
          ),
        ));
      }
      items.add(ActivityEntryTile(
        entry: e,
        onTap: () {
          // TODO wiring to /prs/:id and /issues/:id lives in the router task below.
          // Left as a no-op here for the initial widget; router task wires it.
        },
      ));
    }
    return RefreshIndicator(
      onRefresh: () async {
        // ProviderScope invalidation happens via the AppBar refresh; this
        // fallback keeps pull-to-refresh idiomatic.
      },
      child: ListView(children: items),
    );
  }
}
```

- [ ] **Step 5: Wire tap → detail navigation**

Replace the `TODO` comment in `_Timeline` with a `Navigator`/GoRouter call. Match the existing pattern — search for how the Issues screen navigates to `/issues/:id`:

```dart
onTap: () {
  final target = e.itemType == 'pr' ? '/prs/${e.id}' : '/issues/${e.id}';
  // Use the real router API — e.g. context.go(target) if using go_router.
  GoRouter.of(context).go(target);
},
```

But the activity entry's `id` is the `activity_log` row id, not the PR id. The router expects the PR/issue id. Since the activity_log does not store the PR/issue id (only number + repo), add a small resolver: look up the PR/issue by `(repo, number)` via the existing API client. Simplest v1: omit the direct jump and just show a snackbar: `"tap-to-navigate coming in a follow-up"`. This avoids adding a new `/prs/by-number?repo=X&number=Y` endpoint in this PR.

Apply this cut: in the `onTap` body:

```dart
onTap: () {
  ScaffoldMessenger.of(context).showSnackBar(
    SnackBar(content: Text('${e.repo} #${e.itemNumber}')),
  );
},
```

Note this deliberate cut in the PR description; the detail-navigation follow-up is trivial once the by-number lookup exists.

- [ ] **Step 6: Add the route**

In `lib/shared/router.dart`, add:

```dart
GoRoute(
  path: '/activity',
  builder: (_, __) => const ActivityScreen(),
),
```

Import `ActivityScreen`.

- [ ] **Step 7: Add the nav tab**

In the nav shell file identified in Step 1, add a destination between Issues and Stats:

```dart
NavigationDestination(
  icon: Icon(Icons.timeline),
  label: 'Activity',
),
```

And update any parallel list of routes/titles the shell maintains so the new tab maps to `/activity`. Match the exact structure used there (avoid introducing a new pattern).

- [ ] **Step 8: Run tests**

Run: `cd flutter_app && flutter test`
Expected: all PASS, including `activity_screen_test.dart`.

- [ ] **Step 9: Launch the app and smoke-test**

Run: `cd flutter_app && flutter run -d <device>`
Expected:
- New "Activity" tab visible in nav.
- Opens to today. Empty state if no activity.
- Once the daemon records events, they appear on refresh.
- Date picker, filter chips work.

- [ ] **Step 10: Commit**

```bash
git add flutter_app/lib/features/activity/activity_screen.dart flutter_app/lib/shared/router.dart flutter_app/lib/shared/nav_shell.dart flutter_app/test/features/activity/activity_screen_test.dart
git commit -m "feat(flutter): activity screen with timeline, filters, and route"
```

---

## Self-Review Checklist

Run through this before considering the plan done:

1. **Spec coverage:**
   - [x] §1 architecture — Tasks 1, 4, 8, 12 cover table, recorder, endpoint, UI.
   - [x] §2 data model — Task 1 (schema), Task 2 (store ops), Task 3 (new event), Task 5 (handlers).
   - [x] §3 recorder — Tasks 4 + 5, Task 7 wiring.
   - [x] §4 HTTP API — Task 8.
   - [x] §5 Flutter UI — Tasks 9–12.
   - [x] §6 config — Task 6.
   - [x] §7 retention — Task 7.
   - [x] §8 observability — partially (logs added in Tasks 4–5, 7; `activity_count_24h` on `/stats` not yet a task). **GAP**: add a tiny task for the `/stats` field.
   - [x] §9 testing — tests in each task.
   - [x] §10 backwards compatibility — implicit (new table, new event, new section).
   - [x] §11 rollout — the phase grouping matches the PR plan.

2. **Placeholder scan:** no "TBD", no "implement later", no "add appropriate error handling".

3. **Type consistency:** `InsertActivity` signature matches between `activity.go` (Task 2) and the `Store` interface in `recorder.go` (Task 4) and the `fakeStore` in the recorder test. `ListActivity` returns `([]*Activity, bool, error)` everywhere. `ActivityQuery` uses the same field names in store + handler.

**Fix:** add Task 13 for the `/stats` field to close the §8 gap.

---

### Task 13: `activity_count_24h` on `/stats`

**Files:**
- Modify: `daemon/internal/store/activity.go`
- Modify: `daemon/internal/server/handlers.go` (the `handleStats` function)
- Modify: `daemon/internal/server/handlers_test.go`

- [ ] **Step 1: Add `CountActivitySince` to `activity.go`**

```go
// CountActivitySince returns the number of activity rows with ts >= cutoff.
func (s *Store) CountActivitySince(cutoff time.Time) (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM activity_log WHERE ts >= ?",
		cutoff.UTC().Format(sqliteTimeFormat)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count activity: %w", err)
	}
	return n, nil
}
```

- [ ] **Step 2: Extend `/stats` response with the field**

In `handleStats`, add to whatever map/struct the handler returns:

```go
count24h, err := srv.store.CountActivitySince(time.Now().Add(-24 * time.Hour))
if err != nil {
	slog.Warn("stats: activity count failed", "err", err)
}
// add to the response map:
stats["activity_count_24h"] = count24h
```

Match the exact shape of the existing response (look for the current `handleStats` body — it likely returns a `map[string]any` or a typed struct; extend that).

- [ ] **Step 3: Add a test**

```go
func TestHandleStats_IncludesActivityCount(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.InsertActivity(time.Now(), "a", "a/b", "pr", 1, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, s)
	req := httptest.NewRequest("GET", "/stats", nil)
	req.Header.Set("X-Heimdallm-Token", testAPIToken)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if v, ok := body["activity_count_24h"].(float64); !ok || v != 1 {
		t.Errorf("activity_count_24h = %v, want 1", body["activity_count_24h"])
	}
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `make test-docker GO_TEST_ARGS="./internal/server/..."`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/store/activity.go daemon/internal/server/handlers.go daemon/internal/server/handlers_test.go
git commit -m "feat(daemon): expose activity_count_24h on /stats"
```

---

## Execution Order Summary

Recommended sequence and suggested PR grouping:

| Task | PR (from spec §11) |
|---|---|
| 1, 2 | **PR 1** — activity_log table + store ops |
| 3 | **PR 2** — issue_promoted event |
| 4, 5 | **PR 3 part 1** — recorder |
| 6, 7 | **PR 3 part 2** — config + wiring + retention |
| 8 | **PR 4** — GET /activity |
| 13 | **PR 4 addendum** (or merged into PR 4) — /stats field |
| 9, 10, 11, 12 | **PR 5** — Flutter tab |

PR 1 and PR 2 are independent and can land in any order. PR 3 depends on PR 1; merges cleanly before or after PR 2 (recorder ignores unknown events, so `promote` rows simply start appearing once PR 2 lands). PR 4 depends on PR 1 only. PR 5 depends on PR 4.
