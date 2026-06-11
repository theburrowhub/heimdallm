# Autonomous end-to-end Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a fully unattended mode to the Heimdallm daemon that autonomously selects an issue, drives it through triage→refinement→development→PR, and reacts to reviews (fix on changes/comments/approved-with-issues; merge gate built but disabled).

**Architecture:** A new `daemon/internal/autonomous/` package adds a `Selector` (task-selection cascade) and an `Orchestrator` (single-flight 4-phase chaining) mounted in front of the existing issue pipeline, workers and Tier 3 review loop. Safety lives entirely in the circuit-breaker family: a new per-repo·hour `implement` breaker plus global→org→repo layering of all breakers. New code depends on small local interfaces wrapping the confirmed `github.Client` and `store.Store` methods, mirroring how `Responder`/`FixRunner` are built.

**Tech Stack:** Go (daemon), SQLite (`store`), NATS (`bus`), SSE (`sse`). Tests with the standard library `testing` package and `-race`, run via `cd daemon && go test ./... -race`.

**Spec:** `docs/superpowers/specs/2026-06-12-autonomous-mode-design.md`

---

## File Structure

**New files:**
- `daemon/internal/config/autonomous.go` — `AutonomousConfig` + `AutonomousForRepo` resolver.
- `daemon/internal/config/autonomous_test.go`
- `daemon/internal/autonomous/selector.go` — task-selection cascade + courtesy reassignment.
- `daemon/internal/autonomous/selector_test.go`
- `daemon/internal/autonomous/orchestrator.go` — single-flight 4-phase chaining.
- `daemon/internal/autonomous/orchestrator_test.go`
- `daemon/internal/autonomous/review_class.go` — approved-with-issues classifier.
- `daemon/internal/autonomous/review_class_test.go`
- `daemon/internal/autonomous/singleflight.go` — concurrency=1 phase guard.
- `daemon/internal/autonomous/singleflight_test.go`

**Modified files:**
- `daemon/internal/config/config.go` — `CircuitBreakerConfig.PerImplRepoHr`, layering fields on `OrgAI`/`RepoAI`, `CircuitBreakerForRepo`, defaults.
- `daemon/internal/config/circuit_breaker.go` (if the struct lives there) — `PerImplRepoHr` field.
- `daemon/internal/store/store.go` — `issues.claimed_by_autonomous` column + idempotent migration.
- `daemon/internal/store/issue_circuitbreaker.go` — `CountImplementsForRepo`, `CheckImplementCircuitBreaker`.
- `daemon/internal/store/issues.go` — `SetIssueClaimedByAutonomous`, `ListAutonomousCandidates`.
- `daemon/internal/github/client.go` (or `repos.go`/`pr_reviews.go`) — `AddAssignees`, `BranchExists`, `MergePR`.
- `daemon/internal/sse/broker.go` — new event-type constants.
- `daemon/cmd/heimdallm/main.go` — wire the autonomous poller, `CircuitBreakerForRepo`, merge gate, classifier.

---

## Conventions for every task

- Work from `daemon/`. Run tests with: `cd daemon && go test ./internal/<pkg>/... -race`
- Full suite gate before any PR: `cd daemon && go vet ./... && go test ./... -race`
- Follow existing error-wrapping style: `fmt.Errorf("autonomous: <action>: %w", err)`.
- Untrusted free text posted to GitHub MUST pass `sanitiseUntrustedFreeText` (already in `issues`); the coordination-comment task reuses that fence pattern.

---

## Task 1: Add `PerImplRepoHr` field + breaker layering structs + resolver

**Files:**
- Modify: `daemon/internal/config/config.go` (`CircuitBreakerConfig`, `OrgAI`, `RepoAI`, `applyDefaults`, new `CircuitBreakerForRepo`)
- Test: `daemon/internal/config/circuit_breaker_layering_test.go` (create)

> Note: `CircuitBreakerConfig` is declared in `config.go` and embedded in `Config` as `CircuitBreaker CircuitBreakerConfig`. If your tree declares it in `circuit_breaker.go`, edit it there — the field set is identical.

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/config/circuit_breaker_layering_test.go`:

```go
package config

import "testing"

func TestCircuitBreakerForRepo_GlobalOrgRepoPrecedence(t *testing.T) {
	repoCB := CircuitBreakerConfig{PerImplRepoHr: 9}
	orgCB := CircuitBreakerConfig{PerImplRepoHr: 7, PerRepoHr: 50}
	c := &Config{
		CircuitBreaker: CircuitBreakerConfig{
			PerPR24h: 3, PerRepoHr: 20, PerIssue24h: 3, PerIssueRepoHr: 10, PerImplRepoHr: 5,
		},
		AI: AIConfig{
			Orgs:  map[string]OrgAI{"acme": {CircuitBreaker: &orgCB}},
			Repos: map[string]RepoAI{"acme/widget": {CircuitBreaker: &repoCB}},
		},
	}

	// repo wins for the field it sets, inherits the rest from org then global.
	got := c.CircuitBreakerForRepo("acme/widget")
	if got.PerImplRepoHr != 9 {
		t.Errorf("PerImplRepoHr: want 9 (repo), got %d", got.PerImplRepoHr)
	}
	if got.PerRepoHr != 50 {
		t.Errorf("PerRepoHr: want 50 (org), got %d", got.PerRepoHr)
	}
	if got.PerPR24h != 3 {
		t.Errorf("PerPR24h: want 3 (global), got %d", got.PerPR24h)
	}

	// org-only repo: inherits org over global.
	gotOrg := c.CircuitBreakerForRepo("acme/other")
	if gotOrg.PerImplRepoHr != 7 {
		t.Errorf("org repo PerImplRepoHr: want 7, got %d", gotOrg.PerImplRepoHr)
	}

	// unknown repo: pure global.
	gotGlobal := c.CircuitBreakerForRepo("none/none")
	if gotGlobal.PerImplRepoHr != 5 {
		t.Errorf("global PerImplRepoHr: want 5, got %d", gotGlobal.PerImplRepoHr)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/config/ -run TestCircuitBreakerForRepo_GlobalOrgRepoPrecedence -race`
Expected: FAIL — `got.PerImplRepoHr undefined`, `OrgAI/RepoAI has no field CircuitBreaker`, `c.CircuitBreakerForRepo undefined`.

- [ ] **Step 3: Add the `PerImplRepoHr` field**

In `config.go`, locate `CircuitBreakerConfig` and add the field (keep the existing four):

```go
type CircuitBreakerConfig struct {
	PerPR24h       int `toml:"per_pr_24h"`
	PerRepoHr      int `toml:"per_repo_hr"`
	PerIssue24h    int `toml:"per_issue_24h"`
	PerIssueRepoHr int `toml:"per_issue_repo_hr"`
	// PerImplRepoHr caps auto_implement (development) runs per repo in any
	// 1h window. This is the "breadth" guard autonomous mode relies on:
	// the per-issue breaker only counts triages (review_only), leaving
	// development uncapped. 0 = unlimited.
	PerImplRepoHr int `toml:"per_impl_repo_hr"`
}
```

> If `CircuitBreakerConfig` already has TOML tags, keep them and only append `PerImplRepoHr`.

- [ ] **Step 4: Add the breaker override pointer to `OrgAI` and `RepoAI`**

In `OrgAI` (after `IssueTracking`):

```go
	// CircuitBreaker overrides the org's circuit-breaker caps. nil = inherit
	// global. Resolved by Config.CircuitBreakerForRepo (repo > org > global).
	CircuitBreaker *CircuitBreakerConfig `toml:"circuit_breaker,omitempty"`
```

In `RepoAI` (after `IssueTracking`):

```go
	// CircuitBreaker overrides the repo's circuit-breaker caps. nil = inherit
	// org then global. Resolved by Config.CircuitBreakerForRepo.
	CircuitBreaker *CircuitBreakerConfig `toml:"circuit_breaker,omitempty"`
```

- [ ] **Step 5: Add the `PerImplRepoHr` default**

In `applyDefaults`, alongside the existing breaker defaults:

```go
	if c.CircuitBreaker.PerImplRepoHr == 0 {
		c.CircuitBreaker.PerImplRepoHr = 5
	}
```

- [ ] **Step 6: Add the `CircuitBreakerForRepo` resolver**

Append to `config.go` (mirrors `AIForRepo`):

```go
// CircuitBreakerForRepo resolves circuit-breaker caps for a repo through
// three levels: per-repo > per-org > global. A nil override at a level is
// skipped; a present override fully replaces the value inherited so far.
func (c *Config) CircuitBreakerForRepo(repo string) CircuitBreakerConfig {
	out := c.CircuitBreaker
	if org := repoOrg(repo); org != "" && c.AI.Orgs != nil {
		if o, ok := c.AI.Orgs[org]; ok && o.CircuitBreaker != nil {
			out = mergeCircuitBreaker(out, *o.CircuitBreaker)
		}
	}
	if c.AI.Repos != nil {
		if r, ok := c.AI.Repos[repo]; ok && r.CircuitBreaker != nil {
			out = mergeCircuitBreaker(out, *r.CircuitBreaker)
		}
	}
	return out
}

// mergeCircuitBreaker overlays non-zero fields of override onto base, so an
// org/repo can tune a single axis without restating the rest.
func mergeCircuitBreaker(base, override CircuitBreakerConfig) CircuitBreakerConfig {
	if override.PerPR24h != 0 {
		base.PerPR24h = override.PerPR24h
	}
	if override.PerRepoHr != 0 {
		base.PerRepoHr = override.PerRepoHr
	}
	if override.PerIssue24h != 0 {
		base.PerIssue24h = override.PerIssue24h
	}
	if override.PerIssueRepoHr != 0 {
		base.PerIssueRepoHr = override.PerIssueRepoHr
	}
	if override.PerImplRepoHr != 0 {
		base.PerImplRepoHr = override.PerImplRepoHr
	}
	return base
}
```

- [ ] **Step 7: Run test to verify it passes**

Run: `cd daemon && go test ./internal/config/ -run TestCircuitBreakerForRepo_GlobalOrgRepoPrecedence -race`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add daemon/internal/config/config.go daemon/internal/config/circuit_breaker_layering_test.go
git commit -m "feat(config): add PerImplRepoHr breaker + global->org->repo layering"
```

---

## Task 2: Store — count implements + implement circuit breaker

**Files:**
- Modify: `daemon/internal/store/issue_circuitbreaker.go`
- Test: `daemon/internal/store/issue_circuitbreaker_impl_test.go` (create)

Context: `issue_reviews` rows record `action_taken`. Development runs use one of `auto_implement`, `auto_implement_failed`, `auto_implement_no_changes` (per the design spec). We count those per repo within 1h.

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/store/issue_circuitbreaker_impl_test.go`:

```go
package store

import (
	"testing"
	"time"
)

func TestCheckImplementCircuitBreaker_PerRepoHr(t *testing.T) {
	s := newTestStore(t) // existing test helper in this package

	repo := "acme/widget"
	// Seed 2 implement runs in the last hour for this repo.
	seedImplementReview(t, s, repo, 101, "auto_implement", time.Now().Add(-10*time.Minute))
	seedImplementReview(t, s, repo, 102, "auto_implement_failed", time.Now().Add(-20*time.Minute))

	tripped, reason, err := s.CheckImplementCircuitBreaker(repo, IssueCircuitBreakerLimits{PerRepoHr: 2})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !tripped {
		t.Fatalf("want tripped at cap 2 with 2 implements, got not tripped")
	}
	if reason == "" {
		t.Errorf("want non-empty reason when tripped")
	}

	// Below cap.
	tripped, _, err = s.CheckImplementCircuitBreaker(repo, IssueCircuitBreakerLimits{PerRepoHr: 5})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if tripped {
		t.Errorf("want not tripped at cap 5 with 2 implements")
	}

	// Unlimited (0) never trips.
	tripped, _, _ = s.CheckImplementCircuitBreaker(repo, IssueCircuitBreakerLimits{PerRepoHr: 0})
	if tripped {
		t.Errorf("cap 0 must mean unlimited")
	}
}
```

> If `newTestStore`/`seedImplementReview` helpers do not yet exist in the package, add `seedImplementReview` next to the other test seed helpers. Inspect an existing `*_test.go` in `store/` for the `newTestStore` pattern and reuse it; write `seedImplementReview` to insert one `issue_reviews` row with the given `repo`, issue id, `action_taken`, and `created_at`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/store/ -run TestCheckImplementCircuitBreaker_PerRepoHr -race`
Expected: FAIL — `s.CheckImplementCircuitBreaker undefined`.

- [ ] **Step 3: Implement `CountImplementsForRepo` + `CheckImplementCircuitBreaker`**

Append to `daemon/internal/store/issue_circuitbreaker.go`:

```go
// CountImplementsForRepo counts auto_implement attempts (success, failure,
// or no-changes) recorded for a repo since `since`. Used by the autonomous
// implement breaker — the per-issue breaker counts only review_only.
func (s *Store) CountImplementsForRepo(repo string, since time.Time) (int, error) {
	const q = `
SELECT COUNT(*) FROM issue_reviews ir
JOIN issues i ON i.id = ir.issue_id
WHERE i.repo = ?
  AND ir.created_at >= ?
  AND ir.action_taken IN ('auto_implement','auto_implement_failed','auto_implement_no_changes')`
	var n int
	if err := s.db.QueryRow(q, repo, since.UTC()).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count implements for repo %s: %w", repo, err)
	}
	return n, nil
}

// CheckImplementCircuitBreaker returns (tripped, reason, err). It guards the
// breadth dimension of autonomous mode: how many development runs a repo may
// start per hour. PerRepoHr == 0 disables the axis.
func (s *Store) CheckImplementCircuitBreaker(repo string, cfg IssueCircuitBreakerLimits) (bool, string, error) {
	if cfg.PerRepoHr <= 0 || repo == "" {
		return false, "", nil
	}
	n, err := s.CountImplementsForRepo(repo, time.Now().Add(-1*time.Hour))
	if err != nil {
		return false, "", err
	}
	if n >= cfg.PerRepoHr {
		return true, fmt.Sprintf("per-repo implement cap reached: %d development runs on %s in last 1h (cap %d)", n, repo, cfg.PerRepoHr), nil
	}
	return false, "", nil
}
```

> Verify the join column. If `issue_reviews` stores `repo` directly (check the `CREATE TABLE issue_reviews` in `store.go`), drop the JOIN and filter `ir.repo = ?`. Confirm against the schema before running.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd daemon && go test ./internal/store/ -run TestCheckImplementCircuitBreaker_PerRepoHr -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/store/issue_circuitbreaker.go daemon/internal/store/issue_circuitbreaker_impl_test.go
git commit -m "feat(store): add per-repo·hour implement circuit breaker"
```

---

## Task 3: `AutonomousConfig` with global→org→repo resolution

**Files:**
- Create: `daemon/internal/config/autonomous.go`
- Create: `daemon/internal/config/autonomous_test.go`
- Modify: `daemon/internal/config/config.go` (add `Autonomous` field to `Config`; org/repo override pointers; default fill)

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/config/autonomous_test.go`:

```go
package config

import "testing"

func TestAutonomousForRepo_Precedence(t *testing.T) {
	on := true
	c := &Config{
		Autonomous: AutonomousConfig{
			Enabled:         false,
			AutoMerge:       false,
			MergeMethod:     "squash",
			TakeOthersTasks: true,
			ReassignOnTake:  true,
			DevMaxTurns:     0,
			DevEffort:       "high",
			DevTimeout:      "45m",
		},
		AutonomousOrgs:  map[string]AutonomousOverride{"acme": {Enabled: &on}},
		AutonomousRepos: map[string]AutonomousOverride{"acme/widget": {Enabled: &on}},
	}

	if got := c.AutonomousForRepo("acme/widget"); !got.Enabled {
		t.Errorf("repo override should enable autonomous, got disabled")
	}
	if got := c.AutonomousForRepo("acme/other"); !got.Enabled {
		t.Errorf("org override should enable autonomous, got disabled")
	}
	if got := c.AutonomousForRepo("none/none"); got.Enabled {
		t.Errorf("unknown repo should inherit global (disabled)")
	}
	// Scalar defaults always inherited from global.
	if got := c.AutonomousForRepo("acme/widget"); got.MergeMethod != "squash" || got.DevEffort != "high" {
		t.Errorf("scalars should inherit global: %+v", got)
	}
}

func TestAutonomousDefaults(t *testing.T) {
	c := &Config{}
	c.applyAutonomousDefaults()
	if c.Autonomous.MergeMethod != "squash" {
		t.Errorf("default merge_method want squash, got %q", c.Autonomous.MergeMethod)
	}
	if c.Autonomous.DevEffort != "high" {
		t.Errorf("default dev_effort want high, got %q", c.Autonomous.DevEffort)
	}
	if c.Autonomous.DevTimeout != "45m" {
		t.Errorf("default dev_timeout want 45m, got %q", c.Autonomous.DevTimeout)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/config/ -run 'TestAutonomous' -race`
Expected: FAIL — `Config has no field Autonomous`, `AutonomousConfig undefined`, etc.

- [ ] **Step 3: Create `autonomous.go`**

```go
package config

// AutonomousConfig configures the fully-unattended end-to-end mode. It is
// resolved per repo via AutonomousForRepo (repo > org > global). Safety is
// delegated to the circuit-breaker family (see CircuitBreakerForRepo); there
// is intentionally no per-day task cap.
type AutonomousConfig struct {
	Enabled         bool   `toml:"enabled"`           // master switch / kill-switch
	AutoMerge       bool   `toml:"auto_merge"`        // merge gate; built but OFF by default
	MergeMethod     string `toml:"merge_method"`      // squash|merge|rebase (used only when AutoMerge)
	TakeOthersTasks bool   `toml:"take_others_tasks"` // enable cascade bucket 3
	ReassignOnTake  bool   `toml:"reassign_on_take"`  // add bot as assignee on others' tasks
	DevMaxTurns     int    `toml:"dev_max_turns"`     // 0 = no practical cap for development
	DevEffort       string `toml:"dev_effort"`        // agent effort for development
	DevTimeout      string `toml:"dev_timeout"`       // generous development timeout (e.g. "45m")
}

// AutonomousOverride is the per-org / per-repo override shape. Pointer fields
// are nil when unset (inherit); set fields replace the inherited value.
type AutonomousOverride struct {
	Enabled         *bool  `toml:"enabled,omitempty"`
	AutoMerge       *bool  `toml:"auto_merge,omitempty"`
	MergeMethod     string `toml:"merge_method,omitempty"`
	TakeOthersTasks *bool  `toml:"take_others_tasks,omitempty"`
	ReassignOnTake  *bool  `toml:"reassign_on_take,omitempty"`
	DevMaxTurns     *int   `toml:"dev_max_turns,omitempty"`
	DevEffort       string `toml:"dev_effort,omitempty"`
	DevTimeout      string `toml:"dev_timeout,omitempty"`
}

// AutonomousForRepo resolves autonomous config for a repo: repo > org > global.
func (c *Config) AutonomousForRepo(repo string) AutonomousConfig {
	out := c.Autonomous
	if org := repoOrg(repo); org != "" && c.AutonomousOrgs != nil {
		if o, ok := c.AutonomousOrgs[org]; ok {
			applyAutonomousOverride(&out, o)
		}
	}
	if c.AutonomousRepos != nil {
		if r, ok := c.AutonomousRepos[repo]; ok {
			applyAutonomousOverride(&out, r)
		}
	}
	return out
}

func applyAutonomousOverride(out *AutonomousConfig, o AutonomousOverride) {
	if o.Enabled != nil {
		out.Enabled = *o.Enabled
	}
	if o.AutoMerge != nil {
		out.AutoMerge = *o.AutoMerge
	}
	if o.MergeMethod != "" {
		out.MergeMethod = o.MergeMethod
	}
	if o.TakeOthersTasks != nil {
		out.TakeOthersTasks = *o.TakeOthersTasks
	}
	if o.ReassignOnTake != nil {
		out.ReassignOnTake = *o.ReassignOnTake
	}
	if o.DevMaxTurns != nil {
		out.DevMaxTurns = *o.DevMaxTurns
	}
	if o.DevEffort != "" {
		out.DevEffort = o.DevEffort
	}
	if o.DevTimeout != "" {
		out.DevTimeout = o.DevTimeout
	}
}

// applyAutonomousDefaults fills zero-value scalars with safe defaults.
func (c *Config) applyAutonomousDefaults() {
	if c.Autonomous.MergeMethod == "" {
		c.Autonomous.MergeMethod = "squash"
	}
	if c.Autonomous.DevEffort == "" {
		c.Autonomous.DevEffort = "high"
	}
	if c.Autonomous.DevTimeout == "" {
		c.Autonomous.DevTimeout = "45m"
	}
}
```

- [ ] **Step 4: Wire the `Config` fields and default call**

In `config.go`, add to the `Config` struct (after `CircuitBreaker`):

```go
	Autonomous      AutonomousConfig              `toml:"autonomous"`
	AutonomousOrgs  map[string]AutonomousOverride `toml:"autonomous.orgs"`
	AutonomousRepos map[string]AutonomousOverride `toml:"autonomous.repos"`
```

> TOML maps for `[autonomous.orgs."org"]` decode into `AutonomousOrgs`. Verify the tag style against how `[ai.orgs."org"]` is declared (`Orgs map[string]OrgAI \`toml:"orgs"\`` nested under `AIConfig`). If the codebase nests org/repo maps inside their parent struct, instead add `Orgs map[string]AutonomousOverride \`toml:"orgs"\`` and `Repos ... \`toml:"repos"\`` **inside** `AutonomousConfig` and adjust `AutonomousForRepo` to read `c.Autonomous.Orgs`/`c.Autonomous.Repos`. Pick the form that matches the existing `[ai.orgs]` decoding and update the test accordingly.

In `applyDefaults` (end of the function), add:

```go
	c.applyAutonomousDefaults()
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd daemon && go test ./internal/config/ -run 'TestAutonomous' -race`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add daemon/internal/config/autonomous.go daemon/internal/config/autonomous_test.go daemon/internal/config/config.go
git commit -m "feat(config): add [autonomous] config with global->org->repo resolution"
```

---

## Task 4: Store — `claimed_by_autonomous` column + candidates query

**Files:**
- Modify: `daemon/internal/store/store.go` (idempotent migration)
- Modify: `daemon/internal/store/issues.go` (setter + list query)
- Test: `daemon/internal/store/autonomous_issues_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/store/autonomous_issues_test.go`:

```go
package store

import "testing"

func TestSetIssueClaimedByAutonomous(t *testing.T) {
	s := newTestStore(t)
	id := seedIssue(t, s, "acme/widget", 7, "open") // existing seed helper

	if err := s.SetIssueClaimedByAutonomous(id, true); err != nil {
		t.Fatalf("set claimed: %v", err)
	}
	got, err := s.IsIssueClaimedByAutonomous(id)
	if err != nil {
		t.Fatalf("get claimed: %v", err)
	}
	if !got {
		t.Errorf("want claimed=true after set")
	}
}
```

> If `seedIssue` does not exist, add a minimal helper inserting one `issues` row and returning its autoincrement id; model it on existing seed helpers in the package.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/store/ -run TestSetIssueClaimedByAutonomous -race`
Expected: FAIL — `s.SetIssueClaimedByAutonomous undefined`.

- [ ] **Step 3: Add the idempotent migration**

In `store.go`, in the migration block where other `ALTER TABLE ... ADD COLUMN` statements live (the file already documents an "explicit migration block" for the `prs` review-state columns), add an idempotent add for `issues`:

```go
	// Autonomous mode (#<spec 2026-06-12>): mark issues claimed by the
	// unattended end-to-end pipeline. Idempotent for existing DBs.
	addColumnIfMissing(db, "issues", "claimed_by_autonomous", "INTEGER NOT NULL DEFAULT 0")
```

> Reuse the existing helper the file uses for idempotent column adds. If the file does the add inline with a guarded `ALTER TABLE`, follow that exact idiom instead of inventing `addColumnIfMissing`. Check how the `prs.external_review_state` migration is written and copy it verbatim in shape.

- [ ] **Step 4: Add setter + getter in `issues.go`**

```go
// SetIssueClaimedByAutonomous flags (or clears) an issue as claimed by the
// autonomous end-to-end pipeline. Used for auditing and to keep the selector
// from re-picking an in-flight task.
func (s *Store) SetIssueClaimedByAutonomous(issueID int64, claimed bool) error {
	v := 0
	if claimed {
		v = 1
	}
	if _, err := s.db.Exec(`UPDATE issues SET claimed_by_autonomous = ? WHERE id = ?`, v, issueID); err != nil {
		return fmt.Errorf("store: set issue claimed_by_autonomous: %w", err)
	}
	return nil
}

// IsIssueClaimedByAutonomous reports whether the issue is flagged claimed.
func (s *Store) IsIssueClaimedByAutonomous(issueID int64) (bool, error) {
	var v int
	if err := s.db.QueryRow(`SELECT claimed_by_autonomous FROM issues WHERE id = ?`, issueID).Scan(&v); err != nil {
		return false, fmt.Errorf("store: get issue claimed_by_autonomous: %w", err)
	}
	return v != 0, nil
}

// HasOpenAutoImplementPR reports whether an open PR is linked to the issue's
// GitHub id via auto_implement_issue_id. Used by the selector's "not started"
// predicate.
func (s *Store) HasOpenAutoImplementPR(issueGithubID int64) (bool, error) {
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM prs WHERE auto_implement_issue_id = ? AND state = 'open'`,
		issueGithubID,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("store: has open auto_implement PR: %w", err)
	}
	return n > 0, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd daemon && go test ./internal/store/ -run TestSetIssueClaimedByAutonomous -race`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add daemon/internal/store/store.go daemon/internal/store/issues.go daemon/internal/store/autonomous_issues_test.go
git commit -m "feat(store): track claimed_by_autonomous + open-PR-for-issue lookup"
```

---

## Task 5: GitHub client — `AddAssignees`, `BranchExists`, `MergePR`

**Files:**
- Modify: `daemon/internal/github/repos.go` (AddAssignees, BranchExists)
- Modify: `daemon/internal/github/client.go` (MergePR)
- Test: `daemon/internal/github/autonomous_client_test.go` (create)

These follow the existing `do`/`doWithBody` + status-check idiom shown in `AddLabels`/`GetIssue`.

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/github/autonomous_client_test.go`:

```go
package github

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(srv *httptest.Server) *Client {
	// Mirror the constructor used by other tests in this package. If the
	// existing tests build the client differently (e.g. NewClient(token,
	// opts...)), copy that exact construction and point baseURL at srv.URL.
	c := &Client{token: "t", baseURL: srv.URL, http: srv.Client()}
	return c
}

func TestAddAssignees(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/widget/issues/7/assignees" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if err := newTestClient(srv).AddAssignees("acme/widget", 7, []string{"bot"}); err != nil {
		t.Fatalf("AddAssignees: %v", err)
	}
}

func TestBranchExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/widget/branches/exists" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"exists"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Branch not found"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	ok, err := c.BranchExists("acme/widget", "exists")
	if err != nil || !ok {
		t.Errorf("exists: want ok,nil got %v,%v", ok, err)
	}
	ok, err = c.BranchExists("acme/widget", "missing")
	if err != nil || ok {
		t.Errorf("missing: want false,nil got %v,%v", ok, err)
	}
}
```

> Confirm the `Client` struct field names (`token`, `baseURL`, `http`) against `client.go`. The extracted `do(...)` and `SubmitReview` show requests built as `c.baseURL+path` with `Authorization: Bearer `+c.token`; match whatever the real fields are named.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/github/ -run 'TestAddAssignees|TestBranchExists' -race`
Expected: FAIL — `c.AddAssignees undefined`, `c.BranchExists undefined`.

- [ ] **Step 3: Implement `AddAssignees` and `BranchExists` in `repos.go`**

```go
// AddAssignees adds GitHub logins as assignees on an issue/PR without removing
// existing assignees (POST .../assignees is additive). Used by the autonomous
// selector to claim another user's task while keeping the original assignee.
func (c *Client) AddAssignees(repo string, number int, assignees []string) error {
	if repo == "" || number == 0 || len(assignees) == 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"assignees": assignees})
	if err != nil {
		return fmt.Errorf("github: marshal assignees: %w", err)
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/assignees", repo, number)
	resp, err := c.doWithBody("POST", path, "application/vnd.github+json", "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github: add assignees: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: add assignees %s#%d: status %d: %s", repo, number, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	return nil
}

// BranchExists reports whether a branch exists on the remote. 404 => false.
func (c *Client) BranchExists(repo, branch string) (bool, error) {
	path := fmt.Sprintf("/repos/%s/branches/%s", repo, url.PathEscape(branch))
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return false, fmt.Errorf("github: get branch %s#%s: %w", repo, branch, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("github: get branch %s#%s: status %d: %s", repo, branch, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
}
```

- [ ] **Step 4: Implement `MergePR` in `client.go`**

```go
// MergePR merges a pull request using the given method ("squash"|"merge"|
// "rebase"). Returns the merge error verbatim so the caller can surface a
// non-mergeable (405) state. Built for the autonomous merge gate, which is
// disabled by default — this only runs when AutoMerge is explicitly enabled.
func (c *Client) MergePR(repo string, number int, method string) error {
	if method == "" {
		method = "squash"
	}
	payload, err := json.Marshal(map[string]any{"merge_method": method})
	if err != nil {
		return fmt.Errorf("github: marshal merge: %w", err)
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number)
	resp, err := c.doWithBody("PUT", path, "application/vnd.github+json", "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github: merge PR %s#%d: %w", repo, number, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: merge PR %s#%d: status %d: %s", repo, number, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	return nil
}
```

> Ensure `bytes`, `net/url`, `io`, `net/http` are imported in the edited files (they are already used by neighbouring methods).

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/github/ -run 'TestAddAssignees|TestBranchExists' -race`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add daemon/internal/github/repos.go daemon/internal/github/client.go daemon/internal/github/autonomous_client_test.go
git commit -m "feat(github): add AddAssignees, BranchExists, MergePR"
```

---

## Task 6: Selector — "not started" predicate

**Files:**
- Create: `daemon/internal/autonomous/selector.go` (interfaces + `notStarted`)
- Create: `daemon/internal/autonomous/selector_test.go`

We define small local interfaces so the package is testable with fakes.

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/autonomous/selector_test.go`:

```go
package autonomous

import (
	"context"
	"testing"
)

type fakeStore struct {
	openPR map[int64]bool
}

func (f *fakeStore) HasOpenAutoImplementPR(githubID int64) (bool, error) {
	return f.openPR[githubID], nil
}
func (f *fakeStore) SetIssueClaimedByAutonomous(int64, bool) error { return nil }

type fakeGH struct {
	branches map[string]bool
}

func (f *fakeGH) BranchExists(repo, branch string) (bool, error) { return f.branches[branch], nil }
func (f *fakeGH) AddAssignees(string, int, []string) error       { return nil }
func (f *fakeGH) PostComment(string, int, string) error          { return nil }

func TestNotStarted(t *testing.T) {
	s := &Selector{
		store:        &fakeStore{openPR: map[int64]bool{200: true}},
		gh:           &fakeGH{branches: map[string]bool{"heimdallm/issue-9": true}},
		branchPrefix: "heimdallm/issue-",
	}

	// Has open linked PR -> started.
	c1 := Candidate{Repo: "a/b", Number: 1, GithubID: 200}
	if started, _ := s.notStarted(context.Background(), c1); started {
		t.Errorf("issue with open linked PR must be 'started'")
	}

	// Has remote branch -> started.
	c2 := Candidate{Repo: "a/b", Number: 9, GithubID: 201}
	if started, _ := s.notStarted(context.Background(), c2); started {
		t.Errorf("issue with remote branch must be 'started'")
	}

	// Neither -> not started.
	c3 := Candidate{Repo: "a/b", Number: 3, GithubID: 202}
	if started, _ := s.notStarted(context.Background(), c3); !started {
		t.Errorf("issue with no PR and no branch must be 'not started'")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/autonomous/ -run TestNotStarted -race`
Expected: FAIL — package/types undefined.

- [ ] **Step 3: Create `selector.go` with interfaces, `Candidate`, and `notStarted`**

```go
// Package autonomous implements Heimdallm's fully-unattended end-to-end mode:
// it selects an issue (assigned-to-bot > unassigned > others), drives it
// through the existing triage/refinement/development pipeline single-flight,
// and lets the existing Tier 3 review loop react to reviews.
package autonomous

import (
	"context"
	"fmt"
)

// SelectorStore is the persistence surface the selector needs.
type SelectorStore interface {
	HasOpenAutoImplementPR(issueGithubID int64) (bool, error)
	SetIssueClaimedByAutonomous(issueID int64, claimed bool) error
}

// SelectorGH is the GitHub surface the selector needs.
type SelectorGH interface {
	BranchExists(repo, branch string) (bool, error)
	AddAssignees(repo string, number int, assignees []string) error
	PostComment(repo string, number int, body string) error
}

// Candidate is one issue the selector may pick.
type Candidate struct {
	Repo      string
	Number    int
	GithubID  int64
	StoreID   int64
	Assignees []string
	Labels    []string
	Title     string
	Body      string
}

// Selector picks the next issue to drive autonomously.
type Selector struct {
	store        SelectorStore
	gh           SelectorGH
	branchPrefix string // e.g. the prefix gitops uses for issue branches
}

// notStarted reports whether a candidate has no open linked PR and no remote
// branch referencing it. Both checks are conservative: when in doubt the
// selector treats the task as started and skips it.
func (s *Selector) notStarted(ctx context.Context, c Candidate) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	hasPR, err := s.store.HasOpenAutoImplementPR(c.GithubID)
	if err != nil {
		return false, fmt.Errorf("autonomous: not-started PR check: %w", err)
	}
	if hasPR {
		return false, nil
	}
	branch := fmt.Sprintf("%s%d", s.branchPrefix, c.Number)
	hasBranch, err := s.gh.BranchExists(c.Repo, branch)
	if err != nil {
		return false, fmt.Errorf("autonomous: not-started branch check: %w", err)
	}
	return !hasBranch, nil
}
```

> Confirm the issue-branch naming used by `issues/gitops.go` (`CheckoutNewBranch`) and set `branchPrefix` accordingly when constructing the `Selector` (Task 14). If the branch name embeds the slugged title rather than just the number, change `notStarted` to call `BranchExists` for that exact pattern, or list branches by prefix. Keep the test in sync with the chosen pattern.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd daemon && go test ./internal/autonomous/ -run TestNotStarted -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/autonomous/selector.go daemon/internal/autonomous/selector_test.go
git commit -m "feat(autonomous): selector not-started predicate"
```

---

## Task 7: Selector — cascade selection

**Files:**
- Modify: `daemon/internal/autonomous/selector.go` (add `Pick`)
- Modify: `daemon/internal/autonomous/selector_test.go` (add cascade test)

- [ ] **Step 1: Write the failing test**

Add to `selector_test.go`:

```go
func TestPick_CascadeOrder(t *testing.T) {
	s := &Selector{
		store:        &fakeStore{openPR: map[int64]bool{}},
		gh:           &fakeGH{branches: map[string]bool{}},
		branchPrefix: "heimdallm/issue-",
		botLogin:     "bot",
		skipLabels:   []string{"blocked", "wontfix"},
		takeOthers:   true,
	}

	cands := []Candidate{
		{Repo: "a/b", Number: 1, GithubID: 1, Assignees: []string{"alice"}},          // others
		{Repo: "a/b", Number: 2, GithubID: 2, Assignees: nil},                        // unassigned
		{Repo: "a/b", Number: 3, GithubID: 3, Assignees: []string{"bot"}},            // bot
		{Repo: "a/b", Number: 4, GithubID: 4, Assignees: []string{"bot"}, Labels: []string{"blocked"}}, // skip
	}

	got, bucket, err := s.Pick(context.Background(), cands)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got == nil || got.Number != 3 {
		t.Fatalf("want bot-assigned issue #3 first, got %+v", got)
	}
	if bucket != BucketBotAssigned {
		t.Errorf("want BucketBotAssigned, got %v", bucket)
	}

	// Remove bot-assigned -> unassigned wins next.
	got, bucket, _ = s.Pick(context.Background(), cands[:2])
	if got == nil || got.Number != 2 || bucket != BucketUnassigned {
		t.Fatalf("want unassigned #2, got %+v bucket %v", got, bucket)
	}

	// Only others remain -> others bucket.
	got, bucket, _ = s.Pick(context.Background(), cands[:1])
	if got == nil || got.Number != 1 || bucket != BucketOthers {
		t.Fatalf("want others #1, got %+v bucket %v", got, bucket)
	}

	// take_others disabled -> nothing.
	s.takeOthers = false
	got, _, _ = s.Pick(context.Background(), cands[:1])
	if got != nil {
		t.Errorf("with takeOthers=false others must be skipped, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/autonomous/ -run TestPick_CascadeOrder -race`
Expected: FAIL — `s.Pick undefined`, `BucketBotAssigned undefined`.

- [ ] **Step 3: Implement buckets + `Pick`**

Add to `selector.go` (extend the `Selector` struct with the new fields and add the methods):

```go
// Bucket identifies which cascade tier a candidate was selected from.
type Bucket int

const (
	BucketNone Bucket = iota
	BucketBotAssigned
	BucketUnassigned
	BucketOthers
)

func (b Bucket) String() string {
	switch b {
	case BucketBotAssigned:
		return "bot_assigned"
	case BucketUnassigned:
		return "unassigned"
	case BucketOthers:
		return "others"
	default:
		return "none"
	}
}
```

Extend the `Selector` struct fields:

```go
type Selector struct {
	store        SelectorStore
	gh           SelectorGH
	branchPrefix string
	botLogin     string
	skipLabels   []string // skip_labels + blocked_labels, lower-cased
	takeOthers   bool
}
```

Add the selection logic:

```go
// Pick scans candidates in cascade order (bot-assigned > unassigned > others),
// skipping anything with a skip/blocked label or already started, and returns
// the first eligible candidate with the bucket it came from. Returns (nil,
// BucketNone, nil) when nothing is eligible.
func (s *Selector) Pick(ctx context.Context, cands []Candidate) (*Candidate, Bucket, error) {
	for _, bucket := range []Bucket{BucketBotAssigned, BucketUnassigned, BucketOthers} {
		if bucket == BucketOthers && !s.takeOthers {
			continue
		}
		for i := range cands {
			c := cands[i]
			if s.hasSkipLabel(c) || s.bucketOf(c) != bucket {
				continue
			}
			started, err := s.notStarted(ctx, c)
			if err != nil {
				return nil, BucketNone, err
			}
			if !started {
				continue
			}
			return &c, bucket, nil
		}
	}
	return nil, BucketNone, nil
}

func (s *Selector) bucketOf(c Candidate) Bucket {
	for _, a := range c.Assignees {
		if a == s.botLogin {
			return BucketBotAssigned
		}
	}
	if len(c.Assignees) == 0 {
		return BucketUnassigned
	}
	return BucketOthers
}

func (s *Selector) hasSkipLabel(c Candidate) bool {
	for _, l := range c.Labels {
		for _, skip := range s.skipLabels {
			if equalFold(l, skip) {
				return true
			}
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
```

> `notStarted` reads `started` as its return semantics: it returns `true` when the task is NOT started. In `Pick` the variable is named `started` but holds "not started"; rename for clarity if preferred (e.g. `eligible`). Keep test expectations aligned.

Fix the naming now to avoid the trap — rename `notStarted`'s return to read naturally:

```go
// isEligible reports whether the candidate is unstarted (no open linked PR
// and no remote branch). Renamed from notStarted to avoid double negatives.
func (s *Selector) isEligible(ctx context.Context, c Candidate) (bool, error) { /* body of notStarted */ }
```

And in `Pick` use `eligible, err := s.isEligible(ctx, c)` / `if !eligible { continue }`. Update Task 6's test to call `isEligible` (rename `TestNotStarted` body accordingly) so names are consistent across tasks.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/autonomous/ -run 'TestPick_CascadeOrder|TestNotStarted|TestIsEligible' -race`
Expected: PASS (rename the Task 6 test to `TestIsEligible` calling `s.isEligible`).

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/autonomous/selector.go daemon/internal/autonomous/selector_test.go
git commit -m "feat(autonomous): cascade selection (bot>unassigned>others) with skip-label guard"
```

---

## Task 8: Selector — reassign + agent-generated coordination comment

**Files:**
- Modify: `daemon/internal/autonomous/selector.go` (add `Claim`)
- Modify: `daemon/internal/autonomous/selector_test.go`

- [ ] **Step 1: Write the failing test**

Add to `selector_test.go`:

```go
type recordingGH struct {
	fakeGH
	assigned  []string
	commented string
}

func (r *recordingGH) AddAssignees(repo string, n int, a []string) error {
	r.assigned = append(r.assigned, a...)
	return nil
}
func (r *recordingGH) PostComment(repo string, n int, body string) error {
	r.commented = body
	return nil
}

type fakeCommentGen struct{ out string }

func (f *fakeCommentGen) GenerateCoordinationComment(_ context.Context, _ Candidate) (string, error) {
	return f.out, nil
}

func TestClaim_OthersReassignsAndComments(t *testing.T) {
	gh := &recordingGH{}
	s := &Selector{
		store:      &fakeStore{},
		gh:         gh,
		botLogin:   "bot",
		reassign:   true,
		commentGen: &fakeCommentGen{out: "I'll take this one — picking it up now."},
	}
	c := Candidate{Repo: "a/b", Number: 5, GithubID: 5, StoreID: 50, Assignees: []string{"alice"}}

	if err := s.Claim(context.Background(), c, BucketOthers); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(gh.assigned) != 1 || gh.assigned[0] != "bot" {
		t.Errorf("want bot added as assignee, got %v", gh.assigned)
	}
	if gh.commented == "" {
		t.Errorf("want a coordination comment posted")
	}
}

func TestClaim_BotAssignedSkipsReassign(t *testing.T) {
	gh := &recordingGH{}
	s := &Selector{store: &fakeStore{}, gh: gh, botLogin: "bot", reassign: true,
		commentGen: &fakeCommentGen{out: "x"}}
	c := Candidate{Repo: "a/b", Number: 6, GithubID: 6, StoreID: 60, Assignees: []string{"bot"}}

	if err := s.Claim(context.Background(), c, BucketBotAssigned); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(gh.assigned) != 0 {
		t.Errorf("bot-assigned bucket must not reassign, got %v", gh.assigned)
	}
	if gh.commented != "" {
		t.Errorf("bot-assigned bucket must not post coordination comment")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/autonomous/ -run 'TestClaim' -race`
Expected: FAIL — `s.Claim undefined`, `CommentGenerator`/`commentGen` undefined.

- [ ] **Step 3: Implement `CommentGenerator` + `Claim`**

Add to `selector.go`:

```go
// CommentGenerator produces the coordination comment via the agent. The
// implementation wraps the executor (review-only mode, no workdir), mirroring
// the prReviewExecutor.GenerateReviewResponse pattern, and MUST fence the
// untrusted issue body with sanitiseUntrustedFreeText before prompting.
type CommentGenerator interface {
	GenerateCoordinationComment(ctx context.Context, c Candidate) (string, error)
}
```

Extend the `Selector` struct:

```go
	reassign   bool
	commentGen CommentGenerator
```

Add the method:

```go
// Claim marks the candidate as taken. For the "others" bucket it performs the
// courtesy step: add the bot as an assignee (keeping the original) and post an
// agent-generated coordination comment. For bot-assigned / unassigned buckets
// it only flags the store. Idempotent enough for retries: AddAssignees is
// additive and a duplicate comment is acceptable but avoided by only running
// on first claim.
func (s *Selector) Claim(ctx context.Context, c Candidate, bucket Bucket) error {
	if c.StoreID != 0 {
		if err := s.store.SetIssueClaimedByAutonomous(c.StoreID, true); err != nil {
			return fmt.Errorf("autonomous: flag claimed: %w", err)
		}
	}
	if bucket != BucketOthers {
		return nil
	}
	if s.reassign {
		if err := s.gh.AddAssignees(c.Repo, c.Number, []string{s.botLogin}); err != nil {
			return fmt.Errorf("autonomous: reassign: %w", err)
		}
	}
	if s.commentGen != nil {
		body, err := s.commentGen.GenerateCoordinationComment(ctx, c)
		if err != nil {
			return fmt.Errorf("autonomous: generate coordination comment: %w", err)
		}
		if body != "" {
			if err := s.gh.PostComment(c.Repo, c.Number, body); err != nil {
				return fmt.Errorf("autonomous: post coordination comment: %w", err)
			}
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/autonomous/ -run 'TestClaim' -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/autonomous/selector.go daemon/internal/autonomous/selector_test.go
git commit -m "feat(autonomous): courtesy reassign + agent-generated coordination comment"
```

---

## Task 9: Review classification — approved-with-issues

**Files:**
- Create: `daemon/internal/autonomous/review_class.go`
- Create: `daemon/internal/autonomous/review_class_test.go`

GitHub `GetPRReviews` returns `[]PRReview` (each with `State`: `APPROVED`, `CHANGES_REQUESTED`, `COMMENTED`, and a `Body`). We compute the aggregate decision the autonomous review-loop acts on.

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/autonomous/review_class_test.go`:

```go
package autonomous

import "testing"

func TestClassifyReview(t *testing.T) {
	cases := []struct {
		name       string
		reviews    []ReviewInput
		want       ReviewDecision
	}{
		{"changes requested", []ReviewInput{{State: "CHANGES_REQUESTED"}}, DecisionFix},
		{"commented only", []ReviewInput{{State: "COMMENTED", Body: "nit: rename"}}, DecisionFix},
		{"approved clean", []ReviewInput{{State: "APPROVED", Body: "LGTM"}}, DecisionMergeGate},
		{"approved with unresolved comments", []ReviewInput{{State: "APPROVED", Body: "LGTM", UnresolvedComments: 2}}, DecisionFix},
		{"approved with actionable body", []ReviewInput{{State: "APPROVED", Body: "please rename foo before merge"}}, DecisionFix},
		{"no human reviews", []ReviewInput{}, DecisionWait},
		{"latest approved supersedes older changes", []ReviewInput{{State: "CHANGES_REQUESTED"}, {State: "APPROVED", Body: "LGTM"}}, DecisionMergeGate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyReview(tc.reviews); got != tc.want {
				t.Errorf("ClassifyReview = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/autonomous/ -run TestClassifyReview -race`
Expected: FAIL — types/functions undefined.

- [ ] **Step 3: Implement the classifier**

Create `review_class.go`:

```go
package autonomous

import "strings"

// ReviewInput is the minimal projection of a PR review the classifier needs.
type ReviewInput struct {
	State              string // APPROVED | CHANGES_REQUESTED | COMMENTED
	Body               string
	UnresolvedComments int // count of unresolved inline review comment threads
}

// ReviewDecision is what the autonomous review-loop should do next.
type ReviewDecision int

const (
	DecisionWait      ReviewDecision = iota // no human review yet — keep watching
	DecisionFix                             // run FixRunner
	DecisionMergeGate                       // approved clean — hand to merge gate
)

func (d ReviewDecision) String() string {
	switch d {
	case DecisionFix:
		return "fix"
	case DecisionMergeGate:
		return "merge_gate"
	default:
		return "wait"
	}
}

// actionableHints are phrases that indicate an "approved" review still asks
// for changes ("approve with issues"). Conservative: matching any one routes
// to a fix rather than merge.
var actionableHints = []string{
	"please ", "before merge", "should change", "needs ", "fix ", "rename ",
	"remove ", "address ", "todo", "must ",
}

// ClassifyReview reduces the review list to a single decision. The latest
// review of each kind dominates; an APPROVED with unresolved inline comments
// or an actionable body is treated as "approved with issues" and routed to a
// fix instead of the merge gate.
func ClassifyReview(reviews []ReviewInput) ReviewDecision {
	if len(reviews) == 0 {
		return DecisionWait
	}
	latest := reviews[len(reviews)-1]
	switch strings.ToUpper(latest.State) {
	case "CHANGES_REQUESTED", "COMMENTED":
		return DecisionFix
	case "APPROVED":
		if latest.UnresolvedComments > 0 || hasActionableBody(latest.Body) {
			return DecisionFix
		}
		return DecisionMergeGate
	default:
		return DecisionWait
	}
}

func hasActionableBody(body string) bool {
	b := strings.ToLower(body)
	for _, h := range actionableHints {
		if strings.Contains(b, h) {
			return true
		}
	}
	return false
}
```

> The "latest review" assumption requires `GetPRReviews` to return reviews in chronological order (GitHub does). When wiring (Task 14), build `[]ReviewInput` from `github.PRReview` preserving API order and, if available, only counting the latest review per reviewer. Keep the classifier pure and unit-tested as above.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd daemon && go test ./internal/autonomous/ -run TestClassifyReview -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/autonomous/review_class.go daemon/internal/autonomous/review_class_test.go
git commit -m "feat(autonomous): classify review into fix/merge-gate/wait (approved-with-issues)"
```

---

## Task 10: Merge gate (built, default OFF)

**Files:**
- Modify: `daemon/internal/autonomous/review_class.go` (add `MergeGate`) — or create `merge_gate.go`
- Create: `daemon/internal/autonomous/merge_gate_test.go`

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/autonomous/merge_gate_test.go`:

```go
package autonomous

import (
	"context"
	"errors"
	"testing"
)

type fakeMerger struct {
	called bool
	method string
}

func (f *fakeMerger) MergePR(repo string, number int, method string) error {
	f.called = true
	f.method = method
	return nil
}

func TestMergeGate_DisabledSkips(t *testing.T) {
	m := &fakeMerger{}
	g := &MergeGate{merger: m, enabled: false, method: "squash"}
	res, err := g.Run(context.Background(), "a/b", 7)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.called {
		t.Errorf("merge must NOT be called when disabled")
	}
	if res != MergeSkippedDisabled {
		t.Errorf("want MergeSkippedDisabled, got %v", res)
	}
}

func TestMergeGate_EnabledMerges(t *testing.T) {
	m := &fakeMerger{}
	g := &MergeGate{merger: m, enabled: true, method: "squash"}
	res, err := g.Run(context.Background(), "a/b", 7)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !m.called || m.method != "squash" {
		t.Errorf("want squash merge call, got called=%v method=%q", m.called, m.method)
	}
	if res != MergeDone {
		t.Errorf("want MergeDone, got %v", res)
	}
}

func TestMergeGate_PropagatesError(t *testing.T) {
	g := &MergeGate{merger: errMerger{}, enabled: true, method: "squash"}
	if _, err := g.Run(context.Background(), "a/b", 7); err == nil {
		t.Errorf("want error propagated from merger")
	}
}

type errMerger struct{}

func (errMerger) MergePR(string, int, string) error { return errors.New("405 not mergeable") }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/autonomous/ -run TestMergeGate -race`
Expected: FAIL — `MergeGate`/`Merger` undefined.

- [ ] **Step 3: Implement the merge gate**

Create `daemon/internal/autonomous/merge_gate.go`:

```go
package autonomous

import (
	"context"
	"fmt"
)

// Merger merges a PR. Backed by github.Client.MergePR in production.
type Merger interface {
	MergePR(repo string, number int, method string) error
}

// MergeResult records what the gate did, for SSE/audit.
type MergeResult int

const (
	MergeSkippedDisabled MergeResult = iota // auto_merge=false (the default)
	MergeDone
)

func (r MergeResult) String() string {
	if r == MergeDone {
		return "merged"
	}
	return "skipped_disabled"
}

// MergeGate performs the final merge when, and only when, auto_merge is
// enabled for the repo. With the default (disabled) it is a safe no-op that
// reports MergeSkippedDisabled so the caller can emit an audit event.
type MergeGate struct {
	merger  Merger
	enabled bool
	method  string
}

// NewMergeGate builds a gate from resolved autonomous config.
func NewMergeGate(merger Merger, enabled bool, method string) *MergeGate {
	if method == "" {
		method = "squash"
	}
	return &MergeGate{merger: merger, enabled: enabled, method: method}
}

// Run merges the PR if enabled; otherwise returns MergeSkippedDisabled.
func (g *MergeGate) Run(ctx context.Context, repo string, number int) (MergeResult, error) {
	if err := ctx.Err(); err != nil {
		return MergeSkippedDisabled, err
	}
	if !g.enabled {
		return MergeSkippedDisabled, nil
	}
	if err := g.merger.MergePR(repo, number, g.method); err != nil {
		return MergeSkippedDisabled, fmt.Errorf("autonomous: merge gate: %w", err)
	}
	return MergeDone, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/autonomous/ -run TestMergeGate -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/autonomous/merge_gate.go daemon/internal/autonomous/merge_gate_test.go
git commit -m "feat(autonomous): merge gate (built, disabled by default)"
```

---

## Task 11: Single-flight phase guard

**Files:**
- Create: `daemon/internal/autonomous/singleflight.go`
- Create: `daemon/internal/autonomous/singleflight_test.go`

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/autonomous/singleflight_test.go`:

```go
package autonomous

import (
	"sync"
	"testing"
)

func TestPhaseGuard_OneAtATime(t *testing.T) {
	g := NewPhaseGuard()

	rel, ok := g.TryEnter("development")
	if !ok {
		t.Fatalf("first enter should succeed")
	}
	if _, ok := g.TryEnter("development"); ok {
		t.Errorf("second enter of busy phase must fail")
	}
	// A different phase is independent.
	if _, ok := g.TryEnter("triage"); !ok {
		t.Errorf("independent phase should enter")
	}
	rel()
	if _, ok := g.TryEnter("development"); !ok {
		t.Errorf("after release, phase should be enterable again")
	}
}

func TestPhaseGuard_ConcurrentSafe(t *testing.T) {
	g := NewPhaseGuard()
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rel, ok := g.TryEnter("triage"); ok {
				mu.Lock()
				wins++
				mu.Unlock()
				rel()
			}
		}()
	}
	wg.Wait()
	// Not asserting exact count (releases interleave), only that no race/panic
	// occurred and at least one entered. Run with -race.
	if wins == 0 {
		t.Errorf("expected at least one successful enter")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/autonomous/ -run TestPhaseGuard -race`
Expected: FAIL — `NewPhaseGuard undefined`.

- [ ] **Step 3: Implement the guard**

Create `singleflight.go`:

```go
package autonomous

import "sync"

// PhaseGuard enforces single-flight per pipeline phase: at most one task in
// triage, one in refinement, one in development, and one review-fix running
// at a time. Independent phases never block each other.
type PhaseGuard struct {
	mu   sync.Mutex
	busy map[string]bool
}

// NewPhaseGuard builds an empty guard.
func NewPhaseGuard() *PhaseGuard {
	return &PhaseGuard{busy: make(map[string]bool)}
}

// TryEnter attempts to claim a phase. It returns a release func and true on
// success; false (with a no-op release) when the phase is already busy. The
// release func is safe to call exactly once.
func (g *PhaseGuard) TryEnter(phase string) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy[phase] {
		return func() {}, false
	}
	g.busy[phase] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.busy, phase)
			g.mu.Unlock()
		})
	}, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/autonomous/ -run TestPhaseGuard -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/autonomous/singleflight.go daemon/internal/autonomous/singleflight_test.go
git commit -m "feat(autonomous): single-flight phase guard"
```

---

## Task 12: Orchestrator — stage chaining (self-driven, label gating overridden)

**Files:**
- Create: `daemon/internal/autonomous/orchestrator.go`
- Create: `daemon/internal/autonomous/orchestrator_test.go`

The orchestrator runs one selection→triage→refinement→development tick. It depends on a `StageRunner` interface that the production wiring backs with the existing `issues.Pipeline.Run` (called once per stage with the appropriate options) plus `TransitionIssueStage`.

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/autonomous/orchestrator_test.go`:

```go
package autonomous

import (
	"context"
	"testing"
)

type fakeRunner struct {
	ran    []string // stage names run, in order
	failAt string
}

func (f *fakeRunner) RunStage(_ context.Context, stage string, c Candidate) (StageOutcome, error) {
	f.ran = append(f.ran, stage)
	if stage == f.failAt {
		return StageOutcome{Success: false}, nil
	}
	if stage == "development" {
		return StageOutcome{Success: true, PRNumber: 123}, nil
	}
	return StageOutcome{Success: true}, nil
}

func TestOrchestrator_HappyPathChainsAllStages(t *testing.T) {
	r := &fakeRunner{}
	o := &Orchestrator{runner: r, guard: NewPhaseGuard()}
	c := Candidate{Repo: "a/b", Number: 1, GithubID: 1, StoreID: 10}

	res, err := o.Drive(context.Background(), c)
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	want := []string{"triage", "refinement", "development"}
	if len(r.ran) != 3 || r.ran[0] != want[0] || r.ran[1] != want[1] || r.ran[2] != want[2] {
		t.Fatalf("stage order: want %v, got %v", want, r.ran)
	}
	if res.PRNumber != 123 {
		t.Errorf("want PR 123 surfaced, got %d", res.PRNumber)
	}
}

func TestOrchestrator_StopsOnStageFailure(t *testing.T) {
	r := &fakeRunner{failAt: "refinement"}
	o := &Orchestrator{runner: r, guard: NewPhaseGuard()}
	res, err := o.Drive(context.Background(), Candidate{Repo: "a/b", Number: 2})
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if len(r.ran) != 2 { // triage, refinement (failed) -> stop, no development
		t.Fatalf("want stop after failed refinement, ran %v", r.ran)
	}
	if res.PRNumber != 0 {
		t.Errorf("no PR expected on failed chain")
	}
}

func TestOrchestrator_SingleFlightBlocksBusyPhase(t *testing.T) {
	o := &Orchestrator{runner: &fakeRunner{}, guard: NewPhaseGuard()}
	// Pre-claim triage so Drive cannot start.
	rel, ok := o.guard.TryEnter("triage")
	if !ok {
		t.Fatal("setup: could not pre-claim triage")
	}
	defer rel()
	res, err := o.Drive(context.Background(), Candidate{Repo: "a/b", Number: 3})
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if res.Started {
		t.Errorf("Drive must not start when triage phase is busy")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/autonomous/ -run TestOrchestrator -race`
Expected: FAIL — `Orchestrator`, `StageRunner`, `StageOutcome` undefined.

- [ ] **Step 3: Implement the orchestrator**

Create `orchestrator.go`:

```go
package autonomous

import "context"

// StageOutcome is the result of running one pipeline stage.
type StageOutcome struct {
	Success  bool
	PRNumber int // set by the development stage when a PR is created
}

// StageRunner executes a single pipeline stage for a candidate. The production
// implementation maps stage -> issues.Pipeline.Run with the right RunOptions
// (full agentic ExecOptions for development) and calls TransitionIssueStage to
// advance the GitHub stage labels as an audit trail — without waiting for a
// human, since autonomous mode overrides label gating.
type StageRunner interface {
	RunStage(ctx context.Context, stage string, c Candidate) (StageOutcome, error)
}

// DriveResult summarises one Drive call.
type DriveResult struct {
	Started  bool
	PRNumber int
	LastDone string // last successfully completed stage
}

// stages is the fixed chain. Review is handled asynchronously by Tier 3, not
// by Drive, so it is intentionally absent here.
var stages = []string{"triage", "refinement", "development"}

// Orchestrator drives one issue through the stage chain single-flight.
type Orchestrator struct {
	runner StageRunner
	guard  *PhaseGuard
}

// NewOrchestrator builds an orchestrator.
func NewOrchestrator(runner StageRunner, guard *PhaseGuard) *Orchestrator {
	return &Orchestrator{runner: runner, guard: guard}
}

// Drive runs the candidate through triage->refinement->development. Each stage
// must claim its single-flight slot; if the first stage's slot is busy, Drive
// returns Started=false without doing work. A non-success stage stops the
// chain (the issue stays where it is for the next tick / human inspection).
func (o *Orchestrator) Drive(ctx context.Context, c Candidate) (DriveResult, error) {
	var res DriveResult
	for _, stage := range stages {
		rel, ok := o.guard.TryEnter(stage)
		if !ok {
			// Phase busy. If we have not started anything yet, report not
			// started; otherwise stop the chain cleanly for this tick.
			return res, nil
		}
		res.Started = true
		out, err := o.runner.RunStage(ctx, stage, c)
		rel()
		if err != nil {
			return res, err
		}
		if !out.Success {
			return res, nil
		}
		res.LastDone = stage
		if out.PRNumber != 0 {
			res.PRNumber = out.PRNumber
		}
	}
	return res, nil
}
```

> The production `StageRunner` (built in Task 14) is where label-gating override lives: it calls `issues.Pipeline.Run` with `RunOptions` whose `ExecOpts` carry the full agentic settings for development (`MaxTurns` from `AutonomousConfig.DevMaxTurns`, `Effort` from `DevEffort`, `Timeout` parsed from `DevTimeout`, `PermissionMode: "acceptEdits"`, `WorkDir` = repo checkout), then advances the FSM with `issues.TransitionIssueStage`. Stage names map: `triage`/`refinement`/`development` ↔ `IssueStageTriage`/`IssueStageRefinement`/`IssueStageDevelopment`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/autonomous/ -run TestOrchestrator -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/autonomous/orchestrator.go daemon/internal/autonomous/orchestrator_test.go
git commit -m "feat(autonomous): single-flight stage-chaining orchestrator"
```

---

## Task 13: SSE event constants

**Files:**
- Modify: `daemon/internal/sse/broker.go`
- Test: `daemon/internal/sse/autonomous_events_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/sse/autonomous_events_test.go`:

```go
package sse

import "testing"

func TestAutonomousEventConstants(t *testing.T) {
	pairs := map[string]string{
		EventAutonomousTaskSelected:   "autonomous_task_selected",
		EventAutonomousTaskReassigned: "autonomous_task_reassigned",
		EventAutonomousStageAdvanced:  "autonomous_stage_advanced",
		EventAutonomousReviewClass:    "autonomous_review_classified",
		EventAutonomousMergeSkipped:   "autonomous_merge_skipped",
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("event constant mismatch: got %q want %q", got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/sse/ -run TestAutonomousEventConstants -race`
Expected: FAIL — constants undefined.

- [ ] **Step 3: Add the constants**

In `broker.go`, inside the existing event-type `const (...)` block, append:

```go
	// Autonomous end-to-end mode (#<spec 2026-06-12>).
	EventAutonomousTaskSelected   = "autonomous_task_selected"    // {repo, number, bucket}
	EventAutonomousTaskReassigned = "autonomous_task_reassigned"  // {repo, number, assignee}
	EventAutonomousStageAdvanced  = "autonomous_stage_advanced"   // {repo, number, from, to}
	EventAutonomousReviewClass    = "autonomous_review_classified" // {repo, number, decision}
	EventAutonomousMergeSkipped   = "autonomous_merge_skipped"    // {repo, number, reason}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd daemon && go test ./internal/sse/ -run TestAutonomousEventConstants -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/sse/broker.go daemon/internal/sse/autonomous_events_test.go
git commit -m "feat(sse): add autonomous-mode event constants"
```

---

## Task 14: Wire the autonomous poller into the daemon

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`
- Create: `daemon/cmd/heimdallm/autonomous_runner.go` (production adapters: `StageRunner`, `CommentGenerator`, candidate fetcher, review-input builder)
- Create: `daemon/cmd/heimdallm/autonomous_runner_test.go`

This task assembles the pieces. Because `main.go` is large, put the adapter types and the tick function in a new sibling file in the same `main` package.

- [ ] **Step 1: Write the failing test (adapter: review-input builder)**

Create `daemon/cmd/heimdallm/autonomous_runner_test.go`:

```go
package main

import (
	"testing"

	"github.com/theburrowhub/heimdallm/daemon/internal/autonomous"
	"github.com/theburrowhub/heimdallm/daemon/internal/github"
)

func TestToReviewInputs_PreservesOrderAndFields(t *testing.T) {
	reviews := []github.PRReview{
		{State: "CHANGES_REQUESTED", Body: "fix this"},
		{State: "APPROVED", Body: "LGTM"},
	}
	got := toReviewInputs(reviews, 0)
	if len(got) != 2 {
		t.Fatalf("want 2 inputs, got %d", len(got))
	}
	if got[0].State != "CHANGES_REQUESTED" || got[1].State != "APPROVED" {
		t.Errorf("order/state not preserved: %+v", got)
	}
	if got[1].Body != "LGTM" {
		t.Errorf("body not carried: %q", got[1].Body)
	}
	// classifier integrates correctly
	if d := autonomous.ClassifyReview(got); d != autonomous.DecisionMergeGate {
		t.Errorf("latest approved clean should be merge-gate, got %v", d)
	}
}
```

> Verify the import path prefix (`github.com/theburrowhub/heimdallm/daemon/...`) against `daemon/go.mod`'s module line and the `github.PRReview` field names (`State`, `Body`) against `github/pr_reviews.go`/`models.go`. Adjust if the module path or field names differ.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./cmd/heimdallm/ -run TestToReviewInputs -race`
Expected: FAIL — `toReviewInputs undefined`.

- [ ] **Step 3: Implement the adapters in `autonomous_runner.go`**

```go
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/theburrowhub/heimdallm/daemon/internal/autonomous"
	"github.com/theburrowhub/heimdallm/daemon/internal/config"
	"github.com/theburrowhub/heimdallm/daemon/internal/executor"
	"github.com/theburrowhub/heimdallm/daemon/internal/github"
	issuepipeline "github.com/theburrowhub/heimdallm/daemon/internal/issues"
)

// toReviewInputs projects GitHub reviews into the classifier's input type,
// preserving chronological order. unresolved is the count of unresolved inline
// review comment threads (0 when unknown).
func toReviewInputs(reviews []github.PRReview, unresolved int) []autonomous.ReviewInput {
	out := make([]autonomous.ReviewInput, 0, len(reviews))
	for i, r := range reviews {
		ui := 0
		if i == len(reviews)-1 {
			ui = unresolved // attribute unresolved threads to the latest review
		}
		out = append(out, autonomous.ReviewInput{State: r.State, Body: r.Body, UnresolvedComments: ui})
	}
	return out
}

// autonomousStageRunner backs autonomous.StageRunner with the existing issue
// pipeline. It overrides label gating (it advances the FSM itself) and runs
// development with full agentic ExecOptions.
type autonomousStageRunner struct {
	pipe    *issuepipeline.Pipeline
	gh      *github.Client
	cfg     *config.Config
	cfgMu   interface{ Lock(); Unlock() }
	token   string
	authUser string
}

func (a *autonomousStageRunner) RunStage(ctx context.Context, stage string, c autonomous.Candidate) (autonomous.StageOutcome, error) {
	issue, err := a.gh.GetIssue(c.Repo, c.Number)
	if err != nil {
		return autonomous.StageOutcome{}, fmt.Errorf("autonomous: get issue: %w", err)
	}
	opts := a.runOptionsForStage(stage, c.Repo)
	review, err := a.pipe.Run(ctx, issue, opts)
	if err != nil {
		return autonomous.StageOutcome{Success: false}, fmt.Errorf("autonomous: run %s: %w", stage, err)
	}
	out := autonomous.StageOutcome{Success: true}
	if review != nil && review.PRCreated != 0 { // confirm field name on store.IssueReview
		out.PRNumber = review.PRCreated
	}
	return out, nil
}

// runOptionsForStage resolves RunOptions, layering full agentic settings for
// development from AutonomousForRepo.
func (a *autonomousStageRunner) runOptionsForStage(stage, repo string) issuepipeline.RunOptions {
	ai := a.cfg.AIForRepo(repo)
	auto := a.cfg.AutonomousForRepo(repo)
	opts := issuepipeline.RunOptions{
		Primary:                  ai.Primary,
		Fallback:                 ai.Fallback,
		GitHubToken:              a.token,
		AuthUser:                 a.authUser,
		PRReviewers:              ai.PRReviewers,
		PRAssignee:               ai.PRAssignee,
		PRLabels:                 ai.PRLabels,
		GeneratePRDescription:    ai.GeneratePRDescription != nil && *ai.GeneratePRDescription,
		RequireWorkDirForDevelop: true,
	}
	if ai.PRDraft != nil {
		opts.PRDraft = *ai.PRDraft
	}
	execOpts := executor.ExecOptions{
		Model:          a.cfg.AgentModelFor(ai.Primary), // use the existing resolver if present; else leave zero
		PermissionMode: "acceptEdits",
		WorkDir:        ai.LocalDir,
	}
	if stage == "development" {
		execOpts.MaxTurns = auto.DevMaxTurns
		execOpts.Effort = auto.DevEffort
		if d, err := time.ParseDuration(auto.DevTimeout); err == nil {
			execOpts.Timeout = d
		}
	}
	opts.ExecOpts = execOpts
	return opts
}

// coordinationCommentGen backs autonomous.CommentGenerator via the executor in
// review-only mode (no workdir), mirroring prReviewExecutor.GenerateReviewResponse.
type coordinationCommentGen struct {
	runner   *executor.Executor
	primary  string
	fallback string
}

func (g *coordinationCommentGen) GenerateCoordinationComment(_ context.Context, c autonomous.Candidate) (string, error) {
	cli, err := g.runner.Detect(g.primary, g.fallback)
	if err != nil {
		return "", err
	}
	prompt := buildCoordinationPrompt(c)
	raw, err := g.runner.ExecuteRaw(cli, prompt, executor.ExecOptions{})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// buildCoordinationPrompt fences the untrusted issue body. Reuse the package's
// existing sanitiser by exposing it, or inline the same fence the issues
// package uses (untrustedBodyFenceOpen/Close + sanitiseUntrustedFreeText).
func buildCoordinationPrompt(c autonomous.Candidate) string {
	var b strings.Builder
	b.WriteString("You are Heimdallm, an autonomous engineering assistant. You are about to start work on the GitHub issue below, which is currently assigned to someone else. Write a short, polite comment (2-3 sentences) announcing you are picking it up to avoid duplicate work, and inviting the original assignee to comment if they object. Plain text, no preamble.\n\n")
	b.WriteString(fmt.Sprintf("Repository: %s\nIssue: #%d %s\n\n", c.Repo, c.Number, c.Title))
	b.WriteString("<<<UNTRUSTED_ISSUE_BODY\n")
	b.WriteString(issuepipeline.SanitiseUntrustedFreeText(c.Body)) // export the existing helper if unexported
	b.WriteString("\nUNTRUSTED_ISSUE_BODY\n")
	return b.String()
}
```

> Three integration confirmations required while implementing:
> 1. `store.IssueReview` PR-number field name — the extracted schema shows `issue_reviews.pr_created`. Confirm the Go struct field (likely `PRCreated`) and use it; if the new code path stores the PR via `prs.auto_implement_issue_id` instead, query that instead of reading `review.PRCreated`.
> 2. `executor.Executor.Detect/ExecuteRaw` are confirmed. `a.cfg.AgentModelFor(...)` is a placeholder for whatever model resolver exists — if there is none, resolve the model from `cfg.AI.Agents[cli]` as the rest of `main.go` does, or omit `Model` (the CLI default applies).
> 3. `SanitiseUntrustedFreeText` is currently unexported in `issues`. Either export it (rename `sanitiseUntrustedFreeText`→`SanitiseUntrustedFreeText` and update callers) in a tiny separate commit, or duplicate the minimal fence here. Prefer exporting to stay DRY.

- [ ] **Step 4: Wire the poller tick in `main.go`**

Locate where the existing tiers/pollers are constructed and started (the `runTier2` / scheduler wiring around the lines that build `issuePipe`, `responder`, `fixRunner`, and resolve `resolvedBotLogin`). Add, after the bot login is resolved and `issuePipe` exists:

```go
	// Autonomous end-to-end mode (#<spec 2026-06-12>). Disabled unless
	// [autonomous].enabled (or an org/repo override) is true; the tick
	// no-ops cheaply when off.
	autoGuard := autonomous.NewPhaseGuard()
	autoRunner := &autonomousStageRunner{
		pipe: issuePipe, gh: ghClient, cfg: &cfg, token: token, authUser: resolvedBotLogin,
	}
	autoOrch := autonomous.NewOrchestrator(autoRunner, autoGuard)
	autoSelector := autonomous.NewSelector(
		s,        // *store.Store implements SelectorStore (HasOpenAutoImplementPR, SetIssueClaimedByAutonomous)
		ghClient, // *github.Client implements SelectorGH (BranchExists, AddAssignees, PostComment)
		resolvedBotLogin,
		issueBranchPrefix(), // the prefix issues/gitops.go uses; expose it as a const
		&coordinationCommentGen{runner: exec, primary: cfg.AI.Primary, fallback: cfg.AI.Fallback},
	)
	startAutonomousPoller(ctx, &cfg, &cfgMu, autoSelector, autoOrch, ghClient, s, broker)
```

Add `startAutonomousPoller` to `autonomous_runner.go`:

```go
// startAutonomousPoller runs one autonomous tick on the daemon's poll cadence.
// Each tick: gather candidates across monitored repos with autonomous enabled,
// Pick one, Claim it, and Drive it through the pipeline. Single-flight inside
// the orchestrator prevents overlap. The poller exits when ctx is cancelled.
func startAutonomousPoller(
	ctx context.Context,
	cfg *config.Config,
	cfgMu interface{ Lock(); Unlock() },
	sel *autonomous.Selector,
	orch *autonomous.Orchestrator,
	gh *github.Client,
	st autonomous.SelectorStore,
	broker autonomousBroker,
) {
	go func() {
		ticker := time.NewTicker(autonomousPollInterval(cfg))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runAutonomousTick(ctx, cfg, sel, orch, gh, broker)
			}
		}
	}()
}
```

Implement `runAutonomousTick` to: (a) build the candidate list for repos where `cfg.AutonomousForRepo(repo).Enabled`, by fetching issues with the existing GitHub issue search the daemon already uses; (b) call `sel.Pick`; (c) on a hit, emit `EventAutonomousTaskSelected`, call `sel.Claim`, then `orch.Drive`. Keep this function thin and delegate to already-tested units.

> Confirm the names: the daemon's store variable (extracted code uses `s`), the executor variable (`exec`), the config mutex (`cfgMu`), the broker (`broker`), and `token`. These all appear in the wiring excerpt from `main.go`. `autonomousBroker` is a tiny local interface with the `Publish`/event method the broker exposes — match it to how `responder`/`fixRunner` publish events.

- [ ] **Step 5: Add the `Selector` constructor**

In `selector.go` add:

```go
// NewSelector builds a Selector. skipLabels should already include both
// skip_labels and blocked_labels for the repos in scope.
func NewSelector(store SelectorStore, gh SelectorGH, botLogin, branchPrefix string, commentGen CommentGenerator) *Selector {
	return &Selector{
		store: store, gh: gh, botLogin: botLogin, branchPrefix: branchPrefix,
		commentGen: commentGen, takeOthers: true, reassign: true,
	}
}

// Configure applies resolved autonomous settings (per tick, per repo scope).
func (s *Selector) Configure(takeOthers, reassign bool, skipLabels []string) {
	s.takeOthers = takeOthers
	s.reassign = reassign
	s.skipLabels = skipLabels
}
```

- [ ] **Step 6: Run the adapter test + full build**

Run: `cd daemon && go test ./cmd/heimdallm/ -run TestToReviewInputs -race`
Expected: PASS

Run: `cd daemon && go build ./... && go vet ./...`
Expected: build + vet clean. Fix any signature mismatches surfaced here against the real `main.go` symbols.

- [ ] **Step 7: Commit**

```bash
git add daemon/cmd/heimdallm/autonomous_runner.go daemon/cmd/heimdallm/autonomous_runner_test.go daemon/cmd/heimdallm/main.go daemon/internal/autonomous/selector.go
git commit -m "feat(autonomous): wire selector+orchestrator poller into the daemon"
```

---

## Task 15: Review-loop integration — classify + merge gate in Tier 3

**Files:**
- Modify: `daemon/cmd/heimdallm/main_pr_review_state.go` (or wherever `refreshAutoImplementPRReviewState` dispatches Responder/FixRunner)
- Test: covered by `autonomous_runner_test.go` (classifier integration already tested in Task 14); add a focused dispatch test if the dispatch function is unit-testable.

- [ ] **Step 1: Read the dispatch site**

Read `refreshAutoImplementPRReviewState` and its dispatch of `responder.Run` / `fixRunner.Run`. Identify where the external review state is computed.

- [ ] **Step 2: Insert classification (autonomous repos only)**

At the dispatch point, when `cfg.AutonomousForRepo(pr.Repo).Enabled`, build review inputs and classify:

```go
	reviews, err := ghClient.GetPRReviews(pr.Repo, pr.Number)
	if err == nil && cfg.AutonomousForRepo(pr.Repo).Enabled {
		decision := autonomous.ClassifyReview(toReviewInputs(reviews, unresolvedThreadCount))
		broker.Publish(sse.EventAutonomousReviewClass, /* {repo, number, decision} json */)
		switch decision {
		case autonomous.DecisionFix:
			// existing FixRunner path (already wired) handles the push.
		case autonomous.DecisionMergeGate:
			auto := cfg.AutonomousForRepo(pr.Repo)
			gate := autonomous.NewMergeGate(ghClient, auto.AutoMerge, auto.MergeMethod)
			if res, _ := gate.Run(ctx, pr.Repo, pr.Number); res == autonomous.MergeSkippedDisabled {
				broker.Publish(sse.EventAutonomousMergeSkipped, /* {repo, number, reason:"auto_merge disabled"} */)
			}
		case autonomous.DecisionWait:
			// keep watching; no action
		}
	}
```

> `unresolvedThreadCount` is optional precision: if the daemon does not already fetch review comment threads, pass `0` (the classifier still routes via `CHANGES_REQUESTED`/`COMMENTED`/actionable-body). Do NOT add a new GitHub call solely for this in the first iteration — keep it `0` and note it. The merge gate is OFF by default, so `DecisionMergeGate` only ever emits the skipped event until `auto_merge` is enabled.

- [ ] **Step 3: Build and full test**

Run: `cd daemon && go build ./... && go vet ./... && go test ./... -race`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add daemon/cmd/heimdallm/main_pr_review_state.go
git commit -m "feat(autonomous): classify reviews + merge gate in Tier 3 dispatch"
```

---

## Task 16: Circuit breaker — apply `CircuitBreakerForRepo` + implement breaker at runtime

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go` (breaker setup) and the development dispatch path
- Test: integration covered; add a unit test if the enforcement is extracted into a helper.

- [ ] **Step 1: Replace global breaker reads with per-repo resolution**

Where `issueCBLimits` is built (extracted at `main.go:277-280`), and at the PR-side breaker setup, switch to per-repo resolution at the point of use. For the autonomous development path, before running the development stage, enforce the implement breaker:

```go
	cb := cfg.CircuitBreakerForRepo(repo)
	if tripped, reason, err := s.CheckImplementCircuitBreaker(repo, store.IssueCircuitBreakerLimits{PerRepoHr: cb.PerImplRepoHr}); err == nil && tripped {
		broker.Publish(sse.EventCircuitBreakerTripped, /* {repo, reason} */)
		// skip development this tick; selector will retry next cycle
		return autonomous.StageOutcome{Success: false}, nil
	}
```

Place this check inside `autonomousStageRunner.RunStage` for `stage == "development"`, before calling `a.pipe.Run`.

- [ ] **Step 2: Build and full test**

Run: `cd daemon && go build ./... && go vet ./... && go test ./... -race`
Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add daemon/cmd/heimdallm/main.go daemon/cmd/heimdallm/autonomous_runner.go
git commit -m "feat(autonomous): enforce per-repo implement breaker before development"
```

---

## Task 17: Documentation — configuration guide

**Files:**
- Modify: `docs/configuration-guide.md`

- [ ] **Step 1: Document `[autonomous]`**

Add a section describing every key (`enabled`, `auto_merge`, `merge_method`, `take_others_tasks`, `reassign_on_take`, `dev_max_turns`, `dev_effort`, `dev_timeout`), the `[autonomous.orgs."org"]` / `[autonomous.repos."org/repo"]` overrides, and the new `circuit_breaker.per_impl_repo_hr` plus its org/repo layering. Include a worked example enabling autonomous mode for a single repo with a conservative implement cap. State explicitly that `auto_merge` is built but defaults OFF.

- [ ] **Step 2: Commit**

```bash
git add docs/configuration-guide.md
git commit -m "docs: document [autonomous] mode and per-repo circuit breakers"
```

---

## Final verification (before PR)

- [ ] `cd daemon && go vet ./... && go test ./... -race` — all green.
- [ ] `cd daemon && make build` (or `make build-daemon` from repo root) — daemon binary builds.
- [ ] Manual smoke: run the daemon with `[autonomous] enabled = true` for one test repo against a throwaway issue; confirm via SSE/logs the sequence `autonomous_task_selected` → stage advances → PR created → review-state vigilance active. With `auto_merge=false`, confirm an approved-clean PR emits `autonomous_merge_skipped` and is NOT merged.
- [ ] Confirm with `auto_merge` left OFF that no merge ever occurs.

---

## Self-Review notes (addressed)

- **Spec coverage:** selection cascade (T6–8), single-flight pipeline (T11–12), self-driven stage advance overriding labels (T12 + T14 StageRunner), agentic development (T14 ExecOptions), reassign+agent comment (T8+T14), review classification incl. approved-with-issues (T9+T15), merge gate OFF (T10+T15), implement breaker + global→org→repo layering (T1–2, T16), SSE observability (T13), persistence (T4), config (T3), docs (T17). All spec sections mapped.
- **Integration confirmations** are flagged inline at each adapter (module path, `IssueReview.PRCreated` field, branch-naming pattern, `sanitise` export, model resolver) — these are the only points where the plan defers to a one-line check against the real symbol, because they are facts in files not fully quoted during planning. Every such note states the exact fallback.
- **Naming consistency:** `isEligible` (renamed from `notStarted`) used consistently across T6/T7; `StageOutcome`/`DriveResult` consistent T12/T14; `MergeResult` consistent T10/T15.
