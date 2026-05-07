# GUI Server Section Implementation Plan (#397)

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `/server` route to the Flutter desktop app that consolidates daemon start/stop, listen-URL editing, a live SSE event feed, and the existing logs view. Add a small daemon-side change so the new event feed actually shows poll cycles, and enrich `/health` with version + uptime.

**Architecture:** New Flutter feature folder `flutter_app/lib/features/server/` with three tab widgets (Status / Events / Logs) under a single `ServerScreen`. Existing `LogsScreen` body is extracted into a reusable `LogsView` widget. Existing dashboard `_confirmShutdown` / `_startDaemon` are extracted into shared helpers in `server_actions.dart`. On the daemon side, two new SSE event constants (`polling_started`, `polling_completed`) are emitted from `runTier2` in `daemon/cmd/heimdallm/main.go` via small helpers in `daemon/internal/sse/polling.go`, and `/health` is enriched with `version` + `started_at`.

**Tech Stack:** Dart/Flutter (Riverpod, GoRouter, mocktail), Go 1.25, chi router, existing `sse.Broker`.

**Standing rules from the user:**
- Never commit to `main`. Work happens on `feat/gui-server-section-397` inside `.worktrees/gui-server-397`.
- Do not commit code without explicit user approval (the docs-commit-gate rule extends to code: at the very least, surface the changes for review before pushing).
- All verification runs through `make verify-linux` from the worktree (Docker pipeline). Per-task `flutter test` / `go test` runs against the cached image are fine for inner-loop iteration.

**Spec amendment (worth noting):** The spec at section 9 named `daemon/internal/pipeline/pipeline.go` and `daemon/internal/issues/pipeline.go` as the emission sites for `polling_*` events. The actual top-level poll loop is `runTier2` in `daemon/cmd/heimdallm/main.go` (those `pipeline.go` files contain per-PR / per-issue `Run(...)` methods, not cycle loops). This plan emits from `runTier2` instead. The user-visible behavior is unchanged.

---

## File Structure

### Created (Dart)
- `flutter_app/lib/features/server/server_screen.dart` — top-level Scaffold with 3-tab layout, daemon-down placeholders, `?tab=` query-param routing.
- `flutter_app/lib/features/server/server_actions.dart` — extracted `confirmShutdown`, `startDaemon`, new `restartDaemon` helpers.
- `flutter_app/lib/features/server/event_summary.dart` — pure `summarize(type, payload)` and `glyphFor(type)` mappings.
- `flutter_app/lib/features/server/widgets/status_tab.dart` — running/stopped indicator, Start/Stop button, Listen URL editor, restart banner, version + uptime line.
- `flutter_app/lib/features/server/widgets/events_tab.dart` — SSE consumer with bounded ring buffer, compact rows, expand-to-JSON, pause/filter/search/clear controls.

### Created (Dart tests)
- `flutter_app/test/features/server/event_summary_test.dart` — table-driven coverage of `summarize` + `glyphFor`.
- `flutter_app/test/features/server/server_screen_test.dart` — widget tests for the 3-tab layout, Status form, Events feed rendering and controls.

### Created (Go)
- `daemon/internal/sse/polling.go` — `Publisher` interface, `EmitPollingStarted`, `EmitPollingCompleted` helpers.
- `daemon/internal/sse/polling_test.go` — unit tests for the helpers.

### Modified (Dart)
- `flutter_app/lib/features/logs/logs_screen.dart` — extract `LogsView` widget; `LogsScreen` becomes a thin Scaffold wrapper. Public API unchanged.
- `flutter_app/lib/features/dashboard/dashboard_screen.dart` — replace AppBar `/logs` icon with Server icon; forward `_confirmShutdown` / `_startDaemon` to shared helpers.
- `flutter_app/lib/shared/router.dart` — add `/server` route + redirect from `/logs`.

### Modified (Go)
- `daemon/internal/sse/broker.go` — add `EventPollingStarted` and `EventPollingCompleted` constants.
- `daemon/internal/server/handlers.go` — add `version`, `startedAt` fields to `Server`; enrich `handleHealth` payload.
- `daemon/internal/server/server.go` *(or wherever `New`/`NewWithOptions` lives)* — accept the new fields via Options.
- `daemon/cmd/heimdallm/main.go` — pass version + start time into `NewWithOptions`; thread an `sse.Publisher` into `runTier2`; emit `polling_*` around the PR and Issues sections of `processTick`.
- `daemon/internal/activity/recorder_test.go` — regression test asserting `polling_*` events do not produce activity rows.

---

## Task ordering rationale

Daemon-first because:
1. The Dart code parses event types as strings — having the daemon emit them first means manual end-to-end checks during Dart work are realistic.
2. Daemon work is small and self-contained.

Within Dart: pure functions → shared helpers → refactor → individual widgets → assembly screen → routing → dashboard wiring. Each step builds on the previous.

---

## Phase A — Daemon-side polling events and /health enrichment

### Task 1: Add SSE event constants for polling cycles

**Files:**
- Modify: `daemon/internal/sse/broker.go:7-29`

- [ ] **Step 1.1: Add the two constants**

In `daemon/internal/sse/broker.go`, after `EventRepoDiscovered` (line 25) and before the `EventPRStateChanged` block, add:

```go
	// EventPollingStarted fires once per poll cycle, before any repo work,
	// to give the GUI Server screen a "what's happening now" signal.
	// Payload: {"kind": "prs"|"issues", "repos": [...]}.
	EventPollingStarted = "polling_started"

	// EventPollingCompleted fires once per poll cycle, after all repos in
	// the cycle have been processed (or skipped). Payload: {"kind", "count",
	// "duration_ms"}. Not emitted when the cycle is cancelled mid-flight.
	EventPollingCompleted = "polling_completed"
```

- [ ] **Step 1.2: Verify the constants compile**

Run: `cd daemon && go build ./...`
Expected: builds clean (no test changes yet).

- [ ] **Step 1.3: Commit**

```bash
git add daemon/internal/sse/broker.go
git commit -m "feat(daemon): add polling_started/completed SSE event types

Used by the GUI Server section's Live Events feed to surface poll
cycle activity. No producer wired in this commit; recorder is not
extended (events stay transient).

Refs #397"
```

---

### Task 2: Add `Publisher` interface + emission helpers (TDD)

**Files:**
- Create: `daemon/internal/sse/polling.go`
- Test: `daemon/internal/sse/polling_test.go`

- [ ] **Step 2.1: Write the failing test**

Create `daemon/internal/sse/polling_test.go`:

```go
package sse

import (
	"encoding/json"
	"testing"
	"time"
)

type capturePublisher struct {
	events []Event
}

func (c *capturePublisher) Publish(e Event) {
	c.events = append(c.events, e)
}

func TestEmitPollingStarted_FormatsPayload(t *testing.T) {
	pub := &capturePublisher{}
	EmitPollingStarted(pub, "prs", []string{"acme/foo", "acme/bar"})

	if len(pub.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(pub.events))
	}
	got := pub.events[0]
	if got.Type != EventPollingStarted {
		t.Errorf("type: got %q want %q", got.Type, EventPollingStarted)
	}
	var payload struct {
		Kind  string   `json:"kind"`
		Repos []string `json:"repos"`
	}
	if err := json.Unmarshal([]byte(got.Data), &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.Kind != "prs" {
		t.Errorf("kind: got %q want %q", payload.Kind, "prs")
	}
	if len(payload.Repos) != 2 || payload.Repos[0] != "acme/foo" || payload.Repos[1] != "acme/bar" {
		t.Errorf("repos: got %v", payload.Repos)
	}
}

func TestEmitPollingCompleted_FormatsPayload(t *testing.T) {
	pub := &capturePublisher{}
	EmitPollingCompleted(pub, "issues", 7, 1234*time.Millisecond)

	if len(pub.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(pub.events))
	}
	got := pub.events[0]
	if got.Type != EventPollingCompleted {
		t.Errorf("type: got %q want %q", got.Type, EventPollingCompleted)
	}
	var payload struct {
		Kind       string `json:"kind"`
		Count      int    `json:"count"`
		DurationMs int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal([]byte(got.Data), &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.Kind != "issues" || payload.Count != 7 || payload.DurationMs != 1234 {
		t.Errorf("payload: got %+v", payload)
	}
}

func TestEmit_NilPublisherIsSafe(t *testing.T) {
	// Should not panic — production code may call before publisher is wired.
	EmitPollingStarted(nil, "prs", []string{"acme/foo"})
	EmitPollingCompleted(nil, "issues", 0, 0)
}
```

- [ ] **Step 2.2: Run test to verify it fails**

Run: `docker run --rm -v "$PWD:/app" -w /app/daemon heimdallm-verify go test ./internal/sse -run "TestEmit" -v`
Expected: FAIL with "undefined: EmitPollingStarted" / "undefined: EmitPollingCompleted" / "undefined: Publisher".

- [ ] **Step 2.3: Write the helpers**

Create `daemon/internal/sse/polling.go`:

```go
package sse

import (
	"encoding/json"
	"log/slog"
	"time"
)

// Publisher is the minimal interface the polling emitters need from the
// SSE broker. *Broker satisfies this; tests can pass a capturing fake.
type Publisher interface {
	Publish(Event)
}

// EmitPollingStarted publishes a polling_started event. Safe to call with a
// nil publisher (no-op).
func EmitPollingStarted(pub Publisher, kind string, repos []string) {
	if pub == nil {
		return
	}
	if repos == nil {
		repos = []string{}
	}
	data, err := json.Marshal(map[string]any{
		"kind":  kind,
		"repos": repos,
	})
	if err != nil {
		slog.Error("sse: marshal polling_started", "err", err)
		return
	}
	pub.Publish(Event{Type: EventPollingStarted, Data: string(data)})
}

// EmitPollingCompleted publishes a polling_completed event with the cycle's
// item count and elapsed duration. Safe to call with a nil publisher.
func EmitPollingCompleted(pub Publisher, kind string, count int, duration time.Duration) {
	if pub == nil {
		return
	}
	data, err := json.Marshal(map[string]any{
		"kind":        kind,
		"count":       count,
		"duration_ms": duration.Milliseconds(),
	})
	if err != nil {
		slog.Error("sse: marshal polling_completed", "err", err)
		return
	}
	pub.Publish(Event{Type: EventPollingCompleted, Data: string(data)})
}
```

- [ ] **Step 2.4: Run tests to verify they pass**

Run: `docker run --rm -v "$PWD:/app" -w /app/daemon heimdallm-verify go test ./internal/sse -v`
Expected: all PASS.

- [ ] **Step 2.5: Commit**

```bash
git add daemon/internal/sse/polling.go daemon/internal/sse/polling_test.go
git commit -m "feat(daemon): add Publisher interface + polling event emitters

EmitPollingStarted / EmitPollingCompleted format and publish poll
cycle events through any sse.Publisher (broker satisfies this).
Nil-publisher safe so callers don't need a guard.

Refs #397"
```

---

### Task 3: Wire `polling_*` emission into `runTier2`

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go:2034-2148` (the `runTier2` function)

- [ ] **Step 3.1: Add a `Publisher` parameter to `runTier2`**

In `daemon/cmd/heimdallm/main.go`, change the `runTier2` signature (around line 2034) from:

```go
func runTier2(
	ctx context.Context,
	adapter *tier2Adapter,
	limiter *scheduler.RateLimiter,
	prPublisher scheduler.Tier2PRPublisher,
	configFn func() []string,
	reposChan <-chan []string,
	interval time.Duration,
	coldStart bool,
) {
```

to:

```go
func runTier2(
	ctx context.Context,
	adapter *tier2Adapter,
	limiter *scheduler.RateLimiter,
	prPublisher scheduler.Tier2PRPublisher,
	ssePub sse.Publisher,
	configFn func() []string,
	reposChan <-chan []string,
	interval time.Duration,
	coldStart bool,
) {
```

- [ ] **Step 3.2: Emit `polling_started` / `polling_completed` around the PR section**

Inside `processTick` (around line 2074), wrap the **PR processing** block (line 2083 `// PR processing` through line 2106 — the one ending after the `for _, pr := range prs` loop) with emit calls. Replace the section from `// PR processing` through the end of that `for ... range prs { ... }` block with:

```go
		// PR processing
		sse.EmitPollingStarted(ssePub, "prs", currentRepos)
		prStart := time.Now()
		prCount := 0
		if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
			sse.EmitPollingCompleted(ssePub, "prs", prCount, time.Since(prStart))
			return
		}
		prs, err := adapter.FetchPRsToReview()
		if err != nil {
			slog.Error("tier2: fetch PRs", "err", err)
		} else {
			monitoredSet := make(map[string]struct{}, len(currentRepos))
			for _, r := range currentRepos {
				monitoredSet[r] = struct{}{}
			}
			for _, pr := range prs {
				if _, ok := monitoredSet[pr.Repo]; !ok {
					continue
				}
				if adapter.PRAlreadyReviewed(pr.ID, pr.Repo, pr.Number, pr.UpdatedAt, pr.HeadSHA) {
					continue
				}
				prCount++
				if err := prPublisher.PublishPRReview(ctx, pr.Repo, pr.Number, pr.ID, pr.HeadSHA); err != nil {
					slog.Error("tier2: publish PR review", "repo", pr.Repo, "pr", pr.Number, "err", err)
				}
			}
		}
		sse.EmitPollingCompleted(ssePub, "prs", prCount, time.Since(prStart))
```

Notes:
- `prCount` counts items we actually attempted to publish (post-filter), which matches the user-facing notion of "items processed".
- The early return on `limiter.Acquire` failure still emits `polling_completed` so the UI doesn't see hanging `polling_started` events on rate-limit aborts.

- [ ] **Step 3.3: Emit `polling_started` / `polling_completed` around the Issues section**

In the same `processTick` (around line 2108 `// Issue processing per repo`), replace the issue promotion + processing block (from `// Issue promotion` through `// Retry pending publishes` exclusive — i.e. the block ending after the `for _, repo := range currentRepos` loop) with:

```go
		// Issue processing
		sse.EmitPollingStarted(ssePub, "issues", currentRepos)
		issueStart := time.Now()
		issueCount := 0
		if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
			sse.EmitPollingCompleted(ssePub, "issues", issueCount, time.Since(issueStart))
			return
		}
		if n, err := adapter.PromoteReady(ctx, currentRepos); err != nil {
			slog.Error("tier2: promotion", "err", err)
		} else if n > 0 {
			slog.Info("tier2: promoted issues", "count", n)
		}

		// Issue processing per repo
		for _, repo := range currentRepos {
			if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
				sse.EmitPollingCompleted(ssePub, "issues", issueCount, time.Since(issueStart))
				return
			}
			n, err := adapter.ProcessRepo(ctx, repo)
			if err != nil {
				slog.Error("tier2: issue processing", "repo", repo, "err", err)
				continue
			}
			issueCount += n
			if n > 0 {
				slog.Info("tier2: processed issues", "repo", repo, "count", n)
			}
		}
		sse.EmitPollingCompleted(ssePub, "issues", issueCount, time.Since(issueStart))
```

- [ ] **Step 3.4: Pass `broker` into `runTier2` at the call site**

Find the existing call to `runTier2(...)` in `main.go` (`grep -n "runTier2(" daemon/cmd/heimdallm/main.go` to locate it). Add `broker` as the new parameter — `broker` is the `*sse.Broker` declared at `daemon/cmd/heimdallm/main.go:161`. The argument list should slot it after `prPublisher` and before `configFn` to match the new signature.

- [ ] **Step 3.5: Build the daemon to verify wiring**

Run: `docker run --rm -v "$PWD:/app" -w /app/daemon heimdallm-verify go build ./...`
Expected: builds clean.

- [ ] **Step 3.6: Run the existing daemon test suite**

Run: `docker run --rm -v "$PWD:/app" -w /app/daemon heimdallm-verify go test ./... -count=1 -timeout=120s`
Expected: all green. (No new tests yet for `runTier2` integration — the helpers are tested in Task 2; runtime integration is exercised by `make verify-linux` and manual checks.)

- [ ] **Step 3.7: Commit**

```bash
git add daemon/cmd/heimdallm/main.go
git commit -m "feat(daemon): emit polling_started/completed from runTier2

Threads the SSE broker into runTier2 as an sse.Publisher and emits
one (started, completed) pair per kind per cycle: 'prs' and 'issues'.
Counts reflect post-filter items the cycle attempted to publish or
process. Cancellation paths still emit polling_completed so the UI
doesn't see orphan started events.

Refs #397"
```

---

### Task 4: Activity recorder regression test for `polling_*`

**Files:**
- Modify: `daemon/internal/activity/recorder_test.go`

- [ ] **Step 4.1: Add the regression test**

Append to `daemon/internal/activity/recorder_test.go`. First inspect an existing recorder test (e.g. `TestRecorder_ReviewCompleted` at recorder_test.go:91) to mirror its setup. Then add:

```go
func TestRecorder_PollingEventsAreIgnored(t *testing.T) {
	store := newFakeStore()        // same fake store the file already uses
	rec := NewWithChannel(store, make(chan sse.Event, 8))

	// Feed both polling event types directly through the same handle path
	// the SSE consumer uses.
	for _, ev := range []sse.Event{
		{Type: sse.EventPollingStarted, Data: `{"kind":"prs","repos":["acme/foo"]}`},
		{Type: sse.EventPollingCompleted, Data: `{"kind":"issues","count":3,"duration_ms":42}`},
	} {
		if err := rec.handle(ev); err != nil {
			t.Fatalf("handle(%s): unexpected error: %v", ev.Type, err)
		}
	}

	if got := store.RowCount(); got != 0 {
		t.Errorf("polling events should not produce rows; got %d", got)
	}
}
```

If `newFakeStore` and `RowCount()` are not the names used in the existing file, mirror whatever helpers `TestRecorder_ReviewCompleted` uses; the assertion is "after handling polling events, the store has no new rows".

- [ ] **Step 4.2: Run the recorder tests**

Run: `docker run --rm -v "$PWD:/app" -w /app/daemon heimdallm-verify go test ./internal/activity -run TestRecorder -v`
Expected: all PASS, including the new `TestRecorder_PollingEventsAreIgnored`.

- [ ] **Step 4.3: Commit**

```bash
git add daemon/internal/activity/recorder_test.go
git commit -m "test(activity): assert polling events are ignored by recorder

Locks in the design choice that polling_* events stay transient
(SSE-only) and never reach the activity log. If a future contributor
adds a case for them in recorder.handle, this test will fail.

Refs #397"
```

---

### Task 5: Enrich `/health` with `version` + `started_at` (TDD)

**Files:**
- Modify: `daemon/internal/server/handlers.go:313-315` (`handleHealth`)
- Modify: the `Server` struct definition (same file, near top) and its constructor.
- Modify: the existing `handleHealth` test in `daemon/internal/server/handlers_test.go`.

- [ ] **Step 5.1: Locate the existing constructor and test**

Run: `grep -nE "^func New|^func NewWithOptions|^type Server struct" daemon/internal/server/handlers.go | head`
Run: `grep -n "handleHealth\|TestHealth\|/health" daemon/internal/server/handlers_test.go | head`

Note the line numbers — Steps 5.2 and 5.3 reference them.

- [ ] **Step 5.2: Add a failing health test**

Add to `daemon/internal/server/handlers_test.go` (next to any existing `TestHealth*`):

```go
func TestHealth_ReturnsVersionAndStartedAt(t *testing.T) {
	srv := newTestServer(t, ServerTestOptions{
		Version:   "v1.2.3-test",
		StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/health", nil)
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d want 200", rr.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "ok" {
		t.Errorf("status field: got %v", got["status"])
	}
	if got["version"] != "v1.2.3-test" {
		t.Errorf("version: got %v", got["version"])
	}
	if got["started_at"] != "2026-01-02T03:04:05Z" {
		t.Errorf("started_at: got %v", got["started_at"])
	}
}
```

If the file already has a `newTestServer(t)` helper but no `ServerTestOptions`, define a small struct + helper at the top of the file:

```go
type ServerTestOptions struct {
	Version   string
	StartedAt time.Time
}

func newTestServer(t *testing.T, opts ...ServerTestOptions) *Server {
	t.Helper()
	o := ServerTestOptions{}
	if len(opts) > 0 {
		o = opts[0]
	}
	// Reuse whatever existing minimal fixtures the file already has;
	// pass the new options through to the constructor.
	srv := NewWithOptions(/* existing fixture args */, Options{Version: o.Version, StartedAt: o.StartedAt})
	return srv
}
```

If the existing test file uses a different helper name, adapt accordingly — the design contract is "the test exercises a real `*Server` whose constructor accepts version + startedAt, and asserts those fields appear in `/health`."

- [ ] **Step 5.3: Run the test to verify it fails**

Run: `docker run --rm -v "$PWD:/app" -w /app/daemon heimdallm-verify go test ./internal/server -run TestHealth_ReturnsVersionAndStartedAt -v`
Expected: FAIL — either compile error (unknown `Version` / `StartedAt`) or assertion failure.

- [ ] **Step 5.4: Add `version` + `startedAt` fields to `Server`**

In `daemon/internal/server/handlers.go`, locate the `Server` struct (search for `type Server struct`). Add two fields:

```go
type Server struct {
	// ... existing fields ...
	version   string
	startedAt time.Time
}
```

Add `time` to the import block if not already present.

- [ ] **Step 5.5: Extend `Options` and `NewWithOptions` to accept them**

Locate the `Options` struct (search for `type Options struct`) and add:

```go
type Options struct {
	// ... existing fields ...
	Version   string
	StartedAt time.Time
}
```

In `NewWithOptions`, pass the values through:

```go
func NewWithOptions(s *store.Store, broker *sse.Broker, p *pipeline.Pipeline, apiToken string, opts Options) *Server {
	srv := New(s, broker, p, apiToken)
	// ... existing option wiring ...
	srv.version = opts.Version
	if !opts.StartedAt.IsZero() {
		srv.startedAt = opts.StartedAt
	}
	return srv
}
```

- [ ] **Step 5.6: Update `handleHealth` to include the new fields**

Replace the body at `handlers.go:313-315`:

```go
func (srv *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"status": "ok"}
	if srv.version != "" {
		resp["version"] = srv.version
	}
	if !srv.startedAt.IsZero() {
		resp["started_at"] = srv.startedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}
```

The fields are conditional so callers without options still get the original `{"status":"ok"}` shape — no backwards-incompatibility risk.

- [ ] **Step 5.7: Run the test**

Run: `docker run --rm -v "$PWD:/app" -w /app/daemon heimdallm-verify go test ./internal/server -run TestHealth -v`
Expected: PASS.

- [ ] **Step 5.8: Wire version + start time in `main.go`**

In `daemon/cmd/heimdallm/main.go`, find the call to `server.NewWithOptions(...)` (search `grep -n "NewWithOptions" daemon/cmd/heimdallm/main.go`). Add:

```go
srv := server.NewWithOptions(s, broker, p, apiToken, server.Options{
	// ... existing fields ...
	Version:   versionString(),  // see below
	StartedAt: time.Now(),
})
```

Where `versionString()` returns the build-time version. Look for any existing version variable: `grep -nE "var version|var Version|main\.version" daemon`. If found, use it. If not, add at the top of `main.go`:

```go
// version is overridden via -ldflags "-X main.version=..." at build time.
var version = "dev"

func versionString() string { return version }
```

- [ ] **Step 5.9: Run the full daemon test suite**

Run: `docker run --rm -v "$PWD:/app" -w /app/daemon heimdallm-verify go test ./... -count=1 -timeout=120s`
Expected: all PASS.

- [ ] **Step 5.10: Commit**

```bash
git add daemon/internal/server/handlers.go daemon/internal/server/handlers_test.go daemon/cmd/heimdallm/main.go
git commit -m "feat(daemon): include version + started_at in /health

The GUI Server screen's Status tab needs uptime + version to render
its summary line. Both fields are optional in the response so older
clients/test fixtures that don't set them keep working.

Refs #397"
```

---

## Phase B — Flutter pure logic (event_summary, server_actions)

### Task 6: `event_summary.dart` (TDD)

**Files:**
- Create: `flutter_app/lib/features/server/event_summary.dart`
- Test: `flutter_app/test/features/server/event_summary_test.dart`

- [ ] **Step 6.1: Write the failing test**

Create the test file `flutter_app/test/features/server/event_summary_test.dart`:

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/server/event_summary.dart';

void main() {
  group('summarize', () {
    test('review_started includes repo, number, agent', () {
      final s = summarize('review_started', {
        'pr_id': 1,
        'repo': 'acme/foo',
        'number': 42,
        'agent': 'claude',
      });
      expect(s, contains('acme/foo'));
      expect(s, contains('#42'));
      expect(s, contains('claude'));
    });

    test('review_completed includes duration when present', () {
      final s = summarize('review_completed', {
        'repo': 'acme/foo',
        'number': 42,
        'duration_ms': 4200,
      });
      expect(s, contains('acme/foo'));
      expect(s, contains('#42'));
      expect(s, contains('4.2s'));
    });

    test('issue_promoted includes stage transition when payload has it', () {
      final s = summarize('issue_promoted', {
        'repo': 'acme/foo',
        'number': 7,
        'from_stage': 'triage',
        'to_stage': 'refinement',
      });
      expect(s, contains('acme/foo'));
      expect(s, contains('#7'));
      expect(s, contains('triage'));
      expect(s, contains('refinement'));
    });

    test('polling_started includes kind and repo count', () {
      final s = summarize('polling_started', {
        'kind': 'prs',
        'repos': ['acme/foo', 'acme/bar'],
      });
      expect(s, contains('prs'));
      expect(s, contains('2'));
    });

    test('polling_completed includes kind, count, duration', () {
      final s = summarize('polling_completed', {
        'kind': 'issues',
        'count': 5,
        'duration_ms': 800,
      });
      expect(s, contains('issues'));
      expect(s, contains('5'));
      expect(s, contains('800'));
    });

    test('circuit_breaker_tripped includes reason', () {
      final s = summarize('circuit_breaker_tripped', {
        'reason': '5 failures/30s',
      });
      expect(s, contains('5 failures/30s'));
    });

    test('unknown event type falls back to type-only', () {
      final s = summarize('mystery_event', {'foo': 'bar'});
      expect(s, contains('mystery_event'));
    });
  });

  group('glyphFor', () {
    test('known types return non-empty icon and a color', () {
      for (final t in [
        'pr_detected',
        'review_started',
        'review_completed',
        'review_error',
        'issue_promoted',
        'polling_started',
        'polling_completed',
        'circuit_breaker_tripped',
      ]) {
        final g = glyphFor(t);
        expect(g.icon, isNotNull, reason: '$t has no icon');
      }
    });

    test('unknown types fall back to a default glyph', () {
      final g = glyphFor('mystery');
      expect(g.icon, isNotNull);
    });
  });
}
```

- [ ] **Step 6.2: Run test to verify it fails**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter test test/features/server/event_summary_test.dart 2>&1 | tail -20`
Expected: FAIL — `event_summary.dart` does not exist.

- [ ] **Step 6.3: Implement the helper**

Create `flutter_app/lib/features/server/event_summary.dart`:

```dart
import 'package:flutter/material.dart';

/// One-line human summary for an SSE event payload, used by the Server
/// screen's Live Events tab. Pure function — no widget dependencies.
String summarize(String type, Map<String, dynamic> payload) {
  String repoRef() {
    final repo = payload['repo'] as String?;
    final number = payload['number'];
    if (repo != null && number != null) return '$repo#$number';
    if (repo != null) return repo;
    return '';
  }

  switch (type) {
    case 'pr_detected':
      return 'pr_detected  ${repoRef()}'.trimRight();
    case 'review_started':
      final agent = payload['agent'] as String?;
      final ref = repoRef();
      return agent != null && agent.isNotEmpty
          ? 'review_started  $ref ($agent)'
          : 'review_started  $ref';
    case 'review_completed':
      final dur = _durationLabel(payload['duration_ms']);
      return dur != null
          ? 'review_completed  ${repoRef()} in $dur'
          : 'review_completed  ${repoRef()}';
    case 'review_error':
      return 'review_error  ${repoRef()}';
    case 'review_skipped':
      final reason = payload['reason'] as String?;
      return reason != null
          ? 'review_skipped  ${repoRef()} ($reason)'
          : 'review_skipped  ${repoRef()}';
    case 'issue_detected':
      return 'issue_detected  ${repoRef()}';
    case 'issue_review_started':
      final agent = payload['agent'] as String?;
      return agent != null
          ? 'issue_review_started  ${repoRef()} ($agent)'
          : 'issue_review_started  ${repoRef()}';
    case 'issue_review_completed':
      final dur = _durationLabel(payload['duration_ms']);
      return dur != null
          ? 'issue_review_completed  ${repoRef()} in $dur'
          : 'issue_review_completed  ${repoRef()}';
    case 'issue_refinement_done':
      return 'issue_refinement_done  ${repoRef()}';
    case 'issue_implemented':
      return 'issue_implemented  ${repoRef()}';
    case 'issue_review_error':
      return 'issue_review_error  ${repoRef()}';
    case 'issue_promoted':
      final from = payload['from_stage'] as String?;
      final to = payload['to_stage'] as String?;
      if (from != null && to != null) {
        return 'issue_promoted  ${repoRef()}  $from → $to';
      }
      return 'issue_promoted  ${repoRef()}';
    case 'pr_state_changed':
    case 'issue_state_changed':
      final from = payload['old_state'] ?? payload['from'];
      final to = payload['new_state'] ?? payload['to'];
      if (from != null && to != null) {
        return '$type  ${repoRef()}  $from → $to';
      }
      return '$type  ${repoRef()}';
    case 'circuit_breaker_tripped':
      final reason = payload['reason'] as String?;
      return reason != null
          ? 'circuit_breaker_tripped  $reason'
          : 'circuit_breaker_tripped';
    case 'repo_discovered':
      final repo = payload['repo'] as String? ?? '';
      return 'repo_discovered  $repo'.trimRight();
    case 'polling_started':
      final kind = payload['kind'] as String? ?? '';
      final repos = (payload['repos'] as List?) ?? const [];
      return 'polling_started  $kind (${repos.length} repos)';
    case 'polling_completed':
      final kind = payload['kind'] as String? ?? '';
      final count = payload['count'] ?? 0;
      final ms = payload['duration_ms'] ?? 0;
      return 'polling_completed  $kind  $count items in ${ms}ms';
    default:
      final repo = repoRef();
      return repo.isNotEmpty ? '$type  $repo' : type;
  }
}

String? _durationLabel(dynamic ms) {
  if (ms is! num) return null;
  if (ms < 1000) return '${ms}ms';
  return '${(ms / 1000).toStringAsFixed(1)}s';
}

/// Glyph (icon + color) for an event type, used by the Live Events row
/// renderer.
({IconData icon, Color color}) glyphFor(String type) {
  switch (type) {
    case 'pr_detected':
    case 'issue_detected':
      return (icon: Icons.fiber_manual_record, color: const Color(0xFF6CA0FF));
    case 'review_started':
    case 'issue_review_started':
      return (icon: Icons.play_arrow, color: const Color(0xFFFFB347));
    case 'review_completed':
    case 'issue_review_completed':
    case 'issue_implemented':
    case 'issue_refinement_done':
      return (icon: Icons.check, color: const Color(0xFF6CCA6C));
    case 'review_error':
    case 'issue_review_error':
      return (icon: Icons.close, color: const Color(0xFFFF6B6B));
    case 'review_skipped':
      return (icon: Icons.remove, color: const Color(0xFF888888));
    case 'issue_promoted':
    case 'pr_state_changed':
    case 'issue_state_changed':
      return (icon: Icons.sync_alt, color: const Color(0xFFB070FF));
    case 'circuit_breaker_tripped':
      return (icon: Icons.warning_amber, color: const Color(0xFFFFB347));
    case 'repo_discovered':
      return (icon: Icons.add, color: const Color(0xFF6CA0FF));
    case 'polling_started':
      return (icon: Icons.more_horiz, color: const Color(0xFF888888));
    case 'polling_completed':
      return (icon: Icons.circle_outlined, color: const Color(0xFF888888));
    default:
      return (icon: Icons.label_outline, color: const Color(0xFFD4D4D4));
  }
}
```

- [ ] **Step 6.4: Run test to verify it passes**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter test test/features/server/event_summary_test.dart 2>&1 | tail -10`
Expected: `+1 group: summarize ... All tests passed!` with all sub-tests green.

- [ ] **Step 6.5: Run flutter analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 6.6: Commit**

```bash
git add flutter_app/lib/features/server/event_summary.dart flutter_app/test/features/server/event_summary_test.dart
git commit -m "feat(server): add pure event_summary helper

summarize(type, payload) and glyphFor(type) map SSE event types to
the one-line summary + icon/color pair the Live Events tab renders.
Pure functions, exhaustively tested. Unknown types degrade to
'<type>  <repo>' / a default glyph instead of throwing.

Refs #397"
```

---

### Task 7: Extract shared daemon-action helpers (`server_actions.dart`)

**Files:**
- Create: `flutter_app/lib/features/server/server_actions.dart`
- Modify: `flutter_app/lib/features/dashboard/dashboard_screen.dart` (replace local helper bodies with forwards)

- [ ] **Step 7.1: Create the shared helpers**

Create `flutter_app/lib/features/server/server_actions.dart` by copying the bodies of `_confirmShutdown` (`dashboard_screen.dart:119-149`), `_startDaemon` (`dashboard_screen.dart:151-187`), and `_refreshWhenDaemonStops` (the function starting at line 189). Make them top-level functions named `confirmShutdown`, `startDaemon`, `refreshWhenDaemonStops`. Also import the same dependencies the dashboard file uses for those helpers (`flutter_riverpod`, `apiClientProvider`, `daemonStartingProvider`, `daemonHealthProvider`, `sseStreamProvider`, `platformServicesProvider`, `showToast`).

Then **add** a new helper at the bottom of the same file:

```dart
/// Stop the daemon and immediately respawn it. Used by the Server screen's
/// Restart banner after a Listen URL change.
Future<void> restartDaemon(BuildContext context, WidgetRef ref) async {
  final api = ref.read(apiClientProvider);
  ref.read(daemonStartingProvider.notifier).state = true;
  try {
    await api.shutdownDaemon();
    if (!context.mounted) return;
    showToast(context, 'Restarting…');
    await refreshWhenDaemonStops(context, ref);
    // refreshWhenDaemonStops returns once /health reports unreachable.
    if (!context.mounted) return;
    final platform = ref.read(platformServicesProvider);
    final binary = platform.defaultDaemonBinaryPath();
    if (binary == null || binary.isEmpty) {
      showToast(context, 'Daemon binary not found', isError: true);
      return;
    }
    await platform.spawnDaemon(binary);
    var healthy = false;
    for (var i = 0; i < 80; i++) {
      await Future<void>.delayed(const Duration(milliseconds: 100));
      healthy = await api.checkHealth();
      if (healthy) break;
    }
    if (!context.mounted) return;
    showToast(context, healthy ? 'Server restarted' : 'Restart timed out',
        isError: !healthy);
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  } finally {
    ref.read(daemonStartingProvider.notifier).state = false;
  }
}
```

Also export the same `_daemonStartHealthMaxAttempts` / `_daemonStartHealthInterval` constants used by `_startDaemon` — name them with the same value (`80` and `Duration(milliseconds: 100)`).

- [ ] **Step 7.2: Replace dashboard's local helpers with forwards**

In `flutter_app/lib/features/dashboard/dashboard_screen.dart`:

- Add `import '../server/server_actions.dart' as server_actions;` at the top.
- Replace the body of `_confirmShutdown` with `=> server_actions.confirmShutdown(context, ref);`.
- Replace the body of `_startDaemon` with `=> server_actions.startDaemon(context, ref);`.
- Delete the entire `_refreshWhenDaemonStops` function and the constants `_daemonStartHealthMaxAttempts` / `_daemonStartHealthInterval` (now lives in server_actions).

The two underscore-prefixed forwarders stay so the call sites at lines 55-56 and 564 are unchanged.

- [ ] **Step 7.3: Run dashboard tests + analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter test test/features/dashboard_test.dart 2>&1 | tail -10`
Expected: PASS (no behavior change).

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 7.4: Commit**

```bash
git add flutter_app/lib/features/server/server_actions.dart flutter_app/lib/features/dashboard/dashboard_screen.dart
git commit -m "refactor(server): extract daemon start/stop helpers

Moves _confirmShutdown / _startDaemon / _refreshWhenDaemonStops out
of dashboard_screen.dart into a shared server_actions.dart so the
new Server screen can reuse them. Adds a restartDaemon() helper for
the Listen URL editor's restart banner.

Dashboard's existing call sites are unchanged thanks to one-line
forwarders.

Refs #397"
```

---

## Phase C — Flutter refactor: extract `LogsView`

### Task 8: Extract `LogsView` from `LogsScreen`

**Files:**
- Modify: `flutter_app/lib/features/logs/logs_screen.dart`

- [ ] **Step 8.1: Read the current file structure**

Run: `wc -l flutter_app/lib/features/logs/logs_screen.dart`
Expected: ~211 lines.

Open it; identify the `_LogsScreenState` class (lines 30+) and its `build()` method (which currently returns a `Scaffold`). The split target is:
- `LogsView` — `ConsumerStatefulWidget` whose state holds the buffer/scroll/SSE plumbing and whose `build` returns the **body** widgets without the `Scaffold` and `AppBar`.
- `LogsScreen` — `ConsumerStatefulWidget` (or `StatelessWidget`) whose `build` returns `Scaffold(appBar: ..., body: const LogsView())`.

- [ ] **Step 8.2: Refactor**

Edit `flutter_app/lib/features/logs/logs_screen.dart`. The mechanical transformation:

1. Rename the existing `LogsScreen` class to `LogsView` and rename its `_LogsScreenState` to `_LogsViewState`.
2. In `_LogsViewState.build`, return the **body** that the previous `Scaffold` wrapped — i.e. the `Container`/`Column`/`ListView` previously inside `Scaffold(body: ...)`. Drop the `Scaffold` and `AppBar` calls.
3. At the bottom of the file, add a thin `LogsScreen`:

```dart
class LogsScreen extends StatelessWidget {
  const LogsScreen({super.key});

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => Navigator.of(context).maybePop(),
        ),
        title: const Text('Daemon logs'),
      ),
      body: const LogsView(),
    );
  }
}
```

The exact AppBar contents should mirror what the original `LogsScreen` rendered (back button + 'Daemon logs' title + any toolbar actions like the wrap toggle). If the wrap toggle and connect/disconnect button were in the AppBar `actions:` list, leave them in `LogsScreen`'s AppBar — those are page-level controls. If they were inline at the top of the body, keep them in `LogsView`. Inspect the original `build` to decide.

- [ ] **Step 8.3: Run tests + analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter test 2>&1 | tail -10`
Expected: all tests pass — `LogsScreen`'s public API is identical, so the existing `/logs` route still works.

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 8.4: Commit**

```bash
git add flutter_app/lib/features/logs/logs_screen.dart
git commit -m "refactor(logs): extract LogsView widget for embedding

LogsScreen becomes a thin Scaffold wrapper around a new LogsView
widget so the Server screen's Logs tab can embed the same body
without duplicating the SSE/buffer/scroll plumbing. The /logs
route's behavior is unchanged.

Refs #397"
```

---

## Phase D — Flutter Server screen widgets

### Task 9: `StatusTab` — state indicator + Start/Stop button

**Files:**
- Create: `flutter_app/lib/features/server/widgets/status_tab.dart`
- Test: covered by `server_screen_test.dart` in Task 14.

This task scaffolds the widget and the lightweight indicator + button. The Listen URL editor and version/uptime line ship in Tasks 10 and 11.

- [ ] **Step 9.1: Create the widget skeleton**

Create `flutter_app/lib/features/server/widgets/status_tab.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/api/api_client.dart';
import '../../dashboard/dashboard_providers.dart';
import '../server_actions.dart' as server_actions;

class StatusTab extends ConsumerWidget {
  const StatusTab({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final daemonRunning = ref.watch(daemonHealthProvider).valueOrNull ?? false;
    final daemonStarting = ref.watch(daemonStartingProvider);
    return SingleChildScrollView(
      padding: const EdgeInsets.all(16),
      child: Card(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              _StateIndicator(
                running: daemonRunning,
                starting: daemonStarting,
              ),
              const SizedBox(height: 16),
              _StartStopButton(
                running: daemonRunning,
                starting: daemonStarting,
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _StateIndicator extends StatelessWidget {
  const _StateIndicator({required this.running, required this.starting});
  final bool running;
  final bool starting;

  @override
  Widget build(BuildContext context) {
    final label = starting
        ? 'Starting…'
        : running
            ? 'Running'
            : 'Stopped';
    final color = starting
        ? Colors.amber
        : running
            ? Colors.green
            : Colors.grey;
    return Row(
      children: [
        Icon(Icons.circle, size: 12, color: color),
        const SizedBox(width: 8),
        Text(label, style: const TextStyle(fontSize: 14)),
      ],
    );
  }
}

class _StartStopButton extends ConsumerWidget {
  const _StartStopButton({required this.running, required this.starting});
  final bool running;
  final bool starting;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    if (starting) {
      return const Row(children: [
        SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)),
        SizedBox(width: 8),
        Text('Starting…'),
      ]);
    }
    return FilledButton.icon(
      icon: Icon(running ? Icons.power_settings_new : Icons.play_arrow),
      label: Text(running ? 'Stop server' : 'Start server'),
      onPressed: running
          ? () => server_actions.confirmShutdown(context, ref)
          : () => server_actions.startDaemon(context, ref),
    );
  }
}
```

If `daemonHealthProvider` / `daemonStartingProvider` are not in `dashboard_providers.dart`, locate them via `grep -nE "daemonHealthProvider|daemonStartingProvider" flutter_app/lib/features/dashboard/*.dart` and import the correct file.

- [ ] **Step 9.2: Run analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 9.3: Commit**

```bash
git add flutter_app/lib/features/server/widgets/status_tab.dart
git commit -m "feat(server): add StatusTab skeleton with start/stop button

State indicator (Running / Stopped / Starting) and a single Start/Stop
button delegating to server_actions. Listen URL editor and version
display land in follow-up commits.

Refs #397"
```

---

### Task 10: `StatusTab` — Listen URL editor + restart banner

**Files:**
- Modify: `flutter_app/lib/features/server/widgets/status_tab.dart`

- [ ] **Step 10.1: Add state for the Listen URL fields**

Convert `StatusTab` to a `ConsumerStatefulWidget`. The widget needs a snapshot of the initial bind addr + port so the restart banner only appears for changes the user actually made.

Replace the existing `StatusTab` declaration with:

```dart
class StatusTab extends ConsumerStatefulWidget {
  const StatusTab({super.key});
  @override
  ConsumerState<StatusTab> createState() => _StatusTabState();
}

class _StatusTabState extends ConsumerState<StatusTab> {
  String? _initialBindAddr;
  int? _initialPort;
  String? _editedBindAddr;
  int? _editedPort;
  Timer? _saveDebounce;

  bool get _bindAddrChanged =>
      _editedBindAddr != null && _editedBindAddr != _initialBindAddr;
  bool get _portChanged =>
      _editedPort != null && _editedPort != _initialPort;
  bool get _showBanner => _bindAddrChanged || _portChanged;

  @override
  void dispose() {
    _saveDebounce?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final config = ref.watch(configNotifierProvider).valueOrNull;
    if (config == null) {
      return const Center(child: CircularProgressIndicator());
    }
    _initialBindAddr ??= config.bindAddr ?? '127.0.0.1';
    _initialPort ??= config.serverPort;
    final daemonRunning = ref.watch(daemonHealthProvider).valueOrNull ?? false;
    final daemonStarting = ref.watch(daemonStartingProvider);

    return SingleChildScrollView(
      padding: const EdgeInsets.all(16),
      child: Card(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              _StateIndicator(
                running: daemonRunning,
                starting: daemonStarting,
              ),
              const SizedBox(height: 16),
              _StartStopButton(
                running: daemonRunning,
                starting: daemonStarting,
              ),
              const Divider(height: 32),
              const Text('Listen URL',
                  style: TextStyle(fontWeight: FontWeight.w600)),
              const SizedBox(height: 8),
              _ListenUrlEditor(
                initialBindAddr: _initialBindAddr!,
                initialPort: _initialPort!,
                onBindAddrChanged: _onBindAddrChanged,
                onPortChanged: _onPortChanged,
              ),
              if (_showBanner) ...[
                const SizedBox(height: 16),
                _RestartBanner(
                  portChanged: _portChanged,
                  onRestart: () => server_actions.restartDaemon(context, ref),
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }

  void _onBindAddrChanged(String v) {
    setState(() => _editedBindAddr = v);
    _scheduleSave();
  }

  void _onPortChanged(int v) {
    setState(() => _editedPort = v);
    _scheduleSave();
  }

  void _scheduleSave() {
    _saveDebounce?.cancel();
    _saveDebounce = Timer(const Duration(milliseconds: 800), _save);
  }

  Future<void> _save() async {
    final api = ref.read(apiClientProvider);
    try {
      final patch = <String, dynamic>{};
      if (_bindAddrChanged) patch['bind_addr'] = _editedBindAddr;
      if (_portChanged) patch['server_port'] = _editedPort;
      if (patch.isEmpty) return;
      await api.patchConfig(patch);
      if (!mounted) return;
      showToast(context, 'Saved (restart required)');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }
}
```

Add the imports the file needs:

```dart
import 'dart:async';
import '../../../shared/widgets/toast.dart';
import '../../config/config_providers.dart';
```

- [ ] **Step 10.2: Add the `_ListenUrlEditor` widget**

Below `_StatusTabState`, add:

```dart
class _ListenUrlEditor extends StatefulWidget {
  const _ListenUrlEditor({
    required this.initialBindAddr,
    required this.initialPort,
    required this.onBindAddrChanged,
    required this.onPortChanged,
  });
  final String initialBindAddr;
  final int initialPort;
  final ValueChanged<String> onBindAddrChanged;
  final ValueChanged<int> onPortChanged;

  @override
  State<_ListenUrlEditor> createState() => _ListenUrlEditorState();
}

class _ListenUrlEditorState extends State<_ListenUrlEditor> {
  late final TextEditingController _bindCtrl;
  late final TextEditingController _portCtrl;

  @override
  void initState() {
    super.initState();
    _bindCtrl = TextEditingController(text: widget.initialBindAddr);
    _portCtrl = TextEditingController(text: widget.initialPort.toString());
  }

  @override
  void dispose() {
    _bindCtrl.dispose();
    _portCtrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Row(
      children: [
        Expanded(
          flex: 3,
          child: TextField(
            controller: _bindCtrl,
            decoration: const InputDecoration(
              labelText: 'Bind address',
              helperText: 'e.g. 127.0.0.1, 0.0.0.0',
              border: OutlineInputBorder(),
              isDense: true,
            ),
            onChanged: widget.onBindAddrChanged,
          ),
        ),
        const SizedBox(width: 12),
        Expanded(
          flex: 1,
          child: TextField(
            controller: _portCtrl,
            decoration: const InputDecoration(
              labelText: 'Port',
              border: OutlineInputBorder(),
              isDense: true,
            ),
            keyboardType: TextInputType.number,
            onChanged: (v) {
              final n = int.tryParse(v);
              if (n != null) widget.onPortChanged(n);
            },
          ),
        ),
      ],
    );
  }
}
```

- [ ] **Step 10.3: Add the `_RestartBanner` widget**

Below `_ListenUrlEditorState`, add:

```dart
class _RestartBanner extends StatelessWidget {
  const _RestartBanner({required this.portChanged, required this.onRestart});
  final bool portChanged;
  final VoidCallback onRestart;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: const Color(0xFFFFF4D6),
        border: Border.all(color: const Color(0xFFE8C547)),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              const Icon(Icons.warning_amber, size: 18),
              const SizedBox(width: 8),
              const Expanded(
                child: Text(
                  'Listen URL changed. Restart the server for it to take effect.',
                  style: TextStyle(fontSize: 13),
                ),
              ),
              FilledButton(
                onPressed: onRestart,
                child: const Text('Restart server'),
              ),
            ],
          ),
          if (portChanged) ...[
            const SizedBox(height: 6),
            const Text(
              'Port change also requires restarting the desktop app for the GUI to reconnect.',
              style: TextStyle(fontSize: 12),
            ),
          ],
        ],
      ),
    );
  }
}
```

- [ ] **Step 10.4: Verify `RepoConfig` / `AppConfig` exposes `bindAddr` and `serverPort`**

Run: `grep -nE "bindAddr|serverPort|bind_addr" flutter_app/lib/core/models/config_model.dart | head`
Expected: `serverPort` exists at line 680. `bindAddr` may or may not be present.

If `bindAddr` is **not** present in `AppConfig`, add it:

In `flutter_app/lib/core/models/config_model.dart`, find the `AppConfig` class (around line 680) and add a `final String? bindAddr;` field, plumb it through the constructor, `copyWith`, `toJson` (key `"bind_addr"`), and `fromJson`. The daemon's TOML key is `bind_addr` so the JSON shape mirrors.

- [ ] **Step 10.5: Run analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 10.6: Commit**

```bash
git add flutter_app/lib/features/server/widgets/status_tab.dart flutter_app/lib/core/models/config_model.dart
git commit -m "feat(server): add Listen URL editor + restart banner

The Status tab gains two text fields (bind address + port) with
debounced autosave through PATCH /config. After a change, a yellow
banner offers a Restart server button; if the port changed, the
banner adds 'desktop app restart also required' as a second line.

Refs #397"
```

---

### Task 11: `StatusTab` — version + uptime display

**Files:**
- Modify: `flutter_app/lib/features/server/widgets/status_tab.dart`
- Possibly create: `flutter_app/lib/features/server/server_providers.dart` (small Riverpod provider for daemon health detail)

- [ ] **Step 11.1: Add an `apiClient.fetchHealth()` method**

In `flutter_app/lib/core/api/api_client.dart`, find `checkHealth` (around line 35). Add below it:

```dart
/// Returns the full /health payload, or null if the daemon is unreachable.
/// Includes status, version (optional), started_at (optional, RFC3339).
Future<Map<String, dynamic>?> fetchHealth() async {
  try {
    final resp = await _client.get(_uri('/health'), headers: await _authHeaders());
    if (resp.statusCode != 200) return null;
    return jsonDecode(resp.body) as Map<String, dynamic>;
  } catch (_) {
    return null;
  }
}
```

- [ ] **Step 11.2: Add a provider that polls `/health` every 30 s**

Create `flutter_app/lib/features/server/server_providers.dart`:

```dart
import 'dart:async';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/api/api_client.dart';

class HealthDetail {
  final String? version;
  final DateTime? startedAt;
  const HealthDetail({this.version, this.startedAt});
}

final serverHealthDetailProvider =
    StreamProvider.autoDispose<HealthDetail>((ref) async* {
  final api = ref.read(apiClientProvider);
  while (true) {
    final raw = await api.fetchHealth();
    if (raw == null) {
      yield const HealthDetail();
    } else {
      DateTime? started;
      final s = raw['started_at'];
      if (s is String) {
        started = DateTime.tryParse(s);
      }
      yield HealthDetail(
        version: raw['version'] as String?,
        startedAt: started,
      );
    }
    await Future<void>.delayed(const Duration(seconds: 30));
  }
});
```

- [ ] **Step 11.3: Render version + uptime line in the Status card**

In `_StatusTabState.build`, after the `_RestartBanner` (or after `_ListenUrlEditor` if no banner is showing), add:

```dart
const SizedBox(height: 16),
const Divider(),
const SizedBox(height: 8),
_HealthSummary(),
```

Below the existing widgets, add:

```dart
class _HealthSummary extends ConsumerWidget {
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final detail = ref.watch(serverHealthDetailProvider).valueOrNull;
    if (detail == null) return const SizedBox.shrink();
    final parts = <String>[];
    if (detail.version != null && detail.version!.isNotEmpty) {
      parts.add('Heimdallm ${detail.version}');
    }
    if (detail.startedAt != null) {
      parts.add('running for ${_formatUptime(DateTime.now().difference(detail.startedAt!))}');
    }
    if (parts.isEmpty) return const SizedBox.shrink();
    return Text(
      parts.join(' — '),
      style: const TextStyle(fontSize: 12, color: Colors.grey),
    );
  }
}

String _formatUptime(Duration d) {
  if (d.inDays > 0) return '${d.inDays}d ${d.inHours % 24}h';
  if (d.inHours > 0) return '${d.inHours}h ${d.inMinutes % 60}m';
  if (d.inMinutes > 0) return '${d.inMinutes}m';
  return '${d.inSeconds}s';
}
```

- [ ] **Step 11.4: Run analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 11.5: Commit**

```bash
git add flutter_app/lib/core/api/api_client.dart flutter_app/lib/features/server/server_providers.dart flutter_app/lib/features/server/widgets/status_tab.dart
git commit -m "feat(server): show version + uptime on Status tab

Adds a 30s-polling /health stream provider that surfaces the new
optional version + started_at fields. Older daemons that don't set
those silently degrade to an empty line.

Refs #397"
```

---

### Task 12: `EventsTab` — basic SSE consumption + compact rows

**Files:**
- Create: `flutter_app/lib/features/server/widgets/events_tab.dart`

- [ ] **Step 12.1: Create the widget with a 500-event ring buffer**

Create `flutter_app/lib/features/server/widgets/events_tab.dart`:

```dart
import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/api/sse_client.dart';
import '../../../core/platform/platform_services_provider.dart';
import '../event_summary.dart';

class EventsTab extends ConsumerStatefulWidget {
  const EventsTab({super.key});
  @override
  ConsumerState<EventsTab> createState() => _EventsTabState();
}

class _EventsTabState extends ConsumerState<EventsTab> {
  static const _maxEvents = 500;
  final _events = <_EventRow>[];
  SseClient? _client;
  StreamSubscription<SseEvent>? _sub;
  final _scroll = ScrollController();
  final _expanded = <int>{};

  @override
  void initState() {
    super.initState();
    final platform = ref.read(platformServicesProvider);
    _client = SseClient(platform: platform, path: '/events');
    _sub = _client!.connect().listen(_onEvent);
  }

  @override
  void dispose() {
    _sub?.cancel();
    _client?.disconnect();
    _scroll.dispose();
    super.dispose();
  }

  void _onEvent(SseEvent ev) {
    Map<String, dynamic> payload;
    try {
      final decoded = jsonDecode(ev.data);
      payload = decoded is Map<String, dynamic> ? decoded : <String, dynamic>{};
    } catch (_) {
      payload = const {};
    }
    setState(() {
      _events.add(_EventRow(
        timestamp: DateTime.now(),
        type: ev.type,
        payload: payload,
        rawData: ev.data,
      ));
      while (_events.length > _maxEvents) {
        _events.removeAt(0);
      }
    });
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_scroll.hasClients) {
        _scroll.jumpTo(_scroll.position.maxScrollExtent);
      }
    });
  }

  @override
  Widget build(BuildContext context) {
    if (_events.isEmpty) {
      return const Center(
        child: Text(
          'Waiting for events. Polling cycle runs every 60 s by default.',
          style: TextStyle(color: Colors.grey),
        ),
      );
    }
    return ListView.builder(
      controller: _scroll,
      itemCount: _events.length,
      itemBuilder: (context, i) => _Row(
        row: _events[i],
        expanded: _expanded.contains(i),
        onTap: () => setState(() {
          _expanded.contains(i) ? _expanded.remove(i) : _expanded.add(i);
        }),
      ),
    );
  }
}

class _EventRow {
  final DateTime timestamp;
  final String type;
  final Map<String, dynamic> payload;
  final String rawData;
  const _EventRow({
    required this.timestamp,
    required this.type,
    required this.payload,
    required this.rawData,
  });
}

class _Row extends StatelessWidget {
  const _Row({required this.row, required this.expanded, required this.onTap});
  final _EventRow row;
  final bool expanded;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final glyph = glyphFor(row.type);
    final hh = row.timestamp.hour.toString().padLeft(2, '0');
    final mm = row.timestamp.minute.toString().padLeft(2, '0');
    final ss = row.timestamp.second.toString().padLeft(2, '0');
    return InkWell(
      onTap: onTap,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text('$hh:$mm:$ss',
                    style: const TextStyle(
                        fontFamily: 'monospace', fontSize: 12, color: Colors.grey)),
                const SizedBox(width: 12),
                Icon(glyph.icon, color: glyph.color, size: 16),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    summarize(row.type, row.payload),
                    style: const TextStyle(fontFamily: 'monospace', fontSize: 13),
                  ),
                ),
              ],
            ),
            if (expanded)
              Container(
                margin: const EdgeInsets.only(left: 60, top: 4),
                padding: const EdgeInsets.all(8),
                decoration: BoxDecoration(
                  color: const Color(0xFFF5F5F5),
                  borderRadius: BorderRadius.circular(4),
                ),
                child: SelectableText(
                  _pretty(row.rawData),
                  style: const TextStyle(fontFamily: 'monospace', fontSize: 12),
                ),
              ),
          ],
        ),
      ),
    );
  }

  String _pretty(String raw) {
    try {
      return const JsonEncoder.withIndent('  ').convert(jsonDecode(raw));
    } catch (_) {
      return raw;
    }
  }
}
```

- [ ] **Step 12.2: Run analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 12.3: Commit**

```bash
git add flutter_app/lib/features/server/widgets/events_tab.dart
git commit -m "feat(server): add EventsTab with compact SSE feed

Connects to /events via the existing SseClient. Renders each event
as a single timestamped row with an icon and a summary line; tapping
expands inline to a pretty-printed JSON view. Buffer caps at 500
events. Auto-scrolls to bottom on new events.

Controls (pause, filter, search, clear) ship in the next task.

Refs #397"
```

---

### Task 13: `EventsTab` — pause / filter / search / clear controls

**Files:**
- Modify: `flutter_app/lib/features/server/widgets/events_tab.dart`

- [ ] **Step 13.1: Add the control state**

In `_EventsTabState`, add fields:

```dart
bool _autoScroll = true;
final Set<String> _enabledGroups = {'pr', 'issue', 'polling', 'state', 'circuit_breaker'};
String _searchQuery = '';
```

Define a helper that maps a type to its group:

```dart
String _groupOf(String type) {
  if (type.startsWith('pr_')) return 'pr';
  if (type.startsWith('issue_')) return 'issue';
  if (type.startsWith('polling_')) return 'polling';
  if (type.contains('state_changed')) return 'state';
  if (type == 'circuit_breaker_tripped') return 'circuit_breaker';
  return 'other';
}
```

Update `_onEvent` to only auto-scroll when `_autoScroll` is true:

```dart
WidgetsBinding.instance.addPostFrameCallback((_) {
  if (_autoScroll && _scroll.hasClients) {
    _scroll.jumpTo(_scroll.position.maxScrollExtent);
  }
});
```

- [ ] **Step 13.2: Build the toolbar**

In `build`, replace the top-level return with:

```dart
@override
Widget build(BuildContext context) {
  final visible = _events.where(_isVisible).toList(growable: false);
  return Column(
    children: [
      _Toolbar(
        autoScroll: _autoScroll,
        enabledGroups: _enabledGroups,
        searchQuery: _searchQuery,
        eventCount: _events.length,
        onAutoScrollChanged: (v) => setState(() => _autoScroll = v),
        onGroupToggled: (g) => setState(() {
          _enabledGroups.contains(g) ? _enabledGroups.remove(g) : _enabledGroups.add(g);
        }),
        onSearchChanged: (q) => setState(() => _searchQuery = q),
        onClear: () => setState(() {
          _events.clear();
          _expanded.clear();
        }),
      ),
      Expanded(
        child: visible.isEmpty
            ? const Center(
                child: Text(
                  'Waiting for events. Polling cycle runs every 60 s by default.',
                  style: TextStyle(color: Colors.grey),
                ),
              )
            : ListView.builder(
                controller: _scroll,
                itemCount: visible.length,
                itemBuilder: (context, i) => _Row(
                  row: visible[i],
                  expanded: _expanded.contains(i),
                  onTap: () => setState(() {
                    _expanded.contains(i) ? _expanded.remove(i) : _expanded.add(i);
                  }),
                ),
              ),
      ),
    ],
  );
}

bool _isVisible(_EventRow row) {
  if (!_enabledGroups.contains(_groupOf(row.type))) return false;
  if (_searchQuery.isEmpty) return true;
  final summary = summarize(row.type, row.payload).toLowerCase();
  return summary.contains(_searchQuery.toLowerCase());
}
```

- [ ] **Step 13.3: Add the `_Toolbar` widget**

Below `_Row`, add:

```dart
class _Toolbar extends StatelessWidget {
  const _Toolbar({
    required this.autoScroll,
    required this.enabledGroups,
    required this.searchQuery,
    required this.eventCount,
    required this.onAutoScrollChanged,
    required this.onGroupToggled,
    required this.onSearchChanged,
    required this.onClear,
  });

  final bool autoScroll;
  final Set<String> enabledGroups;
  final String searchQuery;
  final int eventCount;
  final ValueChanged<bool> onAutoScrollChanged;
  final ValueChanged<String> onGroupToggled;
  final ValueChanged<String> onSearchChanged;
  final VoidCallback onClear;

  static const _groups = {
    'pr': 'PR',
    'issue': 'Issue',
    'polling': 'Polling',
    'state': 'State',
    'circuit_breaker': 'Circuit',
  };

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
      decoration: const BoxDecoration(
        border: Border(bottom: BorderSide(color: Color(0xFFE0E0E0))),
      ),
      child: Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          IconButton(
            tooltip: autoScroll ? 'Pause auto-scroll' : 'Resume auto-scroll',
            icon: Icon(autoScroll ? Icons.pause : Icons.play_arrow),
            onPressed: () => onAutoScrollChanged(!autoScroll),
            visualDensity: VisualDensity.compact,
          ),
          ..._groups.entries.map((e) => FilterChip(
                label: Text(e.value),
                selected: enabledGroups.contains(e.key),
                onSelected: (_) => onGroupToggled(e.key),
              )),
          SizedBox(
            width: 200,
            child: TextField(
              decoration: const InputDecoration(
                hintText: 'Search',
                isDense: true,
                prefixIcon: Icon(Icons.search, size: 16),
                border: OutlineInputBorder(),
              ),
              style: const TextStyle(fontSize: 12),
              onChanged: onSearchChanged,
            ),
          ),
          TextButton.icon(
            onPressed: onClear,
            icon: const Icon(Icons.clear_all, size: 16),
            label: Text('Clear ($eventCount)'),
          ),
        ],
      ),
    );
  }
}
```

- [ ] **Step 13.4: Run analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 13.5: Commit**

```bash
git add flutter_app/lib/features/server/widgets/events_tab.dart
git commit -m "feat(server): add toolbar controls to EventsTab

Pause/resume auto-scroll, multi-select filter chips by event group,
substring search, and a Clear button (showing the buffered count).
Filter and search compose; hidden events stay in the buffer so
toggling them back on reveals history.

Refs #397"
```

---

### Task 14: `ServerScreen` — assemble 3 tabs + tests

**Files:**
- Create: `flutter_app/lib/features/server/server_screen.dart`
- Test: `flutter_app/test/features/server/server_screen_test.dart`

- [ ] **Step 14.1: Write the failing widget test**

Create `flutter_app/test/features/server/server_screen_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/server/server_screen.dart';
import 'package:mocktail/mocktail.dart';

class MockApiClient extends Mock implements ApiClient {}

Future<void> _pump(WidgetTester tester, MockApiClient api,
    {String initialTab = 'status'}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        configNotifierProvider.overrideWith(ConfigNotifier.new),
        agentsProvider.overrideWith((_) async => <ReviewPrompt>[]),
      ],
      child: MaterialApp(home: ServerScreen(initialTab: initialTab)),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  testWidgets('renders Status / Events / Logs tabs', (tester) async {
    final api = MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => {
          'repositories': <String>[],
          'server_port': 7842,
          'bind_addr': '127.0.0.1',
          'poll_interval': '60s',
          'retention_days': 30,
          'ai_primary': 'claude',
          'ai_fallback': '',
          'review_mode': 'single',
          'issue_tracking': {'enabled': false},
        });
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    when(() => api.fetchHealth()).thenAnswer((_) async => {'status': 'ok'});

    await _pump(tester, api);

    expect(find.text('Status'), findsOneWidget);
    expect(find.text('Events'), findsOneWidget);
    expect(find.text('Logs'), findsOneWidget);
  });

  testWidgets('Status tab shows Stop button when daemon running', (tester) async {
    final api = MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => {
          'repositories': <String>[],
          'server_port': 7842,
          'bind_addr': '127.0.0.1',
          'poll_interval': '60s',
          'retention_days': 30,
          'ai_primary': 'claude',
          'ai_fallback': '',
          'review_mode': 'single',
          'issue_tracking': {'enabled': false},
        });
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    when(() => api.fetchHealth()).thenAnswer((_) async => {'status': 'ok'});

    await _pump(tester, api);

    expect(find.text('Stop server'), findsOneWidget);
    expect(find.text('Start server'), findsNothing);
  });

  testWidgets('Status tab shows Start button when daemon stopped',
      (tester) async {
    final api = MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => {
          'repositories': <String>[],
          'server_port': 7842,
          'bind_addr': '127.0.0.1',
          'poll_interval': '60s',
          'retention_days': 30,
          'ai_primary': 'claude',
          'ai_fallback': '',
          'review_mode': 'single',
          'issue_tracking': {'enabled': false},
        });
    when(() => api.checkHealth()).thenAnswer((_) async => false);
    when(() => api.fetchHealth()).thenAnswer((_) async => null);

    await _pump(tester, api);

    expect(find.text('Start server'), findsOneWidget);
    expect(find.text('Stop server'), findsNothing);
  });

  testWidgets('initialTab=events selects the Events tab', (tester) async {
    final api = MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => {
          'repositories': <String>[],
          'server_port': 7842,
          'bind_addr': '127.0.0.1',
          'poll_interval': '60s',
          'retention_days': 30,
          'ai_primary': 'claude',
          'ai_fallback': '',
          'review_mode': 'single',
          'issue_tracking': {'enabled': false},
        });
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    when(() => api.fetchHealth()).thenAnswer((_) async => {'status': 'ok'});

    await _pump(tester, api, initialTab: 'events');
    // The Events tab is selected: its placeholder string should be visible.
    expect(find.textContaining('Waiting for events'), findsOneWidget);
  });
}
```

- [ ] **Step 14.2: Run test to verify it fails**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter test test/features/server/server_screen_test.dart 2>&1 | tail -20`
Expected: FAIL — `ServerScreen` does not exist.

- [ ] **Step 14.3: Implement `ServerScreen`**

Create `flutter_app/lib/features/server/server_screen.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../dashboard/dashboard_providers.dart';
import '../logs/logs_screen.dart' show LogsView;
import 'widgets/events_tab.dart';
import 'widgets/status_tab.dart';

const _tabIndices = {'status': 0, 'events': 1, 'logs': 2};

class ServerScreen extends ConsumerStatefulWidget {
  const ServerScreen({super.key, this.initialTab = 'status'});
  final String initialTab;

  @override
  ConsumerState<ServerScreen> createState() => _ServerScreenState();
}

class _ServerScreenState extends ConsumerState<ServerScreen>
    with SingleTickerProviderStateMixin {
  late final TabController _controller;

  @override
  void initState() {
    super.initState();
    _controller = TabController(
      length: 3,
      vsync: this,
      initialIndex: _tabIndices[widget.initialTab] ?? 0,
    );
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final daemonRunning = ref.watch(daemonHealthProvider).valueOrNull ?? false;
    return Scaffold(
      appBar: AppBar(
        title: const Text('Server'),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => Navigator.of(context).maybePop(),
        ),
        bottom: TabBar(
          controller: _controller,
          tabs: const [
            Tab(icon: Icon(Icons.dns_outlined), text: 'Status'),
            Tab(icon: Icon(Icons.bolt_outlined), text: 'Events'),
            Tab(icon: Icon(Icons.article_outlined), text: 'Logs'),
          ],
        ),
      ),
      body: TabBarView(
        controller: _controller,
        children: [
          const StatusTab(),
          daemonRunning
              ? const EventsTab()
              : const _DaemonStoppedPlaceholder(label: 'live events'),
          daemonRunning
              ? const LogsView()
              : const _DaemonStoppedPlaceholder(label: 'logs'),
        ],
      ),
    );
  }
}

class _DaemonStoppedPlaceholder extends StatelessWidget {
  const _DaemonStoppedPlaceholder({required this.label});
  final String label;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.power_off, size: 48, color: Colors.grey),
          const SizedBox(height: 12),
          Text('Server is stopped — start it to see $label.',
              style: const TextStyle(color: Colors.grey)),
          const SizedBox(height: 8),
          const Text('Switch to the Status tab to start the server.',
              style: TextStyle(color: Colors.grey, fontSize: 12)),
        ],
      ),
    );
  }
}
```

- [ ] **Step 14.4: Run the widget tests**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter test test/features/server/ 2>&1 | tail -15`
Expected: all PASS.

- [ ] **Step 14.5: Run analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 14.6: Commit**

```bash
git add flutter_app/lib/features/server/server_screen.dart flutter_app/test/features/server/server_screen_test.dart
git commit -m "feat(server): assemble ServerScreen with Status/Events/Logs tabs

3-tab Scaffold. Events and Logs tabs render a small placeholder when
the daemon is stopped instead of attempting to connect to SSE.
initialTab parameter drives the active tab so deep links via
?tab=events / ?tab=logs work after the router change.

Refs #397"
```

---

## Phase E — Routing and dashboard wiring

### Task 15: Add `/server` route + `/logs` redirect

**Files:**
- Modify: `flutter_app/lib/shared/router.dart`

- [ ] **Step 15.1: Update the router**

In `flutter_app/lib/shared/router.dart`, replace the existing `GoRoute(path: '/logs', ...)` line with:

```dart
GoRoute(
  path: '/server',
  builder: (context, state) {
    final tab = state.uri.queryParameters['tab'] ?? 'status';
    return ServerScreen(initialTab: tab);
  },
),
GoRoute(
  path: '/logs',
  redirect: (context, state) => '/server?tab=logs',
),
```

Also add the import at the top:

```dart
import '../features/server/server_screen.dart';
```

The existing `import '../features/logs/logs_screen.dart';` can stay (it's still re-exported via `LogsView`).

- [ ] **Step 15.2: Run tests + analyze**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter test 2>&1 | tail -10`
Expected: all PASS.

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

- [ ] **Step 15.3: Commit**

```bash
git add flutter_app/lib/shared/router.dart
git commit -m "feat(server): add /server route + redirect /logs to /server?tab=logs

Existing tray menu entries / notification deep links that point at
/logs continue to work and now land on the Server screen with the
Logs tab pre-selected.

Refs #397"
```

---

### Task 16: Replace dashboard's `/logs` AppBar icon with a Server icon

**Files:**
- Modify: `flutter_app/lib/features/dashboard/dashboard_screen.dart` (around line 58-62)

- [ ] **Step 16.1: Edit the AppBar action**

In `dashboard_screen.dart`, replace:

```dart
IconButton(
  icon: const Icon(Icons.article_outlined),
  tooltip: 'Daemon logs',
  onPressed: () => context.push('/logs'),
),
```

with:

```dart
IconButton(
  icon: const Icon(Icons.dns_outlined),
  tooltip: 'Server',
  onPressed: () => context.push('/server'),
),
```

- [ ] **Step 16.2: Run dashboard tests**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter test test/features/dashboard_test.dart 2>&1 | tail -10`
Expected: PASS. If a test asserts on the old `Icons.article_outlined` icon or the `/logs` push, update the assertion to match the new icon and route.

- [ ] **Step 16.3: Commit**

```bash
git add flutter_app/lib/features/dashboard/dashboard_screen.dart
git commit -m "feat(dashboard): swap Logs AppBar icon for Server icon

The new /server route is the primary operational surface (logs is
one of its tabs). The dashboard's quick-access AppBar icon now points
there. The Start/Stop button stays for one-click daemon toggling.

Refs #397"
```

---

## Phase F — End-to-end verification

### Task 17: Run the full verification pipeline

**Files:** none.

- [ ] **Step 17.1: Run `make verify-linux` from the worktree**

Run: `cd /home/vbueno/Desarrollo/workspaces/heimdallm-002/.worktrees/gui-server-397 && make verify-linux`
Expected: exit code 0, "✅ Linux build verification passed".

- [ ] **Step 17.2: Run targeted tests against the cached image**

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter test 2>&1 | tail -10`
Expected: `All tests passed!` with the new tests included.

Run: `docker run --rm -v "$PWD:/app" -w /app/flutter_app heimdallm-verify flutter analyze 2>&1 | tail -10`
Expected: `No issues found!`

Run: `docker run --rm -v "$PWD:/app" -w /app/daemon heimdallm-verify go test ./... -count=1 -timeout=120s`
Expected: `ok` for every package.

- [ ] **Step 17.3: Manual visual check**

Run: `make build-web` from the worktree, open the resulting bundle in a browser, and walk through:

1. From Dashboard, click the new **Server** icon (was Logs); confirm `/server` opens with the Status tab selected.
2. On Status: confirm the Running indicator, the Stop button, the Listen URL editor with `127.0.0.1` and `7842`, the version + uptime line.
3. Edit BindAddr only → save toast appears → restart banner shows the single-line message.
4. Edit Port → banner adds the second line about restarting the desktop app.
5. Click Restart server → daemon stops and respawns; once `/health` is healthy again, the banner disappears.
6. Click the Events tab — placeholder is visible until the next poll cycle (~60 s); then events stream in. Click a row to expand; toggle filter chips; type into search; click Clear.
7. Click the Logs tab — confirm the existing logs view renders identically.
8. Visit `/logs` directly in the browser address bar — confirm it redirects to `/server?tab=logs`.
9. Stop the daemon via the AppBar Stop button; confirm Events and Logs tabs render the placeholder, while Status remains usable.

- [ ] **Step 17.4: Final commit (if anything was tweaked during step 17.3)**

If the manual check surfaced bugs, fix them inline as their own commits per task. Otherwise, no final commit needed — the previous tasks already cover everything.

---

## Spec coverage check

| Spec section | Covered by |
|---|---|
| Routing & navigation | Task 15 (`/server` route + `/logs` redirect), Task 16 (AppBar icon) |
| Component 1 (`server_screen.dart`) | Task 14 |
| Component 2 (`status_tab.dart`) — state indicator + Start/Stop | Task 9 |
| Component 2 — Listen URL editor + restart banner | Task 10 |
| Component 2 — uptime + version | Task 11 (+ daemon Task 5) |
| Component 3 (`events_tab.dart`) — basic feed | Task 12 |
| Component 3 — controls (pause / filter / search / clear) | Task 13 |
| Component 4 (`event_summary.dart`) | Task 6 |
| Component 5 (`server_actions.dart`) — extracted helpers + `restartDaemon` | Task 7 |
| Component 6 (`logs_screen.dart` `LogsView` extraction) | Task 8 |
| Component 7 (dashboard AppBar icon swap + helper forwards) | Task 7 (forwards) + Task 16 (icon) |
| Component 8 (router) | Task 15 |
| Component 9 (daemon polling events) | Tasks 1, 2, 3 (+ regression Task 4) |
| Component 10 (daemon `/health` enrichment) | Task 5 |
| Tests (Dart unit + widget; Go pipeline + recorder + health) | Tasks 2, 4, 5, 6, 14 |
| Verification (`make verify-linux`, manual) | Task 17 |

No spec sections are unaccounted for.

## Self-review notes

- **Type consistency:** `summarize` and `glyphFor` signatures used in Task 6 match the references in Task 12 (`summarize(row.type, row.payload)` / `glyphFor(row.type)`). `serverHealthDetailProvider` defined in Task 11 matches the consumer in Task 11. `restartDaemon`, `confirmShutdown`, `startDaemon` from Task 7 match consumers in Tasks 9-10.
- **Placeholders:** None. Every "TBD-ish" line in the spec (the daemon version source, exact poll-loop sites) is resolved in the relevant plan task with concrete code.
- **Scope check:** Single PR, GUI + small daemon enrichment, ~17 tasks. Largest task (Task 5, /health enrichment) has 10 sub-steps but each step is bounded.
- **Frequent commits:** Every task ends in a commit; intermediate state stays compilable.
- **Spec amendment:** The poll-event emission location was updated (spec said pipeline.go files, plan emits from main.go's runTier2). User-visible behavior unchanged. Captured at the top of this plan.
