# Merge Tracking — Design

## 1. Objective

Track the open pull requests the authenticated user **authored or is assigned to**, determine exactly what is stopping each one from merging, and — at whatever level of automation the operator configures — move them along.

Four independent automations, all off by default:

1. Arm GitHub's native auto-merge on PRs that do not have it.
2. Update a branch that has fallen behind its base.
3. Resolve merge conflicts with the configured agent.
4. Merge when every requirement is met.

And one reporting requirement that carries equal weight: **when a PR cannot merge because of CI, say which check, where it cannot be missed.**

## 2. What already exists and is reused

| Existing | Reused for |
|---|---|
| `github.Client.fetchByQualifier` | The `author:`/`assignee:` search, with the same "no `repo:` filter" rule as `FetchPRsToReview` |
| `github.Client.graphQL` + `acquireGraphQL` | The readiness query, with its rate-limit breaker and separate GraphQL budget |
| `github.Client.MergePR` | Refactored to share `mergePR` with the new `MergePRAtSHA`; the autonomous gate is untouched |
| `repoctx.Manager` (`ModeWrite`) | Ephemeral worktrees for the rebase and the conflict agent |
| `issues.GitExec` | Extended with rebase/conflict plumbing; `buildAskPassEnv`, `runGit`, `procgroup` unchanged |
| `issues.CommitAll`'s sensitive-path denylist | Extracted to `enforceSensitivePathDenylist`, now shared with `StageAll` |
| `issues.SanitiseUntrustedFreeText` + fences | The conflict prompt's anti-injection hardening |
| `issues.ensureAutoImplementWritePerms` | Exported as `EnsureWritePerms`; same write posture as auto_implement |
| `workgate.Gate` | Drains merge-tracking actions during an application update |
| `config` override pattern from `autonomous.go` | `repo > org > global` resolution |
| `store` idempotent-migration pattern | The `merge_tracking` table |
| `AutonomousPoller`'s "no-op when disabled" shape | The reconciler's `AnyEnabled` guard |

## 3. What is new

- `internal/mergetrack` — the pure evaluator, the decision model, the reconciler and the conflict resolver.
- `internal/github/merge_readiness*.go` — one GraphQL query gathering everything needed to decide.
- `internal/github/merge_actions.go` — auto-merge mutations, update-branch, SHA-pinned merge, all with classified errors.
- `internal/store/merge_tracking.go` — the persisted state machine.
- `internal/config/merge_tracking.go` — the `[merge_tracking]` section.
- `internal/issues/gitops_merge.go` — rebase, conflict detection, lease-protected force-push.
- `cmd/heimdallm/merge_tracking_runner.go` — the poller and the worktree runner.
- `internal/server/merge_tracking.go` — the HTTP surface.
- Flutter `features/merge_tracking/`, CLI `merges`/`merge`, TUI Merges tab.

## 4. Architecture

```
tick (own ticker, cadence re-read each cycle)
  │
  ├─ AnyEnabled(repos)?  ── no ──▶ return.  Zero GitHub calls.
  │
  ├─ discover:  FetchMergeTrackingPRs → ∩ monitored → EnsureMergeTracking
  │
  └─ for each due row (≤ max_prs_per_tick, ordered by evaluated_at):
       GetMergeStatus  ─ 1 GraphQL query
       syncRow         ─ new head SHA? reset counters. GitHub auto-merge state wins.
       Evaluate        ─ PURE. snapshot → readiness + per-check breakdown
       Decide          ─ PURE. readiness + config + state → action
       persist         ─ decision + checks, always
       if mutating:
         ClaimMergeTrackingAction   ─ persistent single-flight
         workGate.AcquireContext
         dispatch                   ─ arm / update / rebase / resolve / merge
```

`Evaluate` and `Decide` do no I/O, which is what makes the merge rules exhaustively testable. Merge rules are exactly the kind of thing that must be.

### 4.1 Concurrency

Three layers, each covering what the others cannot:

1. **The ticker's buffered channel** — a tick that fires while the previous one runs is dropped, so the reconciler never runs concurrently with itself. Same invariant as tier 2.
2. **`ClaimMergeTrackingAction`** — a conditional SQL `UPDATE` returning `RowsAffected() == 1`. Persistent, so it survives a restart and spans two daemons pointed at one database. Anchored to the head SHA, so a claim made for one commit cannot execute against another.
3. **`workgate.KindMergeTracking`** — defers write actions while an application update drains.

A process that dies mid-action leaves a row parked in an in-flight phase. `store.Open` resets those to `idle` with a five-minute cooldown; `auto_merge_armed` is deliberately **not** reset, because GitHub holds that state and the next tick re-verifies it.

## 5. The readiness query

One GraphQL round trip per PR, plus pagination for oversized connections. It selects `mergeable`, `mergeStateStatus`, `reviewDecision`, `statusCheckRollup` with `isRequired` per context, `latestOpinionatedReviews(writersOnly: true)` with each review's `commit.oid`, `reviewThreads`, `autoMergeRequest`, `isInMergeQueue`, and `baseRef.branchProtectionRule`.

Three things about this were not obvious and cost real debugging:

**`mergeStateStatus` needs a schema preview.** `Accept: application/vnd.github.merge-info-preview+json`. `graphQL()` hardcoded `application/vnd.github+json`, so `graphQLWith(graphQLOptions{Accept: …})` was added.

**Partial errors are the normal case.** `baseRef.branchProtectionRule` requires **admin** on the repository. Without it GitHub answers with `data` populated *and* an `errors` array carrying a `FORBIDDEN` entry with a `path`. The existing `graphQL()` returned an error the moment `errors` was non-empty, discarding the data — so this query would have failed for every ordinary token. `TolerateFieldErrors` returns the decoded data plus a `*PartialGraphQLError` when **every** error names a path and is of a known field-scoped type; anything else stays fatal. Only a failure on `branchProtectionRule` is accepted; a `FORBIDDEN` on a gating field fails, because evaluating on incomplete gating data is worse than not evaluating.

**`mergeable` and `mergeStateStatus` are computed lazily.** The first read returns `UNKNOWN` and *triggers* the computation. Treating `UNKNOWN` as anything but "ask again" is precisely how an unmergeable PR gets merged.

## 6. The evaluator

`Evaluate(st *github.MergeStatus, in Input) Decision`. The order of its rules is the specification; earlier rules win.

| # | Condition | Outcome |
|---|---|---|
| 1 | `merged` | `ActionMarkMerged`, terminal — nothing below can override it |
| 2 | `state == CLOSED` | `ActionAbandon` |
| 3 | Neither author nor a tracked assignee | `ActionAbandon`, `not_tracked` |
| 4 | Excluded, or `enabled = false` | no action |
| 5 | `viewerPermission ∉ {ADMIN, MAINTAIN, WRITE}` | `insufficient_permission`, terminal |
| 6 | Draft | `draft` — tracked and displayed, never acted on |
| 7 | Head in someone else's fork | `cross_fork` — evaluated, never written to |
| 8 | In a merge queue | `in_merge_queue` — GitHub owns it |
| 9 | `mergeable`/`mergeStateStatus` `UNKNOWN` | `ActionWait`, short recheck, capped at 5 |
| 10 | `HAS_HOOKS` | `ActionWait` |
| 11 | `CONFLICTING` / `DIRTY` | `conflicts` |
| 12 | `BEHIND` | `behind_base` |
| 13 | *(accumulating)* reviews, threads, checks, merge method, `BLOCKED` | every applicable block, ordered by usefulness |

### 6.1 `mergeStateStatus`

| Value | Meaning | Treatment |
|---|---|---|
| `CLEAN` | mergeable, unblocked | evaluate the evidence |
| `UNSTABLE` | a **non-required** check is red | ready if every required check is green — GitHub merges these |
| `BEHIND` | strict checks, out of date | `update_branch` |
| `BLOCKED` | protection blocks it | refine from the evidence; fall back to `blocked_by_protection` |
| `DIRTY` | conflicts | `resolve_conflicts` |
| `DRAFT` | draft | never acted on |
| `HAS_HOOKS` | pre-receive hooks running | wait |
| `UNKNOWN` | still computing | wait, never ready |

### 6.2 Reviews

Approvals and change requests are treated **asymmetrically with respect to the head SHA**, and that asymmetry is the design:

- An `APPROVED` whose `commit.oid` is not the current head does **not** count. A push after the approval means nobody approved what would actually be merged. (#674: *"Un push posterior invalida aprobaciones"*.)
- A `CHANGES_REQUESTED` on an older commit **does** still count. GitHub keeps it active until the reviewer resolves it, and so do we.

Both fail in the same direction: towards not merging.

Two further rules, both stricter than GitHub:

- A standing `CHANGES_REQUESTED` blocks **even when `reviewDecision` is `APPROVED`** — the case where the reviewer is not a required one. This is #674's first acceptance criterion, stated in those words.
- `latestOpinionatedReviews(writersOnly: true)` plus an explicit `authorCanPushToRepository` check: an approval from someone who cannot push cannot satisfy branch protection, so counting it would be a real defect.

### 6.3 Checks

Required-ness comes from two sources and both matter. GitHub's `isRequired(pullRequestNumber:)` covers the normal case, but **a context listed in `requiredStatusCheckContexts` that never reported does not appear in the rollup at all** — no row, nothing red, nothing to notice. Those are collected into `MissingRequired` and block as hard as a failure.

`normalizeCheckState` collapses the two reporting shapes. The load-bearing rule: a check run whose `status != COMPLETED` is **pending regardless of `conclusion`**. GitHub leaves a stale conclusion in place while a check re-runs, so reading `conclusion` first would report a green check that is currently running.

`SKIPPED` and `NEUTRAL` count as green: GitHub's own gate treats them as satisfied.

### 6.4 Threads

Any unresolved, non-outdated review thread blocks — **even without `requiresConversationResolution`**. #674 asks for this explicitly. An open conversation on a PR about to merge automatically is a question nobody answered.

### 6.5 Truncation is not absence

`ChecksTruncated` / `ThreadsTruncated` make `Ready` impossible. Reporting ready on a partial read is the one failure mode this package exists to prevent.

## 7. The two-phase merge

The operator's requirement, stated verbatim: *"Primero automerge, marca la PR como que el automerge ya ha sido activada, y se queda a la espera. En la próxima vuelta comprueba, si github aun no la ha mezclado y los requisitos siguen verdes, mezcla directa."*

Implemented in `Decide` with two independent facts, which can disagree:

- **`githubArmed`** — GitHub currently has auto-merge on. The authority on whether it is on at all: someone can turn it off in the web UI, and GitHub keeps it on across pushes.
- **`rowArmed`** — we recorded arming it **for this head SHA**. This is what licences the second phase.

Promotion to a direct merge requires both, plus `AutoMergeArmedAt < TickStart` — so it can never happen in the same pass that armed it. When GitHub is armed but our record points at a different commit (after a push), the pass does nothing and the reconciler re-anchors the row; the next pass can promote. One extra cycle in a rare case, in exchange for never merging a commit on the strength of a licence granted for a different one.

Three further guards, all from the plan review:

- **`DisableAutoMerge` immediately before the direct merge.** With it still armed, GitHub could fire its own merge concurrently with ours.
- **A merge queue forbids the direct merge entirely.** A `PUT /merge` would jump the queue.
- **Full re-validation before the request** (§8).

## 8. Closing the TOCTOU window

Between the decision and the merge, a PR can gain a commit, lose an approval, enter a merge queue, or be merged by someone else. Before every direct merge:

1. `GetMergeStatus` again — a second, fresh read.
2. `merged` already → record and stop, idempotent.
3. `headRefOid` differs → block `head_sha_moved`. **Never retry.**
4. In a merge queue now → block.
5. `Evaluate` the fresh snapshot; not ready → block with the new reason.
6. Disarm auto-merge.
7. `MergePRAtSHA(..., fresh.HeadOID)` — GitHub itself refuses if the head moved inside its own window; that 409 is a block, not a retry.

## 9. Conflict resolution

The daemon owns git; the agent has exactly one job — edit the conflicted files.

```
acquire ephemeral worktree (repoctx ModeWrite, full history)
checkout the head branch          → preSHA
  abort if preSHA ≠ the SHA the decision was made against
fetch the base                    → baseSHA
rebase onto baseSHA
  clean?    → push, done
  conflict? → run the agent
guard 1: unmerged paths remain          → abort, nothing pushed
guard 2: conflict markers in the files  → abort, nothing pushed
guard 3: any file outside the conflict set changed → abort, nothing pushed
stage (sensitive-path denylist) → rebase --continue
push --force-with-lease=<branch>:<preSHA>
comment on the PR, naming preSHA
```

Guard 2 exists because an agent can "resolve" a conflict by deleting one side and leaving the markers, and git stages that without complaint — in some languages the result even compiles. Guard 3 is the cheapest possible signal that the agent did something unintended.

The lease is spelled out explicitly (`--force-with-lease=<branch>:<sha>`) rather than using the bare form, which compares against the local remote-tracking ref: a stale fetch makes the bare form wrong, and being wrong here means overwriting somebody's commits.

The audit comment names `preSHA` and prints the `git reset --hard` that undoes the resolution. That turns the worst outcome from a loss into a one-line recovery.

## 10. Configuration

Section `[merge_tracking]`, structurally a copy of `autonomous.go`: struct + pointer-field override + `MergeTrackingForRepo` + defaults. Deliberately **not** an extension of `[autonomous]`: that governs PRs the agent opened from issues, this governs the human's own, and sharing one section would mean every decision needed a flag to tell the two origins apart.

Unlike `[autonomous]`, `merge_method` **is validated** at boot. An invalid value would otherwise surface as a 422 from GitHub on every merge attempt, once per cycle, forever.

`poll_interval` and `max_prs_per_tick` are global-only: there is one reconciler loop, so a per-repo cadence has no meaning. A drift test enforces that every other field has an override counterpart, so a field added later cannot silently become non-overridable.

## 11. State and persistence

Table `merge_tracking`, keyed on `pr_id`. Not columns on `prs`: `UpsertPR` rewrites that row every poll and would have to learn to preserve each field, and the lifecycle here (per-head-SHA counters, cooldowns, an armed auto-merge) is independent.

Phases: `idle → blocked → updating | resolving | auto_merge_armed | merging → merged | abandoned`.

`decision_json` holds the full decision including every check, so the listing and the PR detail view explain a blocked merge with **no** call to GitHub. Opening the Merge tab costs nothing.

`ResetMergeTrackingForNewHead` is the mechanical expression of "a push invalidates everything": counters, cooldown, last error and the armed auto-merge are all cleared unless the arming was for exactly this SHA.

The table is registered in `store.RenameRepo`. Without that, a repo rename orphans the rows and the reconciler re-enrols the PR from scratch, losing its counters and its armed state.

## 12. Observability

SSE: `merge_track_detected`, `_evaluated`, `_blocked`, `_auto_merge_armed`, `_branch_updated`, `_conflict_resolved`, `_merged`, `_error`.

`merge_track_evaluated` fires every cycle for every PR and is **not** written to the activity log — one row per PR per cycle would drown it. `merge_track_blocked` is emitted only when the blocking reason **changes**, so a PR waiting an hour on CI produces one event, not twelve.

## 13. Surfacing check failures

The operator's second requirement: *"Si la PR no puede ser mezclada por problemas de checks, mostrar avisos evidentes en el listado, y describir los checks en el detalle de la PR, de manera muy visible y comprensible."*

The daemon's `Detail` text always **names** the check — `"1 required check is failing: build (GitHub Actions)"` — never a bare count. That single string is what every surface renders.

**Listing:** a full-width coloured band on the row, ✕/⏳ counter chips beside the phase badge, rows with check problems sorted first, and a count badge on the tab so a red check is visible from any tab.

**Detail:** a sentence in plain language derived from the counts (so it cannot drift from the rows), then the gating checks with state, app, duration and a link to the log, `Required` marked, and the optional ones collapsed so their noise cannot hide the blocker. Required contexts that never reported get their own callout.

The same sentence is produced by the Go evaluator (`Decision.Headline`), the Flutter widget and the CLI/TUI, so the three surfaces cannot drift into explaining the same state differently.

## 14. Testing

- **Evaluator** — every `mergeStateStatus`, the #674 review matrix, stale approvals, drive-by approvals, threads, truncation, merge queue, permissions, scope.
- **Decide** — the four toggles in combination, both phases of the merge, attempt caps, cooldowns.
- **Reconciler** — real in-memory store, scripted gateway: TOCTOU (exactly two fetches, zero merges when the head moved), regression before merge, idempotence, disarm-before-merge ordering, claim exclusivity, counter reset on push, and **zero GitHub calls when everything is off** (the fake fails the test on any call).
- **Conflict resolver** — each of the three guards, the branch-moved abort, prompt hardening with an injected fence terminator.
- **GitHub client** — `httptest` with inline JSON, the preview Accept header, tolerated and fatal partial errors, every error classification.
- **gitops** — real git repositories in `t.TempDir()`. The force-with-lease tests point the GitHub URL at a local bare repo via `url.insteadOf`; without that they passed on a DNS failure rather than on the lease.
- **Store** — claim exclusivity, per-commit reset, due-row filtering, rename.
- **Server** — every endpoint, auth, 503 when unwired.
- **Flutter** — the warning text, the check table's every state, the collapse, the tab badge count.

## 15. Out of scope

- Fixing failing checks. Merge tracking reports them; the review pipeline is a separate feature.
- Resolving review threads or dismissing reviews on the user's behalf.
- Writing to a PR whose head lives in someone else's fork.
- Merging past a merge queue.
- Migrating the autonomous gate's `MergePR` onto `MergePRAtSHA`. It should happen — it is the same #674 defect — but it belongs to that epic, not this PR.
