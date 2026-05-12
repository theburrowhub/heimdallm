# Plan — Issue #461: Per-execution git worktrees for concurrent pipelines

Self-contained execution spec. After context compaction, re-read this file in full and execute Phase 1 onward without needing prior conversation.

## Problem statement (verbatim from #461)

Multiple pipeline stages on the same repo currently share a single working tree, serialised by an exclusive per-repo lock (`daemon/internal/repoctx`). Concurrent ops on the same repo wait for the lock — even read-only ones — and an in-flight execution can be derailed by branch / commit / push collisions if the lock model ever relaxes.

Issue #461 proposes: every pipeline execution gets its own **git worktree** inside the repo at `<clone>/.worktrees/<token>/`, sharing the `.git/` object store. The exclusive lock relaxes; per-repo concurrency is capped by `max_worktrees_per_repo`.

## Current state — evidence

| Concern | Location | Snippet / fact |
|---|---|---|
| Per-repo binary semaphore | `daemon/internal/repoctx/manager.go:90-93,528-552` | `repoLock{ch chan struct{}, refs int}` — one slot |
| Acquire entry point | `manager.go:138-181` (`Manager.Acquire`) | Validates repo, takes lock, ensures clone, optionally unshallows, returns `Handle` whose `Release()` drops the lock |
| Marker file | `manager.go:20-22` | `.heimdallm-managed` JSON file identifies managed clones |
| Modes | `manager.go:30-42` | `ModeRead` accepts shared mounts; `ModeWrite` forces a managed clone + unshallow |
| Wrapper used by callers | `daemon/cmd/heimdallm/main.go:3616-3644` (`acquireRepoContext`) | Wraps `Manager.Acquire`, mutates `aiCfg.LocalDir` to the handle's path |
| GitOps interface | `daemon/internal/issues/gitops.go:40-68` | `CheckoutNewBranch`, `HasChanges`, `CommitAll`, `Push`, `DeleteRemoteBranch`, `Diff` — all keyed on `dir` |
| Auto-implement git flow | `daemon/internal/issues/pipeline.go:639-840` (`runAutoImplement`) | `CheckoutNewBranch` → exec CLI → `HasChanges` → `CommitAll` → `Push` → `CreatePR` |
| PR review path | `daemon/internal/pipeline/pipeline.go` | Consumes `LocalDir` as agent context only; no git mutations |
| Existing worktree usage | grep `git worktree` in daemon/ | **None.** No partial implementation |
| Startup purge | `main.go:329-330` | `runCloneRetention("startup")` + 24h periodic via `scheduler.New` |
| Stale-claim sweep model | `main.go:136-147` | `ClearStaleInFlight` / `ClearStaleIssueTriageInFlight` at startup, 30-min cutoff — pattern to follow for stale worktrees |

### All 7 callsites of `acquireRepoContext`

| # | Caller | File:line | Mode | Special |
|---|---|---|---|---|
| 1 | `review-worker` (PR review) | `main.go:689` | Read | — |
| 2 | `triage-worker` | `main.go:856` | Read | Calls `ensureRepoContextFullHistory` |
| 3 | `implement-worker` | `main.go:1046` | Write | Fatal on err |
| 4 | `dependency-promoter` | `main.go:1501` | Read | Uses `context.Background()` |
| 5 | tier-2 PR processor | `main.go:2591` | Read | — |
| 6 | issue-stage handler (refinement / develop) | `main.go:2714` | Dynamic (Read/Write) | — |
| 7 | HTTP `GET /config/clones` | `main.go:3353` | Read | Inspection only |

## Design

### Core idea

Replace the single-slot per-repo lock with a **two-tier model**:

1. **Critical section mutex** (short-lived): held only across `git worktree add` and `git worktree remove`. Protects the shared `.git/worktrees/` registry against concurrent mutations.
2. **Worktree-count semaphore** (long-lived): N slots where N = `max_worktrees_per_repo`. Acquired *before* the worktree exists, released *after* it is removed. Blocks (FIFO via the channel) when at capacity → satisfies the issue's "queue if at capacity" requirement.

The clone itself stays in place (shared object store). Each execution acts on its own subdir.

### Worktree path scheme

```
<clone>/
  .git/
    worktrees/             ← git's internal registry, do not touch
  .worktrees/              ← Heimdallm's checkout root (gitignored)
    pr-review-1234/        ← detached HEAD at PR head SHA
    issue-42-triage/       ← detached HEAD at base ref (main)
    issue-42-refinement/   ← detached HEAD at base ref
    issue-57-develop/      ← branch heimdallm/issue-57, checked out
    dep-promote-1234/      ← detached HEAD at base ref
```

Token is `<stage>-<n>` for stages with a clear key (`triage-42`, `pr-review-1234`), or `<purpose>-<random>` for inspection endpoints (`inspect-<8-hex>`). Computed by callers; manager only validates `[A-Za-z0-9_-]+` and rejects path traversal.

### API changes

#### `daemon/internal/repoctx/manager.go`

**Extend `Request`:**

```go
type Request struct {
    Repo               string
    ConfiguredLocalDir string
    LocalDirBases      []string
    CloneDir           string
    Token              string

    Mode Mode

    // NEW — identifies the execution; becomes the worktree subdir
    // under `<clone>/.worktrees/<WorktreeToken>/`. Required for
    // every mode except ModeInspect. Sanitised — reject path
    // traversal and characters outside [A-Za-z0-9_-].
    WorktreeToken string

    // NEW — optional. When set, the worktree is created at this ref
    // (`git worktree add <path> --detach <ref>`). PR review uses
    // the PR head SHA; triage/refinement use the repo default
    // branch (or leave empty for HEAD).
    WorktreeBaseRef string

    // NEW — optional. When non-empty, the worktree is created with
    // a fresh branch (`git worktree add <path> -b <Branch> <BaseRef>`).
    // Used by the develop stage. Only meaningful with ModeWrite.
    Branch string

    // NEW — when true, skip worktree creation; just return a
    // Handle whose Path() points at the main clone. Used by the
    // HTTP /config/clones inspection endpoint (callsite #7).
    Inspect bool
}
```

**Extend `Mode`:**

Keep `ModeRead` and `ModeWrite`. Add no new mode for inspect — use the `Inspect bool` flag instead (Mode still influences unshallow + LocalDirBase acceptance).

**Internal state on `Manager`:**

```go
type Manager struct {
    mu      sync.Mutex
    locks   map[string]*repoLock   // critical-section lock (binary)
    caps    map[string]*repoCap    // worktree-count semaphore (N slots)
    git     gitRunner
    tempDir func() string

    maxWorktrees int // injected from config; defaults to 5
}

type repoCap struct {
    ch   chan struct{} // capacity = maxWorktrees
    refs int
}
```

**`Acquire()` algorithm (new):**

```
0. Validate Repo (owner/name) and Token + WorktreeToken sanitisation.
1. If req.Inspect:
     - Take critical-section lock.
     - Resolve / bootstrap clone path (existing logic).
     - Release critical-section lock.
     - Return Handle whose Path() = clone path, Release() = no-op.
2. Acquire per-repo worktree-count semaphore (blocks when full).
     - This is the queue. Cancellable via ctx.
3. Take critical-section lock.
4. Ensure managed clone (existing ensureManagedClone path).
5. Ensure clone-level gitignore covers `.worktrees/` (write if absent).
6. Compute worktree path = filepath.Join(clone, ".worktrees", token).
     - If path already exists: prune + remove + recreate (defensive).
7. Build `git worktree add` args:
     - `git worktree add <path> --detach <BaseRef|HEAD>` for read-only.
     - `git worktree add <path> -b <Branch> <BaseRef|HEAD>` for develop.
8. Run worktree add (under crit-section lock).
9. If ModeWrite: call EnsureFullHistory on the main clone first
   (worktrees inherit history).
10. Release critical-section lock.
11. Return Handle{
       path: worktree path,
       release: () => {
         take crit-section lock;
         run `git worktree remove --force <path>`;
         release crit-section lock;
         release worktree-count semaphore;
       }
    }.
```

**Failure / cleanup** at each step: roll back acquired resources so we never leak a semaphore slot or leftover worktree. Specifically:

- If clone bootstrap fails after sem acquire → release sem.
- If `worktree add` fails after crit lock → release crit lock + sem.
- Use defer with a `committed bool` flag so the success path can skip the rollback.

**New methods on `Manager`:**

```go
// PruneStaleWorktrees enumerates `<clone>/.worktrees/*` and
// removes any subdirectory whose path is missing from
// `git worktree list --porcelain`, then runs `git worktree prune`
// to clean git's own registry. Returns count of pruned worktrees.
//
// Called at startup (post-clone-purge) and from a periodic sweep so
// daemon crashes mid-execution don't leak worktrees indefinitely.
func (m *Manager) PruneStaleWorktrees(ctx context.Context, cloneDir string) (int, error)
```

**Constructor accepts cap:**

```go
func NewManagerWithOptions(opts ManagerOptions) *Manager {
    // opts.MaxWorktreesPerRepo (default 5 if 0)
}
```

Keep `NewManager()` as a convenience that delegates with defaults.

### Config changes

#### `daemon/internal/config/config.go`

Extend `GitHubConfig`:

```go
type GitHubConfig struct {
    // ... existing fields
    MaxWorktreesPerRepo int `toml:"max_worktrees_per_repo"`
}
```

Default resolution: if zero, treat as 5. Surface in `config.toml` example.

### Callsite changes

Each of the 7 callsites adds a `WorktreeToken` (and where relevant, `WorktreeBaseRef` / `Branch`). New helper for token sanitisation lives next to `acquireRepoContext`.

| Site | New `WorktreeToken` | `WorktreeBaseRef` | `Branch` | `Inspect` |
|---|---|---|---|---|
| review-worker (`main.go:689`) | `pr-review-<pr.Number>` | `pr.Head.SHA` if present, else default | — | false |
| triage-worker (`main.go:856`) | `triage-<issue.Number>` | default branch | — | false |
| implement-worker (`main.go:1046`) | `develop-<issue.Number>` | default branch | `heimdallm/issue-<n>` | false |
| dependency-promoter (`main.go:1501`) | `deps-<pr.Number>` | default branch | — | false |
| tier-2 PR (`main.go:2591`) | `pr-tier2-<pr.Number>` | `pr.Head.SHA` if present | — | false |
| issue-stage (`main.go:2714`) | `<stage>-<issue.Number>` (refinement/develop) | default | dev: `heimdallm/issue-<n>` | false |
| `/config/clones` (`main.go:3353`) | `inspect-<random>` | — | — | true |

Wrapper update:

```go
func acquireRepoContext(
    ctx context.Context,
    manager *repoctx.Manager,
    repo string,
    aiCfg *config.RepoAI,
    localDirBase []string,
    token string,
    mode repoctx.Mode,
    wtToken string,                // NEW
    wtBaseRef string,              // NEW (optional)
    branch string,                 // NEW (optional)
) (*repoctx.Handle, error) {
    ...
}
```

For the inspect callsite, expose a separate helper:

```go
func acquireRepoInspect(ctx, manager, repo, ...) (*repoctx.Handle, error) {
    // req.Inspect = true
}
```

### GitOps adaptation

GitOps already takes `dir` as the working directory — switching to the worktree path requires **no signature changes**. Internal commands run in the worktree; fetch / push reach the shared `.git/`. Verify by:

1. Auditing `gitops.go` for any path that joins `dir + ".git/"` directly (we'd need to use `dir` as a worktree-aware root). Spot check: `HasChanges`, `CommitAll` use `git -C dir`. ✓ worktree-safe.
2. The marker file (`.heimdallm-managed`) lives in the clone root, not the worktree. `HasChanges` and `CommitAll` already exclude it; in a worktree the marker isn't even present, so the exclusion is a no-op. ✓

The auto-implement flow becomes simpler:

- `CheckoutNewBranch` is no longer strictly needed because the worktree is created at the branch (`worktree add -b`). **Decision**: keep `CheckoutNewBranch` for now — it works fine inside a worktree (fetch updates shared `.git`, reset rewinds the worktree HEAD), and removing it widens scope. A follow-up can simplify.

### Bootstrap / gitignore

On clone bootstrap (`ensureManagedClone`), after the initial fetch:

1. Ensure a `.gitignore` exists in the clone root.
2. If `.worktrees/` is missing from it, append the entry.
3. Marker file write proceeds as today.

For user-mapped `local_dir` repos (not auto-cloned, no marker), Heimdallm cannot freely edit `.gitignore`. On first acquire:

1. Check `.gitignore` for `.worktrees/`.
2. If missing, log a `slog.Warn("repoctx: local_dir gitignore is missing .worktrees/, consider adding it")`. Proceed regardless — worktree still works.

### Startup / periodic cleanup

`main.go` startup hook (next to existing in-flight sweeps, ~line 136):

```go
// Prune leftover worktrees from a previous daemon process. Mirrors
// the in-flight DB sweep above — a crash between worktree add and
// release leaves a directory + a registry entry that the next run
// would otherwise fail to overwrite.
for _, cloneDir := range managedCloneDirs(cfg) {
    if n, err := repoCtx.PruneStaleWorktrees(ctx, cloneDir); err != nil {
        slog.Warn("startup: prune stale worktrees", "dir", cloneDir, "err", err)
    } else if n > 0 {
        slog.Info("startup: pruned stale worktrees", "dir", cloneDir, "count", n)
    }
}
```

Also wire `PruneStaleWorktrees` into the same 24h `clonePurge` scheduler so periodic runs catch slow leaks.

### Tests

#### `daemon/internal/repoctx/manager_test.go` — new

| Test | What it pins |
|---|---|
| `TestAcquireCreatesWorktreeUnderManagedClone` | After `Acquire` returns, `<clone>/.worktrees/<token>` exists and `Handle.Path()` points at it |
| `TestAcquireReleaseRemovesWorktreeAndFreesSlot` | `Release()` removes the directory + lets the next `Acquire` (at cap) proceed |
| `TestAcquireBlocksWhenAtMaxWorktreesPerRepo` | With cap=2 and two held handles, a third `Acquire` blocks; releasing one unblocks it |
| `TestAcquireCtxCancelDuringQueueReturnsErr` | When ctx is cancelled while waiting on cap semaphore, returns ctx.Err() and does not leak a slot |
| `TestAcquireConcurrentSameRepoUsesDistinctWorktrees` | Two parallel `Acquire`s under the same repo each get a unique path |
| `TestAcquireWithBranchCreatesAndChecksOut` | `Branch=heimdallm/issue-42` results in a worktree whose HEAD points at that branch |
| `TestAcquireInspectReturnsCloneRoot` | `Inspect=true` → no `.worktrees/...` dir created; path equals the clone root |
| `TestAcquireRejectsPathTraversalToken` | `WorktreeToken=".."` and other dangerous values are rejected before any git ops |
| `TestAcquireFailureRollsBackSemSlot` | If `worktree add` errors (e.g., garbage `BaseRef`), the cap semaphore is released so subsequent acquires succeed |
| `TestPruneStaleWorktreesRemovesOrphans` | Manually-created orphan dir under `.worktrees/` is removed; live entries from `git worktree list` survive |
| `TestBootstrapAddsWorktreesToGitignore` | Fresh clone's `.gitignore` includes `.worktrees/`; idempotent (no duplicate line on re-run) |

#### `daemon/cmd/heimdallm/main_test.go` — extend

| Test | What it pins |
|---|---|
| `TestAcquireRepoContextPassesWorktreeToken` | The wrapper propagates `WorktreeToken` to `Request` |
| `TestAcquireRepoInspectSkipsWorktree` | The inspect helper sets `Inspect=true` |

#### Integration considerations

Worker-entry tests already exist via the `acquireRepoContext` wrapper. Update fixtures to thread through the new token args without changing behaviour assertions.

## Implementation order (TDD)

Each step has a RED commit followed by a GREEN commit. Run `make test-docker` between steps.

1. **Manager: WorktreeToken plumbing + path resolution** (no worktree creation yet).
   - Tests: token sanitisation, path computation.
   - Code: extend `Request`, validate token.

2. **Manager: worktree add/remove with cap-1 (binary) semaphore**.
   - Tests: create + release single worktree.
   - Code: critical-section lock + first worktree-count semaphore (cap=1 initially).

3. **Manager: N-slot semaphore + queuing**.
   - Tests: cap blocking, ctx cancel mid-queue.
   - Code: parameterise cap via `NewManagerWithOptions`.

4. **Manager: `Inspect` shortcut**.
   - Tests: clone-root return without worktree dir.
   - Code: branch in `Acquire`.

5. **Manager: branch + base-ref**.
   - Tests: `-b` branch, `--detach <ref>`.
   - Code: arg builder.

6. **Bootstrap: gitignore + warn for user-mapped repos**.
   - Tests: idempotent `.gitignore` write, warn on user-mapped.

7. **PruneStaleWorktrees + startup wiring**.
   - Tests: orphan removal, live entries survive.

8. **Config: `max_worktrees_per_repo`**.
   - Tests: default = 5, override honoured.

9. **Callsite migration** (one PR can do all 7 sites, or split into review-side and issue-side commits).
   - Update `acquireRepoContext` wrapper signature.
   - Each site supplies its token + base-ref + branch.
   - HTTP endpoint uses the new `acquireRepoInspect` helper.

10. **Full suite + smoke test**.
    - Run two concurrent stages on the same repo manually (e.g., trigger an issue triage + a PR review simultaneously) — verify both run.

## Out-of-scope / explicit follow-ups

- **PR review at PR head SHA**: included here (callsite uses `pr.Head.SHA` as `WorktreeBaseRef`). If too risky in one PR, deliver minimal scope first (always `HEAD`) and follow up.
- **Simplify GitOps to drop `CheckoutNewBranch`** now that `worktree add -b` does the same job. Follow-up.
- **Per-stage worktree TTL** beyond release: not yet — the startup prune + periodic sweep handle the leak case.
- **Surface worktree state in the GUI**: not in scope. Operators can `git worktree list` on the clone.

## Branch / PR mechanics

- Branch: `feat/issue-461-worktrees-concurrent-pipelines` off `main`.
- Open as **draft**.
- Tests via `make test-docker` (project rule, EDR alerts on raw `go test`).
- Commit cadence: one per step in the TDD order above, or merged if commits are tiny.
- PR title: `feat(repoctx): per-execution git worktrees for concurrent pipelines (#461)`.
- PR body: link to this plan file, summarise the seven callsite changes, list new config knob.

## Risks / known unknowns

| Risk | Mitigation |
|---|---|
| `git worktree add` is racy across daemons sharing the same clone | The critical-section lock guards the add; `git` itself locks `.git/worktrees/` atomically inside |
| Worktrees increase disk usage | Cap via `max_worktrees_per_repo`; prune on startup + periodic |
| Existing `local_dir` users have uncommitted work in main worktree | Worktrees are isolated; main worktree is untouched. The `.gitignore` warning surfaces the only side effect (an ignored dir appears) |
| Auto-implement push from a worktree | `git push` from a worktree pushes the branch from the shared object store — semantically identical to today |
| Test fixtures that mock `gitRunner` may need updates | Inspect `manager_test.go` fixtures; extend `gitRunner` interface as a single source of truth |

## Quick re-entry checklist (post-compact)

1. `cd /Users/imunoz/Projects/ai-platform/heimdallm && git checkout main && git pull --ff-only`
2. `git checkout -b feat/issue-461-worktrees-concurrent-pipelines`
3. Read this plan top-to-bottom.
4. Re-read these reference files for hot context:
   - `daemon/internal/repoctx/manager.go` (full)
   - `daemon/internal/repoctx/manager_test.go` (test patterns)
   - `daemon/cmd/heimdallm/main.go` (lines 689, 856, 1046, 1501, 2591, 2714, 3353 for callsites; lines 136-147 for startup-sweep pattern; lines 3616-3644 for wrapper)
   - `daemon/internal/issues/gitops.go` + `pipeline.go:639-840` (auto-implement flow)
5. Start with TDD step 1 (manager WorktreeToken plumbing). Use `make test-docker` between every change.
