# Plan — Issue #489: handle GitHub repo / org renames

Closes `theburrowhub/heimdallm#489` (URGENT). Detect a repo or org
rename on GitHub and propagate the slug change atomically through
every piece of daemon state keyed on the old `org/repo` string.

## Problem statement (verbatim from the issue)

When a repository or its parent org is renamed on GitHub, Heimdallm
continues to operate against the **old slug**. GitHub silently 301s
the API for a grace period which masks the problem, but symptoms
accumulate over time:

- Polling silently 404s when the redirect expires.
- `prs.repo` / `issues.repo` rows retain the old slug → duplicate
  rows once the new slug is discovered.
- `[ai.repos."old/name"]` overrides stop applying because
  `AIForRepo()` looks up by the current slug.
- Working dirs under `local_dir_base/old-org/old-repo` go stale.
- `watch_state` KV entries keep the old `repo` field.
- The `repositories` / `non_monitored` lists in config retain the
  old name forever — no reconciliation path renames an entry.

Both repo renames (`org/old-name` → `org/new-name`) AND org renames
(`old-org/repo` → `new-org/repo`) are in scope. Org renames are
the worst case (every repo under the org flips at once).

## Current state — evidence

| Concern | Location | Snippet / fact |
|---|---|---|
| Schema columns keyed on `repo` | `store/store.go:26-130` | `prs.repo`, `issues.repo`, `activity_log.repo`, watch_state.repo (via `bus/watch.go`). In-flight tables (`reviews_in_flight`, `issue_triage_in_flight`) are keyed on numeric IDs — safe. |
| AIForRepo lookup | `config.AIForRepo(repo)` | Per-repo overrides keyed on the current slug; a renamed repo silently loses its override. |
| Config TOML writer | `config/writer.go:157` | `AtomicWriteTOML(path, map[string]any)` rewrites via tmp + rename. No key-rename helper today. |
| Discovery insert | `cmd/heimdallm/main.go:2378` (`upsertDiscoveredRepos`) | When a PR appears under the new slug, it's added as a NEW repo. Old entry never removed. |
| GH client redirect handling | `github/client.go` | `http.Client` follows 301/302 automatically but the response struct we expose doesn't surface the final URL. `GetPR` returns `Head.Repo.FullName` which is canonical — useful incidentally but not as a detector. |
| Working dirs | `repoctx/manager.go:716` (`cloneTarget`) | Path is `base/owner/name/`. No `Rename(old, new)` method today. `Purge` and `PurgeAll` exist. |
| Watch KV value shape | `bus/watch.go:18-29` (`WatchEntry`) | Carries `Repo`, `Number`, `GithubID`. Keyed in SQLite on `type.<id>` — not on the slug — so the slug is updateable in place. |
| SSE event constants | `sse/broker.go` | `EventRepoDiscovered` exists; no `EventRepoRenamed` yet. |
| Concurrency primitives | `cfgMu`, `repoctx.locks`, `store.db (SetMaxOpenConns=1)` | All available to wrap the reconciliation in a single critical section. |

## Design

### A. Detection (`github/client.go` + a per-repo poller)

Reactive detection alone is not enough — many of our calls go through
the Pulls API and never surface the parent repo's canonical name. We
need a dedicated lightweight probe:

```go
// GetCanonicalFullName returns the current canonical `full_name` for
// the repo at `owner/name`. Returns the SAME value when the repo
// has not been renamed; the caller compares. A 404 (`*APIError`,
// StatusCode=404) means the repo no longer exists (deleted, not
// just renamed); callers treat that as a separate "unreachable"
// state and emit EventRepoUnreachable rather than EventRepoRenamed.
func (c *Client) GetCanonicalFullName(repo string) (string, error)
```

Implementation: `GET /repos/{owner}/{repo}` already returns the
canonical `full_name` even when called with the old slug (GitHub
issues a 301 → 200 with the new name). Single network call per
repo, cheap.

**Where the probe runs.** A new low-frequency goroutine inside
`runTier2` (or a dedicated Tier 4) — once per `repo_rename_check_interval`
(default 1h, configurable). Iterates `cfg.Repositories`, calls the
probe, dispatches to the reconciler when the canonical name differs.
Bot-discovered repos are also covered because they're appended to
the same list.

### B. Reconciler (`internal/rename/reconciler.go`, new package)

Single entry point that wraps every side-effect under the right lock:

```go
type Reconciler struct {
    store    renameStore
    cfgMu    *sync.Mutex
    cfg      **config.Config
    cfgPath  string
    repoCtx  *repoctx.Manager
    broker   eventPublisher
}

// Run renames `oldRepo` → `newRepo` across the daemon. Idempotent:
// a second call with the same pair, or a call where oldRepo no
// longer exists in any store row + config list, is a no-op.
func (r *Reconciler) Run(ctx context.Context, oldRepo, newRepo string) error
```

Steps inside `Run`, in order:

1. Validate inputs (non-empty, different, `owner/name` shape).
2. `cfgMu.Lock()`. Make a defensive copy of the in-memory config.
3. Open a SQLite transaction; run bulk UPDATEs against `prs`,
   `issues`, `activity_log`, `watch_state` setting `repo = ? WHERE repo = ?`.
   Same TX inserts an audit row into the new `repo_renames` table
   (`old_repo TEXT, new_repo TEXT, renamed_at DATETIME`).
4. Commit. On error, abort and return — config + filesystem stay
   untouched.
5. Mutate the in-memory config: rename keys in `Repositories`,
   `NonMonitored`, `AI.Repos[<old>]` → `AI.Repos[<new>]`.
   **For ORG renames** (`old-org/*` → `new-org/*`), also move
   `AI.Orgs[<old-org>]` → `AI.Orgs[<new-org>]` once per reconcile
   batch — the org-level overrides are keyed on the bare org name,
   not `owner/name`, so a repo-level reconcile must check whether
   the org component changed and rename the org map entry too.
   Persist via `AtomicWriteTOML` (new helper `RenameRepoInTOML`
   that reads the existing file as a `map[string]any`, renames the
   nested key, writes back atomically so we do not lose unrelated
   keys the daemon does not model).
6. **Working-dir reset, not move.** `os.Rename(oldClonePath,
   newClonePath)` is fragile across filesystem boundaries, hits
   permission edge cases, and races with in-flight worktree
   acquires under `repoctx`. Instead: under the repoctx
   critical-section lock for the OLD repo, call
   `repoctx.Manager.Purge(oldRepo)` (drops the on-disk tree).
   The next acquire of the NEW slug will clone fresh from the
   canonical GitHub URL — that path already exists and is well
   tested. Trade-off: one extra clone after a rename, which is
   bounded and cheap compared to the failure modes of `os.Rename`.
   Purge failure is non-fatal (logged + SSE
   `worktree_purged: false`).
7. `cfgMu.Unlock()`.
8. Emit `EventRepoRenamed{old_repo, new_repo, worktree_purged}`.

Locking order is `cfgMu` → repoctx lock → store transaction; the
existing codebase already uses cfgMu as the outermost lock, so this
matches the established discipline.

### C. Idempotency + audit

A new table:

```sql
CREATE TABLE repo_renames (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  old_repo    TEXT NOT NULL,
  new_repo    TEXT NOT NULL,
  renamed_at  DATETIME NOT NULL
);
CREATE INDEX idx_repo_renames_old ON repo_renames(old_repo);
```

`Run` checks at step 1: if the most recent `repo_renames` row for
`old_repo` is `new_repo`, return nil (already done). This keeps a
restarted daemon from re-emitting events for an already-completed
rename.

### D. New SSE event

```go
const EventRepoRenamed = "repo_renamed"
// payload: {"old_repo": "...", "new_repo": "...", "worktree_renamed": true}
```

Flutter side: `dashboard_providers.dart` bumps the repo + issue +
PR list refreshes and dismisses any cached entries keyed on
`old_repo`.

### E. Config knob

```toml
[ai]
repo_rename_check_interval = "1h"  # default; "0" disables the probe entirely
```

The flag does NOT gate the reconciler — operators can trigger a
manual rename via a new `POST /admin/repo-rename` endpoint (also
in scope, behind the existing API token) for emergencies.

## Tests (TDD order)

**Store layer (`store/rename_test.go`):**

| Test | What it pins |
|---|---|
| `TestStore_RenameRepo_UpdatesAllTables` | After RenameRepo, every `repo` column on prs / issues / activity_log / watch_state moves to the new slug. |
| `TestStore_RenameRepo_LeavesUnrelatedRowsUntouched` | Rows for a different repo are not modified. |
| `TestStore_RenameRepo_InsertsAuditRow` | `repo_renames` carries the pair + timestamp. |
| `TestStore_RenameRepo_IsAtomic_OnError` | An injected error during the issues UPDATE rolls back every prior UPDATE in the same TX. |
| `TestStore_RenameRepo_Idempotent` | Calling twice produces one audit row, second call is a no-op. |

**Config writer (`config/writer_rename_test.go`):**

| Test | What it pins |
|---|---|
| `TestRenameRepoInTOML_RenamesAIRepoKey` | `[ai.repos."old/name"]` becomes `[ai.repos."new/name"]` with identical values. |
| `TestRenameRepoInTOML_UpdatesRepositoriesList` | `repositories = [..., "old/name", ...]` becomes `[..., "new/name", ...]` preserving order and dedup. |
| `TestRenameRepoInTOML_UpdatesNonMonitoredList` | Same for `non_monitored`. |
| `TestRenameRepoInTOML_PreservesUnrelatedTopLevelKeys` | Operator-only keys we don't model (custom sections) survive the rewrite. |
| `TestRenameRepoInTOML_NoOpWhenOldAbsent` | Returns nil + no rewrite when the old slug isn't present. |
| `TestRenameRepoInTOML_RenamesAIOrgKeyWhenOrgChanged` | When `oldOrg != newOrg`, the `[ai.orgs."old-org"]` block moves to `[ai.orgs."new-org"]` preserving values. |

**GitHub client (`github/canonical_test.go`):**

| Test | What it pins |
|---|---|
| `TestGetCanonicalFullName_ReturnsNewSlug` | Stubbed `/repos/old/name` returns `{"full_name":"new/name", ...}`; the wrapper returns `"new/name"`. |
| `TestGetCanonicalFullName_404_PropagatesAPIError` | 404 yields an `*APIError` with StatusCode=404 (the caller treats it as repo-deleted, NOT a rename). |

**Reconciler (`internal/rename/reconciler_test.go`):**

| Test | What it pins |
|---|---|
| `TestReconciler_Run_FullPipeline` | Store + config + (skipped) worktree + SSE all flip in one call; payload has expected fields. |
| `TestReconciler_Run_Idempotent` | A second invocation with the same pair returns nil and emits no second SSE. |
| `TestReconciler_Run_ValidationRejectsEmpty` | Empty / equal / malformed slugs return error before any side effect. |
| `TestReconciler_Run_StoreErrorAbortsConfig` | Injected store error leaves config TOML untouched on disk. |
| `TestReconciler_Run_WorktreePurgeFailure_NotFatal` | A `Purge` failure is logged + SSE field `worktree_purged=false`; store + config still committed. |

**Detector (`cmd/heimdallm/main_rename_probe_test.go`):**

| Test | What it pins |
|---|---|
| `TestRenameProbe_DispatchesReconcilerOnMismatch` | Stub GH returns a new full_name; probe calls reconciler with `(old, new)`. |
| `TestRenameProbe_NoOpWhenCanonicalMatches` | Same name in == out — reconciler not invoked. |
| `TestRenameProbe_404FromGH_EmitsUnreachableEvent` | 404 emits a separate event, does NOT dispatch the reconciler. |

## Implementation order (TDD)

1. **Store + audit table.** RED+GREEN per the 5 store tests. Schema
   migration is the idempotent `CREATE TABLE IF NOT EXISTS` block
   in `store.go`.
2. **GH client probe.** RED+GREEN per the 2 client tests.
3. **Config rename helper.** RED+GREEN per the 5 writer tests.
   Includes the TOML-map rename so unrelated keys round-trip.
4. **Reconciler.** RED+GREEN per the 5 reconciler tests; uses
   fakes for store / config-writer / repoctx / broker.
5. **Probe goroutine.** RED+GREEN per the 3 probe tests.
6. **Wire into main.go.** Construct the reconciler with real deps,
   start the probe inside `runTier2` (or sibling goroutine).
7. **SSE event constant + Flutter case.** New
   `EventRepoRenamed` + `dashboard_providers.dart` case that
   bumps repo / issue / PR list refresh.
8. **Manual trigger endpoint** `POST /admin/repo-rename` with API
   token check (mirrors existing admin surface).
9. **Docs.** New section in `configuration-guide.md` near the
   discovery/monitoring block.

## Risks

| Risk | Mitigation |
|---|---|
| Concurrent poll writes a row under the OLD slug while the rename is in flight | The transaction holds the SQLite writer lock (`SetMaxOpenConns=1`); the reconciler holds `cfgMu` so the config-driven write paths block until the rename commits. |
| Config TOML round-trip drops unrelated keys we do not model | The writer reads via `map[string]any`, mutates only the keys it knows about, writes back. Pinned by `TestRenameRepoInTOML_PreservesUnrelatedTopLevelKeys`. |
| Working-dir purge fails (permission, in-use worktree, FS error) | Non-fatal; SSE carries `worktree_purged=false`; the next acquire of the NEW slug clones fresh anyway. Operator may run a manual purge if old dir lingers as orphan. |
| Daemon crashes mid-reconcile (store committed, config not yet written) | On restart, the store's `repo_renames` audit row is present but config still has the old slug → the probe runs again, the reconciler's idempotency guard sees the audit and re-applies only the config + worktree steps. |
| GitHub returns a 301 chain (old → intermediate → new) on rename + re-rename | `http.Client` follows the chain; `GetCanonicalFullName` returns the terminal `full_name`. Audit logs every step. |
| Cross-fork PRs whose `head.repo.full_name` differs from the base | Out of scope; we only reconcile the BASE repo slug, never the head. |
| Cap on probe API calls | One call per repo per `repo_rename_check_interval`; bounded by `len(cfg.Repositories)` per cycle. Same rate-limit budget as the discovery poll. |

## Out of scope (follow-ups)

- Webhook-driven detection (would eliminate the 1h probe but
  requires operator infra).
- Cross-fork base-repo renames mid-PR review.
- Backfill `repo_renames` for renames that happened before this
  PR landed.
- GUI toggle for `repo_rename_check_interval`.
- **`upsertDiscoveredRepos` rename race** — after the canonical
  probe finishes but before the reconciler commits, the discovery
  poll can observe a PR/issue under the NEW slug and insert it as
  a brand new repo, leaving a transient duplicate in
  `cfg.Repositories`. Mitigation requires teaching
  `upsertDiscoveredRepos` to consult the in-flight rename set or
  to dedupe against the audit table. Tracked as a follow-up;
  current PR documents the window and accepts it because:
  (a) the discovery poll cadence (15m default) is much slower
  than the rename critical section (sub-second), and
  (b) duplicates dedupe naturally on the next reconciler tick
  once the audit row is in place.

## Re-entry checklist (post-compact)

1. `cd /Users/imunoz/Projects/ai-platform/heimdallm && git checkout main && git pull --ff-only`
2. `git checkout -b fix/issue-489-repo-org-rename`
3. Read this plan top to bottom.
4. Hot-context files to re-read:
   - `daemon/internal/store/store.go` (schema)
   - `daemon/internal/store/prs.go`, `issues.go` (UPDATE patterns)
   - `daemon/internal/config/writer.go` (AtomicWriteTOML + ReadTOMLMap)
   - `daemon/internal/config/config.go` (AIConfig / Repos map / Repositories / NonMonitored)
   - `daemon/internal/bus/watch.go` (WatchEntry / Enroll / ForceUpdate)
   - `daemon/internal/github/client.go` (response shape + APIError)
   - `daemon/internal/repoctx/manager.go` (`cloneTarget`, marker writer)
   - `daemon/cmd/heimdallm/main.go` `upsertDiscoveredRepos` + tier2 runner
5. Start with TDD step 1 (store + audit). Use `make test-docker`
   between every change. Branch: `fix/issue-489-repo-org-rename`,
   PR opens **draft**.

## Branch + PR

Branch: `fix/issue-489-repo-org-rename`. Issue #489 is self-assigned.
PR opens as draft, references `Closes #489` in the body.
