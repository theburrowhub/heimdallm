# Stop PR Review Cost-Runaway Loop — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the PR review feedback loop from 2026-04-22 (theburrowhub/heimdallm#243, €1,300 cost incident) physically impossible to recur by repairing four overlapping dedup defenses and adding an unconditional cost ceiling.

**Architecture:** Six fixes, each landing as its own PR for incremental deployment. Fix 2 ships first (smallest, closes the probable proximate cause). Fix 1 ships second (the unconditional ceiling — every other defense can fail; this one must not). Fixes 3–5 restore the anchor and amplifier defenses. Fix 6 pairs with each fix rather than shipping alone.

**Tech Stack:** Go 1.21 (daemon), Dart/Flutter (UI), SQLite (store via modernc.org/sqlite), TOML (config), `slog` (logging), existing SSE broker for UI events.

**Source of truth:** theburrowhub/heimdallm#243 — the full audit with root cause, defense-by-defense breakdown, and the explicit "do not reintroduce" note on the removed cross-instance-check fix. Read the issue before starting any task.

---

## Scope Check

The six fixes are thematically one feature ("make the cost-runaway impossible") but they are independent subsystems (store schema, pipeline dedup, scheduler backoff, config + UI wiring, startup recovery). They ship as **separate PRs** in the order below. Each task block produces working, testable software on its own.

The existing issue-triage fix in PR #226 (`daemon/internal/issues/fetcher.go`) used the same pattern and is the template for Fix 3 and Fix 6.

---

## File Structure

### New files

- `daemon/internal/pipeline/dedup.go` — shared helper `ReviewFreshEnough(anchor, observed, grace)` used by both PR and issue paths. Prevents future drift (the issue-triage code comment at `fetcher.go:20` already flags this debt).
- `daemon/internal/pipeline/pipeline_reloop_test.go` — PR-path regression tests (Fix 6). Mirrors `issues/fetcher_test.go:221 TestFetcher_SlowTriageDoesNotReloop`.
- `daemon/internal/store/circuitbreaker.go` — `CountReviewsForPR`, `CountReviewsForRepo`, and the table-backed breaker check.
- `daemon/internal/store/circuitbreaker_test.go` — unit tests for the counters and the breaker-trip semantics.
- `daemon/internal/store/inflight.go` — persistent in-flight reviews table + accessors.
- `daemon/internal/store/inflight_test.go` — tests.
- `daemon/internal/config/circuit_breaker.go` — `CircuitBreakerConfig` struct + defaults + validation.
- `flutter_app/lib/features/circuit_breaker/circuit_breaker_banner.dart` — UI banner when breaker trips.

### Modified files

- `daemon/internal/pipeline/pipeline.go:206-219` — fail-closed on HEAD SHA lookup (Fix 2); populate `PublishedAt` after `SubmitReview` returns (Fix 3); integrate circuit-breaker check before `Execute` (Fix 1).
- `daemon/internal/store/reviews.go` — `Review.PublishedAt` field, schema in `InsertReview` + `scanReview`, update `MarkReviewPublished` signature to accept `publishedAt`.
- `daemon/internal/store/store.go` — `ALTER TABLE reviews ADD COLUMN published_at` migration; new tables `reviews_in_flight` and an index for the circuit-breaker counts.
- `daemon/cmd/heimdallm/main.go:1445-1462` — `PRAlreadyReviewed` uses `PublishedAt` with 2 min grace, fall back to `CreatedAt` for legacy rows.
- `daemon/cmd/heimdallm/main.go:1582` — Tier 3 passes `snap.UpdatedAt` to `PRAlreadyReviewed` (not `item.LastSeen`).
- `daemon/internal/scheduler/queue.go:109-120` — `ResetBackoff(item, observedUpdatedAt)` signature; caller updated.
- `daemon/internal/scheduler/tier3.go:87` — pass `snap.UpdatedAt` into `ResetBackoff`.
- `daemon/internal/scheduler/tier2.go:169-179` — delay first tick on reload (cold start still fires immediately).
- `daemon/internal/config/config.go` — `Config.CircuitBreaker CircuitBreakerConfig` field + TOML binding.
- `daemon/internal/sse/broker.go` — new `EventCircuitBreakerTripped` constant.

---

## Preconditions

Before starting:

- [ ] Confirm issue #243 is assigned to the implementer.
- [ ] Local main is up to date: `git checkout main && git pull --ff-only`.
- [ ] Baseline green: `make test-docker` and `cd flutter_app && flutter test` both pass on main. Any red here is unrelated infrastructure and must be diagnosed before starting — do NOT layer these fixes on a broken baseline.

---

## Task 1 (Fix 2) — Fail-closed on HEAD SHA lookup

**Why first:** single file, ~30 lines changed, highest leverage. The proximate cause of the €1,300 leak. Ships as a tiny PR that can deploy same-day.

**Files:**
- Modify: `daemon/internal/pipeline/pipeline.go:206-219`
- Test: `daemon/internal/pipeline/pipeline_reloop_test.go` (create)

**Branch:** `fix/pr-dedup-fail-closed-head-sha`

- [ ] **Step 1.1: Start branch**

```bash
cd /Users/imunoz/Projects/ai-platform/heimdallm
git checkout main
git pull --ff-only
git checkout -b fix/pr-dedup-fail-closed-head-sha
```

- [ ] **Step 1.2: Create the failing test file**

Create `daemon/internal/pipeline/pipeline_reloop_test.go` with a minimal fake `github.Client` interface subset. The test fixture reproduces the bug: `GetPRHeadSHA` fails, so the guard should return early without running the review. The current code falls through and calls the executor.

```go
package pipeline_test

import (
	"errors"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

// fakeGH implements the subset of github.Client that the pipeline uses for
// the HEAD-SHA dedup path. Only GetPRHeadSHA needs to error — the other
// methods return zero values because they must not be called when the guard
// trips fail-closed.
type fakeGH struct {
	headSHAErr error
	submitted  bool
	diffCalled bool
}

func (f *fakeGH) GetPRHeadSHA(_ string, _ int) (string, error) {
	return "", f.headSHAErr
}
func (f *fakeGH) FetchDiff(_ string, _ int) (string, error) {
	f.diffCalled = true
	return "", nil
}
func (f *fakeGH) SubmitReview(_ string, _ int, _, _ string) (int64, string, error) {
	f.submitted = true
	return 0, "", nil
}

func TestRun_FailClosedWhenHeadSHALookupFails(t *testing.T) {
	store := newMemStore(t) // helper shared with other pipeline tests
	fgh := &fakeGH{headSHAErr: errors.New("github: 503 service unavailable")}
	p := pipeline.NewForTest(fgh, store) // test-only constructor wiring the fake

	pr := &gh.PullRequest{
		Repo: "org/repo", Number: 1,
		Head: gh.Commit{SHA: ""}, // empty, forces resolver path
	}
	_, err := p.Run(pr, pipeline.RunOptions{})
	if err == nil {
		t.Fatalf("expected fail-closed error, got nil")
	}
	if fgh.diffCalled {
		t.Errorf("fetch diff must not be called when HEAD SHA resolver fails")
	}
	if fgh.submitted {
		t.Errorf("SubmitReview must not be called when HEAD SHA resolver fails")
	}
	_ = store
}
```

Also create the helper at the top of the file:

```go
func newMemStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
```

If `pipeline.NewForTest` does not exist, add it in the same commit as a small `pipeline_testutil.go` next to `pipeline.go` exporting the constructor for tests only (guarded by a `_test` build tag if the existing code uses that convention; inspect `pipeline.go` to match).

- [ ] **Step 1.3: Run the test to verify it fails**

Run: `cd daemon && go test ./internal/pipeline/ -run TestRun_FailClosedWhenHeadSHALookupFails -v`

Expected: `FAIL` — the current code at `pipeline.go:207-212` logs a warning and continues. `fgh.submitted` or `fgh.diffCalled` will be true, or the test will fail because the pipeline reached `Execute` (which our fake does not implement and will either panic or proceed).

- [ ] **Step 1.4: Implement the fail-closed fix**

Edit `daemon/internal/pipeline/pipeline.go`:206-219. Replace the existing block:

```go
	if pr.Head.SHA == "" {
		if sha, err := p.gh.GetPRHeadSHA(pr.Repo, pr.Number); err != nil {
			slog.Warn("pipeline: could not resolve HEAD SHA, skipping dedup guard",
				"repo", pr.Repo, "pr", pr.Number, "err", err)
		} else {
			pr.Head.SHA = sha
		}
	}
	prevReview, _ := p.store.LatestReviewForPR(prID)
	if prevReview != nil && pr.Head.SHA != "" && prevReview.HeadSHA == pr.Head.SHA {
		slog.Info("pipeline: skipping re-review, HEAD SHA unchanged",
			"repo", pr.Repo, "pr", pr.Number, "head_sha", pr.Head.SHA)
		return prevReview, nil
	}
```

with the fail-closed version:

```go
	// Authoritative dedup by HEAD commit SHA. We must NOT proceed to Execute
	// when we cannot confirm the SHA, because a transient API failure would
	// otherwise bypass the cross-instance dedup and let every peer bot run
	// the review on top of the same commit. See theburrowhub/heimdallm#243.
	if pr.Head.SHA == "" {
		sha, err := p.gh.GetPRHeadSHA(pr.Repo, pr.Number)
		if err != nil {
			// One short retry absorbs rate-limit blips without turning the
			// fail-closed stance into a permanent outage.
			sha, err = p.gh.GetPRHeadSHA(pr.Repo, pr.Number)
		}
		if err != nil {
			slog.Warn("pipeline: HEAD SHA unresolved — skipping review (fail-closed)",
				"repo", pr.Repo, "pr", pr.Number, "err", err)
			return nil, fmt.Errorf("pipeline: resolve HEAD SHA: %w", err)
		}
		pr.Head.SHA = sha
	}
	prevReview, _ := p.store.LatestReviewForPR(prID)
	// Legacy rows (before the head_sha column was added in d16e51e) have empty
	// HeadSHA and would otherwise bypass the guard. Treat as "cannot confirm
	// safe" — backfill the column from the current snapshot and skip. The user
	// can trigger a re-review manually if they want one, but we never spend
	// Claude credits on a legacy row whose dedup state is ambiguous.
	if prevReview != nil && prevReview.HeadSHA == "" && pr.Head.SHA != "" {
		slog.Info("pipeline: backfilling empty HeadSHA on legacy review row, skipping re-review",
			"repo", pr.Repo, "pr", pr.Number, "review_id", prevReview.ID, "head_sha", pr.Head.SHA)
		_ = p.store.UpdateReviewHeadSHA(prevReview.ID, pr.Head.SHA)
		return prevReview, nil
	}
	if prevReview != nil && pr.Head.SHA != "" && prevReview.HeadSHA == pr.Head.SHA {
		slog.Info("pipeline: skipping re-review, HEAD SHA unchanged",
			"repo", pr.Repo, "pr", pr.Number, "head_sha", pr.Head.SHA)
		return prevReview, nil
	}
```

Add the new store method `UpdateReviewHeadSHA` in `daemon/internal/store/reviews.go`:

```go
// UpdateReviewHeadSHA backfills the head_sha column on a legacy review row.
// Used by the pipeline's fail-closed dedup: if a previous review had no SHA
// (from before the column was added), we populate it from the current
// snapshot instead of proceeding to a full re-review.
func (s *Store) UpdateReviewHeadSHA(reviewID int64, headSHA string) error {
	_, err := s.db.Exec("UPDATE reviews SET head_sha = ? WHERE id = ?", headSHA, reviewID)
	if err != nil {
		return fmt.Errorf("store: update review head_sha: %w", err)
	}
	return nil
}
```

- [ ] **Step 1.5: Run the test to verify it passes**

Run: `cd daemon && go test ./internal/pipeline/ -run TestRun_FailClosedWhenHeadSHALookupFails -v`

Expected: `PASS`.

- [ ] **Step 1.6: Add the legacy-row backfill test**

Add to `pipeline_reloop_test.go`:

```go
func TestRun_LegacyRowWithEmptyHeadSHAIsBackfilledAndSkipped(t *testing.T) {
	s := newMemStore(t)
	// Seed a "legacy" review row with head_sha = "".
	prRow := &store.PR{GithubID: 100, Repo: "org/repo", Number: 2, Title: "t", State: "open", UpdatedAt: time.Now()}
	prID, err := s.UpsertPR(prRow)
	if err != nil { t.Fatal(err) }
	_, err = s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Summary: "", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: time.Now().Add(-1 * time.Hour), HeadSHA: "",
	})
	if err != nil { t.Fatal(err) }

	fgh := &fakeGH{} // GetPRHeadSHA returns "abc123" by default via zero-value config
	fgh.headSHAValue = "abc123"
	p := pipeline.NewForTest(fgh, s)

	pr := &gh.PullRequest{Repo: "org/repo", Number: 2, Head: gh.Commit{SHA: ""}}
	rev, err := p.Run(pr, pipeline.RunOptions{})
	if err != nil { t.Fatalf("run: %v", err) }
	if rev == nil || rev.HeadSHA != "abc123" {
		t.Errorf("expected legacy row backfilled to abc123, got %+v", rev)
	}
	if fgh.diffCalled || fgh.submitted {
		t.Errorf("must not execute review when backfilling legacy row")
	}
}
```

Extend `fakeGH` to return a configurable SHA:

```go
type fakeGH struct {
	headSHAErr   error
	headSHAValue string
	submitted    bool
	diffCalled   bool
}

func (f *fakeGH) GetPRHeadSHA(_ string, _ int) (string, error) {
	if f.headSHAErr != nil { return "", f.headSHAErr }
	return f.headSHAValue, nil
}
```

- [ ] **Step 1.7: Run the backfill test**

Run: `cd daemon && go test ./internal/pipeline/ -run TestRun_LegacyRow -v`

Expected: `PASS`.

- [ ] **Step 1.8: Run the full daemon test suite via docker**

Run: `make test-docker`

Expected: all packages pass. No changes outside `pipeline/` and `store/reviews.go`, so the surface is small.

- [ ] **Step 1.9: Commit**

```bash
git add daemon/internal/pipeline/pipeline.go \
        daemon/internal/pipeline/pipeline_reloop_test.go \
        daemon/internal/store/reviews.go
git commit -m "$(cat <<'EOF'
fix(daemon): fail-closed on HEAD SHA lookup for PR dedup

Refs #243.

Before: when GetPRHeadSHA fails (rate-limit blip, transient 5xx), the
pipeline logged a warning and proceeded to Execute. With 7 team daemons
hitting the API simultaneously, failures were plausible; every instance
that got an error would bypass the primary cross-instance dedup and
spend Claude credits on the same commit. This was the probable
proximate cause of the 2026-04-22 cost-runaway on
freepik-company/ai-bumblebee-proxy#1051.

Fix:
- Retry GetPRHeadSHA once before giving up.
- On persistent failure, return an error instead of proceeding.
- On legacy rows with empty head_sha, backfill from the current
  snapshot and skip the review (the user can trigger manually if they
  want one). Previously these rows always bypassed the guard because
  "" == any real SHA is never true.

Tests:
- TestRun_FailClosedWhenHeadSHALookupFails: injects GetPRHeadSHA
  errors; asserts Execute + SubmitReview are never called.
- TestRun_LegacyRowWithEmptyHeadSHAIsBackfilledAndSkipped: seeds a
  review with HeadSHA=""; asserts the row is backfilled and the review
  does not re-run.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 1.10: Push and open PR**

```bash
git push -u origin fix/pr-dedup-fail-closed-head-sha
gh pr create --repo theburrowhub/heimdallm \
  --title "fix(daemon): fail-closed on HEAD SHA lookup for PR dedup" \
  --assignee ivanmunozruiz \
  --body "Refs #243 (Fix 2 in the priority list).

Smallest-surface high-leverage change from the audit. Ships first so the probable proximate cause of the incident is closed while the rest of the plan lands.

**Test plan**
- [x] Regression tests for both failure modes
- [x] \`make test-docker\` green
- [ ] Manual: restart daemon, verify reviews still post on healthy PRs
"
```

---

## Task 2 (Fix 1) — Circuit breaker (SQLite-backed)

**Why second:** the only unconditional ceiling. Even with Task 1 closing the proximate cause, other defenses can still fail (Fix 3/4/5 below). The circuit breaker caps worst-case damage independent of any other logic.

**Files:**
- Create: `daemon/internal/store/circuitbreaker.go`
- Create: `daemon/internal/store/circuitbreaker_test.go`
- Create: `daemon/internal/config/circuit_breaker.go`
- Modify: `daemon/internal/config/config.go` — add field + wire into `Config`
- Modify: `daemon/internal/sse/broker.go:6-27` — add event constant
- Modify: `daemon/internal/pipeline/pipeline.go:293` — call breaker check before `Execute`
- Modify: `daemon/internal/store/store.go` — add index for counter queries

**Branch:** `feat/circuit-breaker-review-cap`

- [ ] **Step 2.1: Start branch off the latest main**

```bash
git checkout main && git pull --ff-only
git checkout -b feat/circuit-breaker-review-cap
```

- [ ] **Step 2.2: Write the failing test for `CountReviewsForPR`**

Create `daemon/internal/store/circuitbreaker_test.go`:

```go
package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func TestCountReviewsForPR_CountsWithinWindow(t *testing.T) {
	s := newTestStore(t)
	prID, err := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1,
		Title: "t", State: "open", UpdatedAt: time.Now()})
	if err != nil { t.Fatal(err) }

	// Insert three reviews, two recent and one outside the 24h window.
	recent := time.Now().Add(-2 * time.Hour)
	old := time.Now().Add(-48 * time.Hour)
	for _, at := range []time.Time{recent, recent.Add(time.Minute), old} {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: at,
		}); err != nil { t.Fatal(err) }
	}

	since := time.Now().Add(-24 * time.Hour)
	n, err := s.CountReviewsForPR(prID, since)
	if err != nil { t.Fatalf("count: %v", err) }
	if n != 2 {
		t.Errorf("want 2 within 24h, got %d", n)
	}
}

func TestCountReviewsForRepo_CountsDistinctPRs(t *testing.T) {
	s := newTestStore(t)
	for i := int64(1); i <= 3; i++ {
		prID, _ := s.UpsertPR(&store.PR{GithubID: i, Repo: "org/r", Number: int(i),
			Title: "t", State: "open", UpdatedAt: time.Now()})
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: time.Now().Add(-10 * time.Minute),
		}); err != nil { t.Fatal(err) }
	}
	since := time.Now().Add(-1 * time.Hour)
	n, err := s.CountReviewsForRepo("org/r", since)
	if err != nil { t.Fatalf("count: %v", err) }
	if n != 3 {
		t.Errorf("want 3 reviews in last hour, got %d", n)
	}
}
```

- [ ] **Step 2.3: Run the tests to verify they fail**

Run: `cd daemon && go test ./internal/store/ -run "TestCountReviews" -v`

Expected: `FAIL` — `CountReviewsForPR` and `CountReviewsForRepo` do not exist yet.

- [ ] **Step 2.4: Implement `CountReviewsForPR` and `CountReviewsForRepo`**

Create `daemon/internal/store/circuitbreaker.go`:

```go
package store

import (
	"fmt"
	"time"
)

// CountReviewsForPR returns the number of reviews for the given PR whose
// created_at is at or after `since`. Used by the circuit breaker to cap
// runaway re-review loops. Only reviews already persisted to SQLite count —
// an in-flight review that has not called InsertReview yet is gated
// separately via the inflight table (Task 6a).
func (s *Store) CountReviewsForPR(prID int64, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM reviews WHERE pr_id = ? AND created_at >= ?",
		prID, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count reviews for pr: %w", err)
	}
	return n, nil
}

// CountReviewsForRepo returns the number of reviews on ANY PR in the given
// repo whose created_at is at or after `since`. Used for the per-repo rate
// limit of the circuit breaker.
func (s *Store) CountReviewsForRepo(repo string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM reviews r
		JOIN prs p ON r.pr_id = p.id
		WHERE p.repo = ? AND r.created_at >= ?`,
		repo, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count reviews for repo: %w", err)
	}
	return n, nil
}
```

Add a covering index for the `pr_id + created_at` query. In `daemon/internal/store/store.go`, inside `Open` alongside the other `ALTER TABLE` migrations (~line 143), append:

```go
	// Covering index for the circuit-breaker counters (see issue #243).
	// CREATE INDEX IF NOT EXISTS is idempotent; safe on every startup.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_reviews_pr_created ON reviews(pr_id, created_at)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_reviews_created ON reviews(created_at)")
```

- [ ] **Step 2.5: Run the counter tests to verify they pass**

Run: `cd daemon && go test ./internal/store/ -run "TestCountReviews" -v`

Expected: `PASS`.

- [ ] **Step 2.6: Write the failing test for the breaker trip semantics**

Extend `daemon/internal/store/circuitbreaker_test.go`:

```go
func TestCircuitBreaker_TripsOnPerPRCap(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1,
		Title: "t", State: "open", UpdatedAt: time.Now()})
	// Seed 3 reviews in the last 24h → cap is 3.
	for i := 0; i < 3; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: time.Now().Add(time.Duration(-i) * time.Minute),
		}); err != nil { t.Fatal(err) }
	}

	cfg := store.CircuitBreakerLimits{
		PerPR24h:   3,
		PerRepoHr:  20,
	}
	tripped, reason, err := s.CheckCircuitBreaker(prID, "org/r", cfg)
	if err != nil { t.Fatalf("check: %v", err) }
	if !tripped {
		t.Errorf("expected tripped, got false (reason=%q)", reason)
	}
	if reason == "" {
		t.Errorf("tripped must include a human-readable reason")
	}
}

func TestCircuitBreaker_AllowsUnderCap(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{GithubID: 2, Repo: "org/r", Number: 2,
		Title: "t", State: "open", UpdatedAt: time.Now()})
	// 2 reviews, cap 3 → must allow.
	for i := 0; i < 2; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: time.Now().Add(time.Duration(-i) * time.Minute),
		}); err != nil { t.Fatal(err) }
	}
	cfg := store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 20}
	tripped, _, err := s.CheckCircuitBreaker(prID, "org/r", cfg)
	if err != nil { t.Fatalf("check: %v", err) }
	if tripped {
		t.Errorf("expected allowed, got tripped")
	}
}
```

- [ ] **Step 2.7: Run the breaker tests to verify they fail**

Run: `cd daemon && go test ./internal/store/ -run "TestCircuitBreaker" -v`

Expected: `FAIL` — `CircuitBreakerLimits` and `CheckCircuitBreaker` do not exist yet.

- [ ] **Step 2.8: Implement the breaker check**

Extend `daemon/internal/store/circuitbreaker.go`:

```go
// CircuitBreakerLimits is the configured set of caps. Enforced by
// CheckCircuitBreaker; zero values mean "unlimited" for that axis.
type CircuitBreakerLimits struct {
	PerPR24h  int // max reviews per PR in any 24h window
	PerRepoHr int // max reviews per repo in any 1h window
}

// CheckCircuitBreaker returns (tripped, reason, err). When tripped is true,
// the caller MUST NOT proceed to spend Claude credits for this PR. reason is
// a human-readable explanation suitable for logs and UI surfaces; it is
// empty when tripped is false.
func (s *Store) CheckCircuitBreaker(prID int64, repo string, cfg CircuitBreakerLimits) (bool, string, error) {
	if cfg.PerPR24h > 0 {
		n, err := s.CountReviewsForPR(prID, time.Now().Add(-24*time.Hour))
		if err != nil { return false, "", err }
		if n >= cfg.PerPR24h {
			return true, fmt.Sprintf("per-PR cap reached: %d reviews in last 24h (cap %d)", n, cfg.PerPR24h), nil
		}
	}
	if cfg.PerRepoHr > 0 && repo != "" {
		n, err := s.CountReviewsForRepo(repo, time.Now().Add(-1*time.Hour))
		if err != nil { return false, "", err }
		if n >= cfg.PerRepoHr {
			return true, fmt.Sprintf("per-repo cap reached: %d reviews on %s in last 1h (cap %d)", n, repo, cfg.PerRepoHr), nil
		}
	}
	return false, "", nil
}
```

- [ ] **Step 2.9: Run the breaker tests to verify they pass**

Run: `cd daemon && go test ./internal/store/ -run "TestCircuitBreaker" -v`

Expected: `PASS`.

- [ ] **Step 2.10: Add the config section**

Create `daemon/internal/config/circuit_breaker.go`:

```go
package config

// CircuitBreakerConfig caps the number of reviews per PR and per repo to
// prevent cost-runaway loops. The defaults are conservative — users with
// high-volume workflows must explicitly raise them. See
// theburrowhub/heimdallm#243 for the incident that prompted these caps.
type CircuitBreakerConfig struct {
	// PerPR24h caps reviews on the same PR over any 24-hour window.
	// 0 = unlimited. Default 3.
	PerPR24h int `toml:"per_pr_24h"`
	// PerRepoHr caps reviews on the same repo over any 1-hour window.
	// 0 = unlimited. Default 20.
	PerRepoHr int `toml:"per_repo_hr"`
}

// DefaultCircuitBreakerConfig returns the safe defaults applied when the
// [circuit_breaker] TOML section is missing or zero-valued.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		PerPR24h:  3,
		PerRepoHr: 20,
	}
}
```

Modify `daemon/internal/config/config.go:33-39` — extend `Config`:

```go
type Config struct {
	Server         ServerConfig         `toml:"server"`
	GitHub         GitHubConfig         `toml:"github"`
	AI             AIConfig             `toml:"ai"`
	Retention      RetentionConfig      `toml:"retention"`
	ActivityLog    ActivityLogConfig    `toml:"activity_log"`
	CircuitBreaker CircuitBreakerConfig `toml:"circuit_breaker"`
}
```

In the `applyDefaults` function (grep for it in the same file), add:

```go
	if c.CircuitBreaker.PerPR24h == 0 {
		c.CircuitBreaker.PerPR24h = 3
	}
	if c.CircuitBreaker.PerRepoHr == 0 {
		c.CircuitBreaker.PerRepoHr = 20
	}
```

- [ ] **Step 2.11: Add the SSE event**

Modify `daemon/internal/sse/broker.go:6-27`. Add after `EventReviewSkipped`:

```go
	EventCircuitBreakerTripped = "circuit_breaker_tripped"
```

- [ ] **Step 2.12: Wire the breaker into the pipeline**

Modify `daemon/internal/pipeline/pipeline.go` — add a breaker-check call before step 5 (Execute). Find the "// 5. Execute review" comment and insert before it:

```go
	// 4b. Circuit breaker: hard cap on review count per PR / per repo. Runs
	// AFTER all dedup layers so it only fires when the dedup failed but the
	// caller is about to spend Claude credits anyway. See
	// theburrowhub/heimdallm#243.
	if p.breaker != nil {
		tripped, reason, err := p.store.CheckCircuitBreaker(prID, pr.Repo, *p.breaker)
		if err != nil {
			slog.Warn("pipeline: circuit breaker check failed, proceeding", "err", err)
		} else if tripped {
			slog.Error("pipeline: CIRCUIT BREAKER TRIPPED — skipping review",
				"repo", pr.Repo, "pr", pr.Number, "reason", reason)
			p.notify.Notify("Heimdallm circuit breaker",
				fmt.Sprintf("%s #%d: %s", pr.Repo, pr.Number, reason))
			// The Pipeline.broker (SSE) is in main.go — publish via its
			// already-wired event channel; the pipeline doesn't depend on
			// the broker directly, so we return a typed error and let the
			// caller emit the SSE (they already do for SkipReason).
			return nil, fmt.Errorf("pipeline: circuit breaker tripped: %s", reason)
		}
	}
```

Add a `breaker *store.CircuitBreakerLimits` field to the `Pipeline` struct (grep for `type Pipeline struct` in the same file) and a setter `SetCircuitBreakerLimits`:

```go
// SetCircuitBreakerLimits enables the per-PR and per-repo caps. Nil disables
// all caps (equivalent to the pre-issue-243 behaviour). Must be called before
// Run; typically once at daemon startup.
func (p *Pipeline) SetCircuitBreakerLimits(limits *store.CircuitBreakerLimits) {
	p.breaker = limits
}
```

In `daemon/cmd/heimdallm/main.go`, at the point where the Pipeline is constructed (grep for `pipeline.New(`), call:

```go
	cbLimits := store.CircuitBreakerLimits{
		PerPR24h:  cfg.CircuitBreaker.PerPR24h,
		PerRepoHr: cfg.CircuitBreaker.PerRepoHr,
	}
	pipe.SetCircuitBreakerLimits(&cbLimits)
```

In `daemon/cmd/heimdallm/main.go` — where errors from `pipe.Run` are handled (grep for `pipeline run failed`), add a branch that detects the typed error via `strings.Contains(err.Error(), "circuit breaker tripped")` and emits the SSE event:

```go
	if errors.Is(err, context.Canceled) {
		// ... existing handling ...
	}
	if strings.Contains(err.Error(), "circuit breaker tripped") {
		broker.Publish(sse.Event{
			Type: sse.EventCircuitBreakerTripped,
			Data: sseData(map[string]any{
				"pr_number": pr.Number,
				"repo":      pr.Repo,
				"reason":    strings.TrimPrefix(err.Error(), "pipeline: circuit breaker tripped: "),
			}),
		})
		return
	}
```

- [ ] **Step 2.13: Write the integration test — pipeline refuses to execute when breaker trips**

Extend `daemon/internal/pipeline/pipeline_reloop_test.go`:

```go
func TestRun_CircuitBreakerTripStopsExecute(t *testing.T) {
	s := newMemStore(t)
	prRow := &store.PR{GithubID: 42, Repo: "org/r", Number: 42, Title: "t",
		State: "open", UpdatedAt: time.Now()}
	prID, _ := s.UpsertPR(prRow)
	// Seed 3 reviews to hit the cap.
	for i := 0; i < 3; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: time.Now().Add(time.Duration(-i) * time.Minute),
			HeadSHA: "abc",
		}); err != nil { t.Fatal(err) }
	}

	fgh := &fakeGH{headSHAValue: "def"} // different SHA so dedup passes
	p := pipeline.NewForTest(fgh, s)
	p.SetCircuitBreakerLimits(&store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 999})

	pr := &gh.PullRequest{Repo: "org/r", Number: 42, Head: gh.Commit{SHA: ""}}
	_, err := p.Run(pr, pipeline.RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "circuit breaker tripped") {
		t.Fatalf("expected circuit breaker error, got %v", err)
	}
	if fgh.submitted {
		t.Errorf("SubmitReview must not be called when breaker trips")
	}
}
```

- [ ] **Step 2.14: Run all new tests**

Run: `cd daemon && go test ./internal/store/ ./internal/pipeline/ -v`

Expected: all new tests `PASS`; no regressions in existing tests.

- [ ] **Step 2.15: Full daemon test suite**

Run: `make test-docker`

Expected: all packages pass.

- [ ] **Step 2.16: Commit**

```bash
git add daemon/internal/store/circuitbreaker.go \
        daemon/internal/store/circuitbreaker_test.go \
        daemon/internal/store/store.go \
        daemon/internal/config/circuit_breaker.go \
        daemon/internal/config/config.go \
        daemon/internal/sse/broker.go \
        daemon/internal/pipeline/pipeline.go \
        daemon/internal/pipeline/pipeline_reloop_test.go \
        daemon/cmd/heimdallm/main.go
git commit -m "$(cat <<'EOF'
feat(daemon): SQLite-backed circuit breaker caps review loops

Refs #243.

The 2026-04-22 cost-runaway loop had no hard ceiling — once every
dedup defense silently bypassed, the pipeline ran uncapped until the
team disabled Heimdallm by hand. €1300 lost across ~7 instances.

Add a SQLite-backed circuit breaker that checks review counts before
spending Claude credits:

- Per-PR cap (default 3 reviews / 24h, configurable).
- Per-repo cap (default 20 reviews / hour, configurable).
- Counters use the existing reviews table + two new indices (no new
  table needed). Survives daemon restart because SQLite persists.
- When tripped: log at Error, desktop notify, emit new SSE event
  circuit_breaker_tripped, and return a typed error so the caller
  skips instead of proceeding to Execute.
- [circuit_breaker] TOML section with conservative defaults. High-
  volume users must explicitly raise the caps.

Tests:
- Counter unit tests (per-PR window, per-repo across PRs).
- Breaker trip semantics.
- Pipeline integration test: breaker trip halts Execute.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 2.17: Push and open PR**

```bash
git push -u origin feat/circuit-breaker-review-cap
gh pr create --repo theburrowhub/heimdallm \
  --title "feat(daemon): SQLite-backed circuit breaker caps review loops" \
  --assignee ivanmunozruiz \
  --body "Refs #243 (Fix 1 in the priority list — the unconditional ceiling).

Ships after fix/pr-dedup-fail-closed-head-sha. Any other dedup defense may still fail — this PR is the defense of last resort that physically caps worst-case cost.

**Defaults**
- Per-PR: 3 reviews / 24h
- Per-repo: 20 reviews / hour
- Configurable via \`[circuit_breaker]\` in TOML

**Test plan**
- [x] Unit tests for counters and breaker
- [x] Integration test: pipeline refuses to call Execute when breaker trips
- [x] \`make test-docker\` green
- [ ] Manual: seed 4 reviews on a test PR, assert next poll is refused + SSE event arrives in the UI
"
```

---

## Task 3 (Fix 3) — `PublishedAt` anchor with 2-minute grace

**Why third:** restores the secondary dedup defense with a meaningful anchor. Must land after Task 1 (so every review persisted has a SHA) but independent of Task 2.

**Files:**
- Modify: `daemon/internal/store/reviews.go` — `Review.PublishedAt`, `InsertReview`, `scanReview`, `MarkReviewPublished` signature
- Modify: `daemon/internal/store/store.go` — `ALTER TABLE reviews ADD COLUMN published_at`
- Modify: `daemon/internal/pipeline/pipeline.go:335` — populate `PublishedAt` on successful submit
- Modify: `daemon/cmd/heimdallm/main.go:1446-1462` — `PRAlreadyReviewed` uses `PublishedAt` with 2m grace
- Create: `daemon/internal/pipeline/dedup.go` — shared helper
- Test: extend `daemon/internal/pipeline/pipeline_reloop_test.go`

**Branch:** `fix/pr-dedup-published-at-anchor`

- [ ] **Step 3.1: Start branch**

```bash
git checkout main && git pull --ff-only
git checkout -b fix/pr-dedup-published-at-anchor
```

- [ ] **Step 3.2: Write the failing test — slow review does not re-loop when anchored on PublishedAt**

Extend `daemon/internal/pipeline/pipeline_reloop_test.go`:

```go
func TestPRAlreadyReviewed_SlowReviewDoesNotReloop(t *testing.T) {
	s := newMemStore(t)
	prRow := &store.PR{GithubID: 99, Repo: "org/r", Number: 99, Title: "t",
		State: "open", UpdatedAt: time.Now()}
	prID, _ := s.UpsertPR(prRow)

	// Review started 3 minutes ago, posted to GitHub 30s ago (PublishedAt).
	startedAt := time.Now().Add(-3 * time.Minute)
	publishedAt := time.Now().Add(-30 * time.Second)
	if _, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: startedAt, PublishedAt: publishedAt,
		HeadSHA: "abc",
	}); err != nil { t.Fatal(err) }

	// GitHub's PR.updated_at was bumped 15s after PublishedAt — still inside
	// the 2-minute grace. Should be treated as "already reviewed".
	updatedAt := publishedAt.Add(15 * time.Second)
	adapter := newTestAdapter(s) // helper for calling PRAlreadyReviewed
	if !adapter.PRAlreadyReviewed(99, updatedAt) {
		t.Errorf("slow review (3 min) must not re-loop when updated_at is within 2m grace of PublishedAt")
	}
}

func TestPRAlreadyReviewed_FallsBackToCreatedAtWhenPublishedAtZero(t *testing.T) {
	s := newMemStore(t)
	prRow := &store.PR{GithubID: 100, Repo: "org/r", Number: 100, Title: "t",
		State: "open", UpdatedAt: time.Now()}
	prID, _ := s.UpsertPR(prRow)
	createdAt := time.Now().Add(-30 * time.Second)
	if _, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: createdAt, // PublishedAt zero
		HeadSHA: "abc",
	}); err != nil { t.Fatal(err) }

	updatedAt := createdAt.Add(10 * time.Second)
	adapter := newTestAdapter(s)
	if !adapter.PRAlreadyReviewed(100, updatedAt) {
		t.Errorf("legacy row (PublishedAt zero) must fall back to CreatedAt and still dedup")
	}
}

func TestPRAlreadyReviewed_AllowsReviewAfterGraceWindow(t *testing.T) {
	s := newMemStore(t)
	prRow := &store.PR{GithubID: 101, Repo: "org/r", Number: 101, Title: "t",
		State: "open", UpdatedAt: time.Now()}
	prID, _ := s.UpsertPR(prRow)
	publishedAt := time.Now().Add(-5 * time.Minute) // well outside 2m grace
	if _, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: publishedAt.Add(-1 * time.Minute),
		PublishedAt: publishedAt, HeadSHA: "abc",
	}); err != nil { t.Fatal(err) }

	updatedAt := time.Now()
	adapter := newTestAdapter(s)
	if adapter.PRAlreadyReviewed(101, updatedAt) {
		t.Errorf("activity 5 min after publish must be treated as new change (grace only 2m)")
	}
}
```

Add the `newTestAdapter` helper to the test file:

```go
// newTestAdapter constructs a minimal tier2Adapter-equivalent that exposes
// PRAlreadyReviewed for the test. Mirrors the real adapter in main.go but
// without the Flutter/scheduler/config deps we don't need here.
func newTestAdapter(s *store.Store) interface {
	PRAlreadyReviewed(githubID int64, updatedAt time.Time) bool
} {
	return pipeline.NewTestAdapter(s) // defined in pipeline_testutil.go
}
```

- [ ] **Step 3.3: Run the tests to verify they fail**

Run: `cd daemon && go test ./internal/pipeline/ -run "TestPRAlreadyReviewed" -v`

Expected: `FAIL` — `PublishedAt` field does not exist yet; compilation error.

- [ ] **Step 3.4: Add `PublishedAt` to the `Review` struct and schema**

Modify `daemon/internal/store/reviews.go`. Update the struct:

```go
type Review struct {
	ID                int64     `json:"id"`
	PRID              int64     `json:"pr_id"`
	CLIUsed           string    `json:"cli_used"`
	Summary           string    `json:"summary"`
	Issues            string    `json:"issues"`
	Suggestions       string    `json:"suggestions"`
	Severity          string    `json:"severity"`
	CreatedAt         time.Time `json:"created_at"`
	// PublishedAt is the local clock time immediately after SubmitReview
	// returned a success. Anchor for the updated_at dedup — using CreatedAt
	// made the 30s grace useless because CreatedAt is stamped BEFORE the
	// Claude call, so for any review taking longer than 30s the grace had
	// already expired when the review actually hit GitHub. See
	// theburrowhub/heimdallm#243.
	PublishedAt       time.Time `json:"published_at"`
	GitHubReviewID    int64     `json:"github_review_id"`
	GitHubReviewState string    `json:"github_review_state"`
	HeadSHA           string    `json:"head_sha"`
}
```

Update `InsertReview`:

```go
func (s *Store) InsertReview(r *Review) (int64, error) {
	publishedAt := ""
	if !r.PublishedAt.IsZero() {
		publishedAt = r.PublishedAt.UTC().Format(sqliteTimeFormat)
	}
	res, err := s.db.Exec(`
		INSERT INTO reviews (pr_id, cli_used, summary, issues, suggestions, severity, created_at, published_at, github_review_id, github_review_state, head_sha)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.PRID, r.CLIUsed, r.Summary, r.Issues, r.Suggestions, r.Severity,
		r.CreatedAt.UTC().Format(sqliteTimeFormat), publishedAt,
		r.GitHubReviewID, r.GitHubReviewState, r.HeadSHA,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert review: %w", err)
	}
	return res.LastInsertId()
}
```

Update the `SELECT` column lists in `ListUnpublishedReviews`, `ListReviewsForPR`, `LatestReviewForPR` to include `published_at`:

```go
"SELECT id, pr_id, cli_used, summary, issues, suggestions, severity, created_at, published_at, github_review_id, github_review_state, head_sha FROM reviews ..."
```

Update `scanReview`:

```go
func scanReview(s scanner) (*Review, error) {
	var rev Review
	var createdAt, publishedAt string
	var err error
	if err = s.Scan(&rev.ID, &rev.PRID, &rev.CLIUsed, &rev.Summary,
		&rev.Issues, &rev.Suggestions, &rev.Severity, &createdAt, &publishedAt,
		&rev.GitHubReviewID, &rev.GitHubReviewState, &rev.HeadSHA); err != nil {
		return nil, fmt.Errorf("store: scan review: %w", err)
	}
	if rev.CreatedAt, err = time.Parse(sqliteTimeFormat, createdAt); err != nil {
		return nil, fmt.Errorf("store: parse created_at %q: %w", createdAt, err)
	}
	if publishedAt != "" {
		if rev.PublishedAt, err = time.Parse(sqliteTimeFormat, publishedAt); err != nil {
			return nil, fmt.Errorf("store: parse published_at %q: %w", publishedAt, err)
		}
	}
	return &rev, nil
}
```

Update `MarkReviewPublished` to accept a `publishedAt`:

```go
// MarkReviewPublished records the GitHub review ID, state, and local post
// timestamp after a successful SubmitReview. publishedAt is stamped by the
// caller immediately after the API returned; anchoring the dedup window on
// this value (not on the row's CreatedAt, which precedes the Claude call)
// is what closes the 2026-04-22 cost-runaway regression. See
// theburrowhub/heimdallm#243.
func (s *Store) MarkReviewPublished(reviewID, ghReviewID int64, ghReviewState string, publishedAt time.Time) error {
	_, err := s.db.Exec(
		"UPDATE reviews SET github_review_id=?, github_review_state=?, published_at=? WHERE id=?",
		ghReviewID, ghReviewState,
		publishedAt.UTC().Format(sqliteTimeFormat), reviewID,
	)
	return err
}
```

Modify `daemon/internal/store/store.go` — add migration in the `Open` function next to the other `ALTER TABLE` lines:

```go
	db.Exec("ALTER TABLE reviews ADD COLUMN published_at TEXT NOT NULL DEFAULT ''")
```

Add the column to the `CREATE TABLE reviews` DDL (grep for `CREATE TABLE IF NOT EXISTS reviews`) so fresh installs also have it:

```go
  published_at   TEXT NOT NULL DEFAULT '',
```

Update all `MarkReviewPublished` call sites in `daemon/internal/pipeline/pipeline.go`. Grep first: `grep -n "MarkReviewPublished" daemon/internal/pipeline/pipeline.go`. At each call site, pass `time.Now().UTC()` immediately after `SubmitReview` succeeded:

```go
	publishedAt := time.Now().UTC()
	if err := p.store.MarkReviewPublished(rev.ID, ghReviewID, ghReviewState, publishedAt); err != nil {
		slog.Warn("pipeline: failed to mark review published", "err", err)
	}
	rev.PublishedAt = publishedAt
	rev.GitHubReviewID = ghReviewID
	rev.GitHubReviewState = ghReviewState
```

Also update the `MarkReviewPublished` call in `PublishPending` (grep for it — the retry path should stamp the new `publishedAt` too).

- [ ] **Step 3.5: Create the shared helper `ReviewFreshEnough`**

Create `daemon/internal/pipeline/dedup.go`:

```go
package pipeline

import "time"

// GraceDefault is the standard updated_at grace window applied by both the
// PR review dedup (see PRAlreadyReviewed in main.go) and the issue triage
// dedup (see issues/fetcher.go). 2 minutes absorbs GitHub replication lag
// plus any peer-bot submission timing without suppressing legitimate human
// activity (a human push within 2 min of a review is rare enough that we
// accept "picked up on the next tick" as the trade-off).
//
// See theburrowhub/heimdallm#243 for the incident and grace-duration
// analysis. Do NOT widen past 5 minutes without revisiting that analysis —
// longer windows blind the daemon to real human activity.
const GraceDefault = 2 * time.Minute

// ReviewFreshEnough returns true when `observed` (the GitHub updated_at
// we just fetched) is within `grace` of `anchor` (the local timestamp we
// recorded when the review was successfully posted). Used by both the PR
// and issue dedup paths so the two cannot drift apart.
//
// anchor.IsZero() is treated as "no fresh anchor, cannot dedup" — the
// caller decides whether to fall back to an older anchor (e.g. CreatedAt
// for legacy rows) or allow the review to run.
func ReviewFreshEnough(anchor, observed time.Time, grace time.Duration) bool {
	if anchor.IsZero() { return false }
	return !observed.After(anchor.Add(grace))
}
```

- [ ] **Step 3.6: Rewrite `PRAlreadyReviewed` to use the anchor**

Modify `daemon/cmd/heimdallm/main.go:1446-1462`. Replace the function body:

```go
func (a *tier2Adapter) PRAlreadyReviewed(githubID int64, updatedAt time.Time) bool {
	existing, _ := a.store.GetPRByGithubID(githubID)
	if existing == nil {
		return false
	}
	if existing.Dismissed {
		return true
	}
	rev, err := a.store.LatestReviewForPR(existing.ID)
	if err != nil || rev == nil {
		return false
	}
	// Prefer PublishedAt (stamped when SubmitReview returned); fall back to
	// CreatedAt for legacy rows. CreatedAt is stamped BEFORE the Claude call,
	// so a 30s grace on CreatedAt was useless for reviews taking >30s — the
	// 2026-04-22 cost-runaway regression. See theburrowhub/heimdallm#243.
	anchor := rev.PublishedAt
	if anchor.IsZero() {
		anchor = rev.CreatedAt
	}
	return pipeline.ReviewFreshEnough(anchor, updatedAt, pipeline.GraceDefault)
}
```

Add `pipeline` import at the top of the file if not already present.

- [ ] **Step 3.7: Add the `NewTestAdapter` helper**

Create `daemon/internal/pipeline/testutil_test.go` (or export a constructor in an existing test util):

```go
package pipeline

import (
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

// testAdapter is a stand-in for daemon/cmd/heimdallm.tier2Adapter used by
// the reloop tests. It mirrors the real PRAlreadyReviewed logic so we can
// regression-test the dedup anchor semantics without standing up the full
// scheduler + cfg + broker plumbing.
type testAdapter struct {
	store *store.Store
}

// NewTestAdapter is exported only for the regression tests in
// pipeline_reloop_test.go. Do NOT use in production code — the real
// adapter lives in cmd/heimdallm/main.go with the full scheduler plumbing.
func NewTestAdapter(s *store.Store) *testAdapter {
	return &testAdapter{store: s}
}

func (a *testAdapter) PRAlreadyReviewed(githubID int64, updatedAt time.Time) bool {
	existing, _ := a.store.GetPRByGithubID(githubID)
	if existing == nil || existing.Dismissed {
		return existing != nil
	}
	rev, err := a.store.LatestReviewForPR(existing.ID)
	if err != nil || rev == nil {
		return false
	}
	anchor := rev.PublishedAt
	if anchor.IsZero() { anchor = rev.CreatedAt }
	return ReviewFreshEnough(anchor, updatedAt, GraceDefault)
}
```

- [ ] **Step 3.8: Run the new tests**

Run: `cd daemon && go test ./internal/pipeline/ -run "TestPRAlreadyReviewed" -v`

Expected: `PASS`.

- [ ] **Step 3.9: Full daemon test suite**

Run: `make test-docker`

Expected: all packages pass, including every existing `MarkReviewPublished` and `Review` struct consumer that was auto-updated via compile errors.

- [ ] **Step 3.10: Commit**

```bash
git add daemon/internal/store/reviews.go \
        daemon/internal/store/store.go \
        daemon/internal/pipeline/pipeline.go \
        daemon/internal/pipeline/dedup.go \
        daemon/internal/pipeline/testutil_test.go \
        daemon/internal/pipeline/pipeline_reloop_test.go \
        daemon/cmd/heimdallm/main.go
git commit -m "$(cat <<'EOF'
fix(daemon): anchor PR dedup on PublishedAt with 2-minute grace

Refs #243.

Before: PRAlreadyReviewed compared updated_at against rev.CreatedAt +
30 seconds. CreatedAt is stamped BEFORE the Claude call, so for any
review taking longer than 30s the grace window was already expired
when the review actually posted — the bug from the 2026-04-22 loop.

Add a PublishedAt column stamped immediately after SubmitReview
returns successfully. Change the dedup to:
- Prefer PublishedAt (the actual post-to-GitHub time).
- Fall back to CreatedAt for legacy rows where PublishedAt is zero.
- Use a 2-minute grace instead of 30 seconds — wide enough to absorb
  GitHub replication + peer-bot submission timing, narrow enough to
  not suppress legitimate human activity.

Extract ReviewFreshEnough into a shared helper in pipeline/dedup.go
with GraceDefault = 2 * time.Minute so the PR and issue paths cannot
drift apart again (fetcher.go:20 already flagged this debt).

Tests:
- Slow review with PublishedAt within grace → no re-loop.
- Legacy row (PublishedAt zero) falls back to CreatedAt.
- Activity after the grace window is treated as new change.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 3.11: Push and open PR**

```bash
git push -u origin fix/pr-dedup-published-at-anchor
gh pr create --repo theburrowhub/heimdallm \
  --title "fix(daemon): anchor PR dedup on PublishedAt with 2-minute grace" \
  --assignee ivanmunozruiz \
  --body "Refs #243 (Fix 3).

Closes the secondary dedup defense by anchoring on the actual post-to-GitHub timestamp. Depends on the PublishedAt column migration.

**Test plan**
- [x] Slow-review scenario (review takes 3 min, dedup still holds)
- [x] Legacy row fallback (PublishedAt zero → CreatedAt anchor)
- [x] Activity past grace is treated as new change
- [x] \`make test-docker\` green
- [ ] Manual: deploy to dev, review a PR, assert exactly one review regardless of subsequent \`updated_at\` bumps
"
```

---

## Task 4 (Fix 4) — `ResetBackoff` uses observed timestamp; Tier 3 passes `snap.UpdatedAt`

**Why fourth:** restores the Tier 3 change-detection defense. Small refactor; one signature change + two callers.

**Files:**
- Modify: `daemon/internal/scheduler/queue.go:109-120` — signature + body
- Modify: `daemon/internal/scheduler/tier3.go:87` — pass `snap.UpdatedAt`
- Modify: `daemon/cmd/heimdallm/main.go:1582` — pass `snap.UpdatedAt` to `PRAlreadyReviewed`
- Test: extend `daemon/internal/scheduler/queue_test.go`

**Branch:** `fix/tier3-observed-updated-at`

- [ ] **Step 4.1: Start branch**

```bash
git checkout main && git pull --ff-only
git checkout -b fix/tier3-observed-updated-at
```

- [ ] **Step 4.2: Write the failing test**

Extend `daemon/internal/scheduler/queue_test.go`:

```go
func TestWatchQueue_ResetBackoff_StoresObservedUpdatedAt(t *testing.T) {
	q := NewWatchQueue()
	observed := time.Now().Add(-5 * time.Minute)
	item := &WatchItem{Type: "pr", GithubID: 100, Backoff: 8 * time.Minute}
	q.ResetBackoff(item, observed)
	if !item.LastSeen.Equal(observed) {
		t.Errorf("LastSeen = %v, want observed = %v", item.LastSeen, observed)
	}
	if item.Backoff != initialBackoff {
		t.Errorf("Backoff = %v, want initial = %v", item.Backoff, initialBackoff)
	}
}
```

- [ ] **Step 4.3: Run the test to verify it fails**

Run: `cd daemon && go test ./internal/scheduler/ -run "TestWatchQueue_ResetBackoff_StoresObservedUpdatedAt" -v`

Expected: compile error — `ResetBackoff` does not accept the second argument yet.

- [ ] **Step 4.4: Change the signature**

Modify `daemon/internal/scheduler/queue.go:109-120`:

```go
// ResetBackoff resets an item's backoff to initial and records the observed
// GitHub updated_at as LastSeen. Called when activity is detected on the
// item. Using observed (not time.Now()) is critical: if a Tier 3 check
// discovered the change via GitHub's updated_at, storing time.Now() as
// LastSeen would drift ahead of GitHub's clock and make subsequent ticks
// spuriously re-detect the same change. See theburrowhub/heimdallm#243.
//
// Same concurrency contract as ReEnqueue — caller must own the item
// exclusively.
func (q *WatchQueue) ResetBackoff(item *WatchItem, observedUpdatedAt time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.seen[item.GithubID] {
		return
	}
	item.Backoff = initialBackoff
	item.LastSeen = observedUpdatedAt
	item.NextCheck = time.Now().Add(initialBackoff)
	q.seen[item.GithubID] = true
	heap.Push(&q.items, item)
}
```

- [ ] **Step 4.5: Update the caller in tier3**

Modify `daemon/internal/scheduler/tier3.go:87`. The surrounding block is:

```go
	if changed {
		slog.Info("tier3: change detected",
			"type", item.Type, "repo", item.Repo, "number", item.Number)
		if err := deps.Checker.HandleChange(ctx, item, snap); err != nil {
			slog.Error("tier3: handle change", "err", err)
		}
		deps.Queue.ResetBackoff(item)
	} else {
```

Change the last line to:

```go
		// Pass the observed timestamp so LastSeen matches GitHub-side time,
		// not wall clock — see #243.
		observed := time.Time{}
		if snap != nil {
			observed = snap.UpdatedAt
		}
		deps.Queue.ResetBackoff(item, observed)
```

- [ ] **Step 4.6: Also update the existing `TestWatchQueue_ResetBackoff` test (if present)**

Grep for existing tests that call `ResetBackoff(item)`: `grep -n "ResetBackoff" daemon/internal/scheduler/queue_test.go`. For each, update to pass an observed timestamp (e.g., `time.Now()` preserves the old assertion's intent).

- [ ] **Step 4.7: Update the Tier 3 `PRAlreadyReviewed` call in main.go**

Modify `daemon/cmd/heimdallm/main.go:1582`:

```go
	// Mirror the Tier 2 updated_at dedup against the freshly-observed GitHub
	// snapshot timestamp, NOT item.LastSeen — the queue's LastSeen has
	// already been overwritten by ResetBackoff on earlier ticks and is no
	// longer a faithful representation of the PR's current updated_at.
	if a.PRAlreadyReviewed(item.GithubID, snap.UpdatedAt) {
		slog.Debug("tier3: PR already reviewed, skipping", "pr", item.Number, "repo", item.Repo)
		return nil
	}
```

- [ ] **Step 4.8: Run the tests**

Run: `cd daemon && go test ./internal/scheduler/ -v`

Expected: `PASS`, including the updated existing tests.

- [ ] **Step 4.9: Full daemon test suite**

Run: `make test-docker`

Expected: all packages pass.

- [ ] **Step 4.10: Commit**

```bash
git add daemon/internal/scheduler/queue.go \
        daemon/internal/scheduler/queue_test.go \
        daemon/internal/scheduler/tier3.go \
        daemon/cmd/heimdallm/main.go
git commit -m "$(cat <<'EOF'
fix(daemon): ResetBackoff stores observed updated_at, not wall clock

Refs #243.

Before: ResetBackoff stamped item.LastSeen = time.Now() unconditionally
and discarded the snap.UpdatedAt that triggered the change. The next
Tier 3 tick compared a wall-clock LastSeen against GitHub's updated_at
and spuriously re-detected the same change, especially when GitHub
bumped updated_at during an in-flight review.

Also: Tier 3's PRAlreadyReviewed call at main.go:1582 passed
item.LastSeen instead of snap.UpdatedAt, compounding the drift.

Fix: ResetBackoff now takes observedUpdatedAt and stores it. Tier 3
passes snap.UpdatedAt on both the ResetBackoff and PRAlreadyReviewed
calls, so every decision is anchored on GitHub-observed time.

Test added: TestWatchQueue_ResetBackoff_StoresObservedUpdatedAt.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin fix/tier3-observed-updated-at
gh pr create --repo theburrowhub/heimdallm \
  --title "fix(daemon): ResetBackoff stores observed updated_at, not wall clock" \
  --assignee ivanmunozruiz \
  --body "Refs #243 (Fix 4). Small refactor that restores the Tier 3 change-detection defense.

**Test plan**
- [x] Unit test on ResetBackoff signature
- [x] \`make test-docker\` green
"
```

---

## Task 5 (Fix 5) — Restart / reload hardening

**Why fifth:** closes the last amplifier — even with correct dedup, a daemon restart or config reload could wipe in-memory guards mid-loop and restart the leak.

**Sub-fix 5a (persistent in-flight):** store in-flight reviews in SQLite so they survive restart.

**Sub-fix 5b (delay first tick on reload):** Tier 2 should skip the immediate `processTick()` when the pipeline was just reloaded; cold start still fires immediately.

**Files:**
- Create: `daemon/internal/store/inflight.go`
- Create: `daemon/internal/store/inflight_test.go`
- Modify: `daemon/internal/store/store.go` — new table
- Modify: `daemon/cmd/heimdallm/main.go:252-263` — use persistent store instead of `inFlight` map
- Modify: `daemon/internal/scheduler/tier2.go:169-180` — accept a `coldStart bool` param
- Modify: `daemon/cmd/heimdallm/main.go` (reload path) — pass `coldStart=false`

**Branch:** `feat/persist-inflight-and-delay-first-tick`

- [ ] **Step 5.1: Start branch**

```bash
git checkout main && git pull --ff-only
git checkout -b feat/persist-inflight-and-delay-first-tick
```

- [ ] **Step 5.2: Write the failing test for in-flight persistence**

Create `daemon/internal/store/inflight_test.go`:

```go
package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func TestInFlight_ClaimAndRelease(t *testing.T) {
	s := newTestStore(t)
	claimed, err := s.ClaimInFlightReview(42, "abc123")
	if err != nil { t.Fatalf("claim: %v", err) }
	if !claimed {
		t.Errorf("first claim should succeed")
	}
	// Second claim on the same (pr_id, head_sha) must fail.
	claimed, err = s.ClaimInFlightReview(42, "abc123")
	if err != nil { t.Fatalf("second claim: %v", err) }
	if claimed {
		t.Errorf("second claim on same (pr, sha) must return false")
	}
	// Different SHA on the same PR is allowed (new commit).
	claimed, err = s.ClaimInFlightReview(42, "def456")
	if err != nil { t.Fatalf("new sha claim: %v", err) }
	if !claimed {
		t.Errorf("claim for new SHA must succeed")
	}
	// Release the first claim; should allow a re-claim.
	if err := s.ReleaseInFlightReview(42, "abc123"); err != nil {
		t.Fatalf("release: %v", err)
	}
	claimed, err = s.ClaimInFlightReview(42, "abc123")
	if err != nil { t.Fatalf("re-claim: %v", err) }
	if !claimed {
		t.Errorf("re-claim after release must succeed")
	}
}

func TestInFlight_StaleEntriesAreCleared(t *testing.T) {
	s := newTestStore(t)
	// Simulate a stale row from a crashed daemon.
	if err := s.InsertStaleInFlight(42, "abc123", time.Now().Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearStaleInFlight(30 * time.Minute)
	if err != nil { t.Fatalf("clear: %v", err) }
	if n != 1 {
		t.Errorf("want 1 stale row cleared, got %d", n)
	}
	// The row should now be claimable again.
	claimed, err := s.ClaimInFlightReview(42, "abc123")
	if err != nil { t.Fatalf("claim after clear: %v", err) }
	if !claimed {
		t.Errorf("claim after stale-clear must succeed")
	}
}
```

- [ ] **Step 5.3: Run the test to verify it fails**

Run: `cd daemon && go test ./internal/store/ -run "TestInFlight" -v`

Expected: compile error — methods and table do not exist.

- [ ] **Step 5.4: Implement the in-flight table**

Add to the `schema` constant in `daemon/internal/store/store.go` (grep for `CREATE TABLE IF NOT EXISTS` and append):

```go
CREATE TABLE IF NOT EXISTS reviews_in_flight (
  pr_id       INTEGER NOT NULL,
  head_sha    TEXT    NOT NULL,
  started_at  DATETIME NOT NULL,
  PRIMARY KEY (pr_id, head_sha)
);
```

Add to the `Open` function (next to the other `ALTER TABLE` migrations):

```go
	// Idempotent — new installs get this from schema; existing DBs need the
	// CREATE. Not an ALTER because the table didn't exist before #243.
	db.Exec(`CREATE TABLE IF NOT EXISTS reviews_in_flight (
		pr_id       INTEGER NOT NULL,
		head_sha    TEXT    NOT NULL,
		started_at  DATETIME NOT NULL,
		PRIMARY KEY (pr_id, head_sha)
	)`)
```

Create `daemon/internal/store/inflight.go`:

```go
package store

import (
	"fmt"
	"time"
)

// ClaimInFlightReview inserts a row marking (prID, headSHA) as currently
// being reviewed. Returns (true, nil) on successful claim, (false, nil) if
// another daemon (or this one, pre-restart) already claimed it. Errors
// surface real SQLite problems, not contention.
//
// headSHA is part of the composite key so a new commit on an in-flight PR
// can be reviewed immediately; only the same commit is deduplicated.
func (s *Store) ClaimInFlightReview(prID int64, headSHA string) (bool, error) {
	res, err := s.db.Exec(
		"INSERT OR IGNORE INTO reviews_in_flight (pr_id, head_sha, started_at) VALUES (?, ?, ?)",
		prID, headSHA, time.Now().UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return false, fmt.Errorf("store: claim inflight: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil { return false, fmt.Errorf("store: claim inflight rowsaffected: %w", err) }
	return n == 1, nil
}

// ReleaseInFlightReview removes the (prID, headSHA) row so the pair can be
// re-claimed. Always call in a defer from the caller that successfully
// claimed; no-op if the row doesn't exist.
func (s *Store) ReleaseInFlightReview(prID int64, headSHA string) error {
	_, err := s.db.Exec(
		"DELETE FROM reviews_in_flight WHERE pr_id = ? AND head_sha = ?",
		prID, headSHA,
	)
	if err != nil {
		return fmt.Errorf("store: release inflight: %w", err)
	}
	return nil
}

// ClearStaleInFlight removes claims older than `maxAge`. Protects against
// claims leaked by a daemon that crashed between claim and release. Safe to
// call on every startup; returns the number of rows cleared.
func (s *Store) ClearStaleInFlight(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(sqliteTimeFormat)
	res, err := s.db.Exec("DELETE FROM reviews_in_flight WHERE started_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("store: clear stale inflight: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil { return 0, fmt.Errorf("store: clear stale rowsaffected: %w", err) }
	return int(n), nil
}

// InsertStaleInFlight is test-only: seeds an in-flight row with a custom
// started_at so ClearStaleInFlight can be exercised deterministically.
func (s *Store) InsertStaleInFlight(prID int64, headSHA string, startedAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT INTO reviews_in_flight (pr_id, head_sha, started_at) VALUES (?, ?, ?)",
		prID, headSHA, startedAt.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return fmt.Errorf("store: insert stale inflight: %w", err)
	}
	return nil
}
```

- [ ] **Step 5.5: Run the in-flight tests**

Run: `cd daemon && go test ./internal/store/ -run "TestInFlight" -v`

Expected: `PASS`.

- [ ] **Step 5.6: Replace the in-memory inFlight map with the persistent claim**

In `daemon/cmd/heimdallm/main.go:249-263`, replace the `inFlight` map check with a SQLite claim:

```go
	runReview := func(pr *gh.PullRequest, aiCfg config.RepoAI) {
		// Persistent in-flight claim: survives daemon restart and config reload.
		// Keyed on (pr_id, head_sha) so a new commit on the same PR is not
		// gated by a stale in-flight row from a prior HEAD.
		//
		// We use pr.ID as the store's PR id when available; for early-stage
		// PRs that have not yet been upserted, skip the claim and rely on
		// the downstream SHA dedup (cheap path).
		stored, _ := a.store.GetPRByGithubID(pr.ID)
		var claimed bool
		if stored != nil && pr.Head.SHA != "" {
			ok, err := a.store.ClaimInFlightReview(stored.ID, pr.Head.SHA)
			if err != nil {
				slog.Warn("runReview: claim inflight failed, proceeding", "err", err)
			} else if !ok {
				slog.Info("runReview: already in flight (persistent), skipping",
					"pr", pr.Number, "repo", pr.Repo)
				return
			} else {
				claimed = true
			}
		}
		defer func() {
			if claimed {
				if err := a.store.ReleaseInFlightReview(stored.ID, pr.Head.SHA); err != nil {
					slog.Warn("runReview: release inflight failed", "err", err)
				}
			}
		}()
		// ... rest of existing runReview body ...
```

Delete the `inFlight map[int64]bool` + `reviewMu` fields and the old guard at the top of `runReview`.

Add a startup call to clear stale claims in `main()` right after `store.Open`:

```go
	if n, err := store.ClearStaleInFlight(30 * time.Minute); err != nil {
		slog.Warn("startup: clear stale inflight failed", "err", err)
	} else if n > 0 {
		slog.Info("startup: cleared stale inflight rows", "count", n)
	}
```

- [ ] **Step 5.7: Delay first Tier 2 tick on reload**

Modify `daemon/internal/scheduler/tier2.go:169-180`. Change `RunTier2` to accept a `coldStart bool` (or read it from `Tier2Deps`):

```go
// RunTier2 runs the review-requested polling tier.
//
// coldStart controls the behaviour of the very first tick:
//   - true (initial daemon startup): fire processTick() immediately so the
//     UI sees activity without waiting PollInterval.
//   - false (pipeline reload): skip the immediate tick. A reload is often
//     triggered by a UI config PATCH and firing the tick immediately would
//     re-poll every repo before backoff state has settled, amplifying any
//     in-flight review loop. See theburrowhub/heimdallm#243.
func RunTier2(ctx context.Context, deps Tier2Deps, coldStart bool) {
	ticker := time.NewTicker(deps.PollInterval)
	defer ticker.Stop()

	processTick := func() {
		// ... existing body ...
	}

	if coldStart {
		processTick()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processTick()
		}
	}
}
```

In `daemon/cmd/heimdallm/main.go`, locate every call site of `RunTier2`. The initial startup invocation passes `true`; the reload path passes `false`. Grep: `grep -n "RunTier2" daemon/cmd/heimdallm/main.go`. At the reload call site (inside `reloadFn`), change to `RunTier2(ctx, deps, false)`; at the startup site, `RunTier2(ctx, deps, true)`.

- [ ] **Step 5.8: Run all tests**

Run: `make test-docker`

Expected: all packages pass. If the signature change broke other callers, fix them to pass the correct `coldStart` value.

- [ ] **Step 5.9: Commit**

```bash
git add daemon/internal/store/inflight.go \
        daemon/internal/store/inflight_test.go \
        daemon/internal/store/store.go \
        daemon/internal/scheduler/tier2.go \
        daemon/cmd/heimdallm/main.go
git commit -m "$(cat <<'EOF'
feat(daemon): persist in-flight reviews and delay first tick on reload

Refs #243.

Two amplifiers of the 2026-04-22 cost-runaway loop:

5a. The inFlight map was process-local heap state. A daemon restart
    (or a config PATCH triggering reloadFn) wiped it, and Tier 2's
    first tick fires immediately — so the same PR could be re-picked
    up with no in-flight guard in place. Replace the map with a
    persistent reviews_in_flight table keyed on (pr_id, head_sha),
    claimed via INSERT OR IGNORE. On startup, ClearStaleInFlight
    removes rows older than 30 minutes (protects against a daemon that
    crashed between claim and release).

5b. RunTier2 now takes coldStart bool. On initial startup (true), it
    runs the first processTick() immediately for UX. On pipeline
    reload (false) it waits one full PollInterval before the first
    tick — so a UI config PATCH cannot re-trigger the whole fleet by
    accident.

Tests:
- ClaimInFlightReview + ReleaseInFlightReview round-trip.
- ClearStaleInFlight honours the maxAge cutoff.
- Existing Tier 2 tests pass (signature migrated).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin feat/persist-inflight-and-delay-first-tick
gh pr create --repo theburrowhub/heimdallm \
  --title "feat(daemon): persist in-flight reviews and delay first tick on reload" \
  --assignee ivanmunozruiz \
  --body "Refs #243 (Fix 5). Closes the restart / reload amplifier.

**Test plan**
- [x] Unit tests for claim / release / clear-stale
- [x] \`make test-docker\` green
- [ ] Manual: kill daemon mid-review, restart, assert the review does not re-run (same SHA) and stale claims are cleared after 30 minutes
- [ ] Manual: PATCH /config, assert Tier 2 does not immediately re-poll every repo
"
```

---

## Task 6 (Fix 6) — Multi-instance regression test + Flutter banner

**Why last:** by the time you get here, the fixes are in. Task 6 is the "this cannot recur" seal: a regression test that simulates two daemons sharing a SQLite, plus a UI banner when the breaker trips so the user sees it.

**Files:**
- Extend: `daemon/internal/pipeline/pipeline_reloop_test.go` — multi-instance scenario
- Create: `flutter_app/lib/features/circuit_breaker/circuit_breaker_banner.dart`
- Modify: `flutter_app/lib/features/dashboard/dashboard_providers.dart` — subscribe to `circuit_breaker_tripped` SSE
- Modify: `flutter_app/lib/features/dashboard/dashboard_screen.dart` — show banner

**Branch:** `feat/circuit-breaker-banner-and-multi-instance-test`

- [ ] **Step 6.1: Start branch**

```bash
git checkout main && git pull --ff-only
git checkout -b feat/circuit-breaker-banner-and-multi-instance-test
```

- [ ] **Step 6.2: Write the multi-instance regression test**

Extend `daemon/internal/pipeline/pipeline_reloop_test.go`:

```go
func TestRun_TwoInstancesSharingStoreDoNotDoubleReview(t *testing.T) {
	// Two tier2Adapters sharing the same SQLite simulates two team members'
	// daemons on the same repo. Instance A runs the review, persists the
	// row. Instance B immediately checks PRAlreadyReviewed; the shared
	// PublishedAt must dedup it.
	s := newMemStore(t)
	prRow := &store.PR{GithubID: 1234, Repo: "org/r", Number: 1234,
		Title: "t", State: "open", UpdatedAt: time.Now()}
	prID, _ := s.UpsertPR(prRow)

	publishedAt := time.Now()
	if _, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: publishedAt.Add(-2 * time.Minute),
		PublishedAt: publishedAt, HeadSHA: "abc",
	}); err != nil { t.Fatal(err) }

	// Simulate GitHub's updated_at bump from A's review submission.
	updatedAt := publishedAt.Add(5 * time.Second)

	// B is a fresh adapter instance on the same store.
	adapterB := pipeline.NewTestAdapter(s)
	if !adapterB.PRAlreadyReviewed(1234, updatedAt) {
		t.Errorf("Instance B must dedup against Instance A's PublishedAt in the shared store")
	}
}
```

- [ ] **Step 6.3: Run the test**

Run: `cd daemon && go test ./internal/pipeline/ -run "TestRun_TwoInstancesSharingStore" -v`

Expected: `PASS` (the Fix 3 changes should already make this work).

- [ ] **Step 6.4: Create the Flutter banner widget**

Create `flutter_app/lib/features/circuit_breaker/circuit_breaker_banner.dart`:

```dart
import 'package:flutter/material.dart';

/// Banner shown when the daemon's review circuit breaker has tripped.
/// Dismiss is explicit — the user must acknowledge seeing the warning so
/// they can't miss a cost event. The message is sourced from the SSE
/// event payload.
class CircuitBreakerBanner extends StatelessWidget {
  final String message;
  final VoidCallback onDismiss;
  const CircuitBreakerBanner({
    super.key,
    required this.message,
    required this.onDismiss,
  });

  @override
  Widget build(BuildContext context) {
    return MaterialBanner(
      backgroundColor: Colors.red.shade50,
      leading: const Icon(Icons.warning_amber_rounded, color: Colors.red),
      content: Text(
        'Review circuit breaker tripped — $message',
        style: const TextStyle(color: Colors.black87),
      ),
      actions: [
        TextButton(onPressed: onDismiss, child: const Text('Dismiss')),
      ],
    );
  }
}
```

- [ ] **Step 6.5: Subscribe to the SSE event and show the banner**

Modify `flutter_app/lib/features/dashboard/dashboard_providers.dart`. Add a provider for the latest circuit-breaker event:

```dart
/// Latest circuit-breaker-tripped payload from the daemon. Null until the
/// breaker fires or after the user dismisses the banner.
final circuitBreakerProvider = StateProvider<String?>((ref) => null);
```

In the `_handleSseEvent` function (grep for it in the same file), add a case:

```dart
      case 'circuit_breaker_tripped':
        final repo = data['repo'] as String? ?? 'unknown';
        final prNumber = (data['pr_number'] as num?)?.toInt() ?? 0;
        final reason = data['reason'] as String? ?? '';
        ref.read(circuitBreakerProvider.notifier).state =
            '$repo #$prNumber — $reason';
```

- [ ] **Step 6.6: Render the banner in the dashboard**

Modify `flutter_app/lib/features/dashboard/dashboard_screen.dart`. At the top of the scaffold body (inside `build`), watch the provider and show the banner when non-null:

```dart
    final cbMessage = ref.watch(circuitBreakerProvider);
    // ... existing build ...
    return Scaffold(
      body: Column(
        children: [
          if (cbMessage != null)
            CircuitBreakerBanner(
              message: cbMessage,
              onDismiss: () =>
                  ref.read(circuitBreakerProvider.notifier).state = null,
            ),
          Expanded(child: /* existing body */),
        ],
      ),
    );
```

Add the import at the top:

```dart
import '../circuit_breaker/circuit_breaker_banner.dart';
```

- [ ] **Step 6.7: Add a Flutter widget test**

Create `flutter_app/test/features/circuit_breaker_banner_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/circuit_breaker/circuit_breaker_banner.dart';

void main() {
  testWidgets('CircuitBreakerBanner shows the message and dismisses', (tester) async {
    var dismissed = false;
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: CircuitBreakerBanner(
          message: 'org/r #42 — per-PR cap reached',
          onDismiss: () => dismissed = true,
        ),
      ),
    ));

    expect(find.textContaining('org/r #42'), findsOneWidget);
    expect(find.textContaining('per-PR cap reached'), findsOneWidget);

    await tester.tap(find.text('Dismiss'));
    await tester.pumpAndSettle();
    expect(dismissed, isTrue);
  });
}
```

- [ ] **Step 6.8: Run Flutter and Go tests**

Run: `cd flutter_app && flutter test`
Expected: all tests pass.

Run: `make test-docker`
Expected: all packages pass.

- [ ] **Step 6.9: Commit**

```bash
git add daemon/internal/pipeline/pipeline_reloop_test.go \
        flutter_app/lib/features/circuit_breaker/ \
        flutter_app/lib/features/dashboard/dashboard_providers.dart \
        flutter_app/lib/features/dashboard/dashboard_screen.dart \
        flutter_app/test/features/circuit_breaker_banner_test.dart
git commit -m "$(cat <<'EOF'
test+ui(heimdallm): multi-instance regression test + breaker banner

Refs #243.

- Add pipeline_reloop_test.go case simulating two daemons sharing a
  SQLite store: B must dedup against A's PublishedAt. Locks in the
  cross-instance behaviour that the original incident lacked.
- Flutter: subscribe to the new circuit_breaker_tripped SSE event and
  render a MaterialBanner the user must dismiss. Cost events cannot
  slip by silently like they did on 2026-04-22.

Widget test covers the banner + dismiss path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin feat/circuit-breaker-banner-and-multi-instance-test
gh pr create --repo theburrowhub/heimdallm \
  --title "test+ui(heimdallm): multi-instance regression test + circuit breaker banner" \
  --assignee ivanmunozruiz \
  --body "Refs #243 (Fix 6). Closes the loop: the regression test proves cross-instance dedup works, and the banner guarantees the user sees a breaker trip instead of discovering it a day later in the billing console.

**Test plan**
- [x] Multi-instance Go test
- [x] Widget test
- [x] \`make test-docker\` + \`flutter test\` green
- [ ] Manual: trip the breaker in dev, assert the banner appears in the Flutter UI and dismisses cleanly
"
```

---

## Self-review

**Spec coverage** — each #243 fix mapped to a task:

- Fix 1 (circuit breaker) → Task 2
- Fix 2 (fail-closed SHA) → Task 1
- Fix 3 (PublishedAt + 2 min grace) → Task 3
- Fix 4 (ResetBackoff signature) → Task 4
- Fix 5 (restart/reload hardening) → Task 5
- Fix 6 (regression tests) → interleaved across Tasks 1, 2, 3, plus a final multi-instance + UI test in Task 6

**Shared helper `ReviewFreshEnough`** (Task 3.5) addresses the `fetcher.go:20` debt comment — one implementation serving both PR and issue paths.

**Placeholder scan** — no "TBD", no "add error handling", every step has a test or a code block. `pipeline.NewForTest` is defined in Task 1.2 (or noted to be added in that commit); `PRAlreadyReviewed` is the existing signature in `main.go:1446` unchanged by Task 3 (just the body). `NewTestAdapter` is created in Task 3.7 and reused in Task 6.2.

**Type consistency**:
- `Review.PublishedAt` (Task 3.4) and `MarkReviewPublished(..., publishedAt time.Time)` (Task 3.4) match.
- `ResetBackoff(item, observed time.Time)` (Task 4.4) and caller `deps.Queue.ResetBackoff(item, snap.UpdatedAt)` (Task 4.5) match.
- `ClaimInFlightReview(prID int64, headSHA string)` (Task 5.4) and caller in `runReview` (Task 5.6) match.
- `CircuitBreakerLimits{PerPR24h, PerRepoHr}` (Task 2.8) and `SetCircuitBreakerLimits(*store.CircuitBreakerLimits)` (Task 2.12) match.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-23-stop-pr-review-cost-runaway.md`. Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session with checkpoints between each.

Recommended path: Subagent-Driven for Tasks 1 and 2 (to ship the critical defenses fast with separate reviews), then Inline for the rest if the earlier reviews are clean.
