# Plan — Issue #482: review-state vigilance on auto_implement PRs

Closes `theburrowhub/heimdallm#482`. Three phases shipped together
behind opt-in flags so the bounded-cost behaviour (observation) is
always on and the unbounded-cost behaviours (auto-respond, auto-fix)
ship **off by default** with hard per-PR caps.

## Problem statement (verbatim from the issue)

Once `auto_implement` creates a PR and posts `MarkerDone`, the issue
is permanently skipped by the fetcher and the daemon has zero
visibility into what happens to that PR afterwards. The fetcher's
dedup gate (`fetcher.go:297-299`) unconditionally skips
`ActionTaken == "auto_implement" && PRCreated > 0`, and Tier 3's
state monitor only watches open→closed transitions — it never reads
the PR's review-decision state. Reviewers landing APPROVED,
COMMENTED or CHANGES_REQUESTED reviews are not surfaced and never
acted on.

## Current state — evidence

| Concern | Location | Snippet / fact |
|---|---|---|
| Issue→PR link persisted | `store/issues.go:43` (`IssueReview.PRCreated int`) | The store already records the PR number when `auto_implement` creates a PR. |
| Tier 3 `CheckItem` polls PR snapshot | `main.go:3191-3224` | `GetPRSnapshot` returns state/draft/author/updated_at/headSHA — no review-state. Closed-or-merged short-circuits. |
| Tier 3 `HandleChange` flow | `main.go:3254-3343` | Evaluates pipeline guards; on pass, runs `runReview`; re-enrolls in watch. The bot's own PRs are filtered out by `SkipReasonSelfAuthored` (`pipeline/guards.go:56-67`) so today the watch fires and is short-circuited every cycle on auto_implement PRs. |
| Watch enrollment API | `main.go:733,941,1019,1135,2798,3339` | One-liner: `watchStore.Enroll(ctx, "pr", repo, number, githubID)`. Already used six places — auto_implement does **not** call it after PR creation today. |
| `prs` schema | `store/store.go:26-38` | No external-review-state columns. Has `state` ("open"/"closed") and `dismissed` only. |
| `reviews.github_review_state` | `store/store.go:51` | Stores the **bot's own** submitted review state, not external reviewers'. Not reusable for this feature. |
| GitHub list-reviews | n/a | `client.go` has `GetPRSnapshot`, `GetPR`, `GetPRHeadSHA`, but no `GET /repos/{owner}/{repo}/pulls/{n}/reviews` wrapper. |
| Auto_implement PR creation site | `issues/pipeline.go:815-882` | After `CreatePullRequest`, the pipeline stores the PR row, posts the link-back comment with `MarkerDone`, inserts the review row. Watch enrollment goes here. |
| Circuit breaker | `store/circuitbreaker.go:59-100` | Existing `CheckCircuitBreaker(prID, repo, headSHA, limits)` with `PerPR24h` / `PerRepoHr` caps. Reusable for the response/fix caps. |

## Cost ceiling design (read first)

The issue's stated reactions for COMMENTED and CHANGES_REQUESTED have
unbounded blast radius: an enthusiastic reviewer or a misconfigured
bot can ping-pong the agent forever. Every implementation decision
below derives from these defaults:

- Phase 1 (observation) ships **always-on**. No flag. Bounded cost
  (one extra REST list-reviews call per Tier 3 tick per auto-implement
  PR, $0 in AI tokens).
- Phase 2 (auto-respond to COMMENTED) ships **off**. When on, the cap
  is **per-PR-24h**, defaults to 5, reuses the circuit-breaker table.
- Phase 3 (auto-fix CHANGES_REQUESTED) ships **off**. When on, the cap
  is **per-PR lifetime**, defaults to 3, persisted on the PR row.
- Both opt-in modes refuse to run when the latest external review's
  author is the bot itself (closes the self-loop window even without
  a CHANGES_REQUESTED guard).

## Design — three phases in one PR

### Phase 1 — Observation layer

**Schema (idempotent migration in `store/store.go:Open`):**

```go
db.Exec("ALTER TABLE prs ADD COLUMN external_review_state TEXT NOT NULL DEFAULT ''")
db.Exec("ALTER TABLE prs ADD COLUMN external_reviewer TEXT NOT NULL DEFAULT ''")
db.Exec("ALTER TABLE prs ADD COLUMN external_review_at TEXT NOT NULL DEFAULT ''")
db.Exec("ALTER TABLE prs ADD COLUMN auto_implement_issue_id INTEGER NOT NULL DEFAULT 0")
db.Exec("ALTER TABLE prs ADD COLUMN review_response_count INTEGER NOT NULL DEFAULT 0")
db.Exec("ALTER TABLE prs ADD COLUMN review_fix_count INTEGER NOT NULL DEFAULT 0")
```

`auto_implement_issue_id` is the back-link from PR to the originating
issue's store row id; populated only when `auto_implement` creates the
PR. Non-zero ⇔ "this PR was created by the daemon and is eligible for
review-state vigilance". The two counter columns are the persistent
per-PR caps for phases 2 and 3.

**Store methods:**
- `UpdatePRReviewState(prID, state, reviewer string, at time.Time) error`
- `MarkPRAutoImplementOrigin(prID, issueID int64) error`
- `IncrementPRReviewResponseCount(prID int64) (int, error)` — returns new value
- `IncrementPRReviewFixCount(prID int64) (int, error)` — returns new value
- `ListAutoImplementPRsByState(state string) ([]*PR, error)` — diagnostic

**GitHub API wrapper (`github/client.go`):**

```go
type PRReview struct {
    ID          int64
    User        User
    State       string // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
    Body        string
    SubmittedAt time.Time
}

func (c *Client) GetPRReviews(repo string, number int) ([]PRReview, error)
```

Single REST call: `GET /repos/{repo}/pulls/{number}/reviews`. Returns
the chronological list. Aggregation lives in `issues/reviewstate.go`
(new package-local helper):

```go
// LatestExternalReviewState collapses the reviews list into the
// state we display/react to. Rules:
//  - DISMISSED reviews are filtered out.
//  - "Bot's own" reviews are filtered out (author == botLogin).
//  - The remaining list is walked chronologically. For each reviewer,
//    only their latest non-COMMENTED state counts as a "decision";
//    COMMENTED contributes only when no APPROVED/CHANGES_REQUESTED
//    review exists from that reviewer yet.
//  - CHANGES_REQUESTED from any reviewer dominates APPROVED.
//  - If at least one CHANGES_REQUESTED is current, the aggregate is
//    CHANGES_REQUESTED.
//  - Else if at least one APPROVED is current, aggregate is APPROVED.
//  - Else if any COMMENTED is current, aggregate is COMMENTED.
//  - Else empty (no external reviews).
```

The "from any reviewer" wording is deliberate: this matches the
GitHub-UI `reviewDecision` concept that humans recognise.

**Auto-implement PR enrollment (`issues/pipeline.go`):**

After `CreatePullRequest` + `UpsertPR` (the existing block at lines
815-829), call:

```go
if p.watch != nil {
    if err := p.watch.Enroll(ctx, "pr", issue.Repo, prNumber, createdPR.ID); err != nil {
        slog.Warn("issues pipeline: enroll auto-implement PR in watch failed", ...)
    }
}
p.store.MarkPRAutoImplementOrigin(prRowID, issueID)
```

Inject `watch` via `Pipeline.New(... watch WatchEnroller)`. New
interface `WatchEnroller { Enroll(ctx, kind, repo, number, githubID) error }`
satisfied by the existing `*scheduler.WatchStore`. Tests pass a
fake.

**Tier 3 CheckItem extension (`main.go:3191`):**

After the closed/merged short-circuit and before the standard
"changed since LastSeen" check, query the store for the PR row. If
`auto_implement_issue_id != 0`, run a second branch:

```go
stored, _ := a.store.GetPRByGithubID(item.GithubID)
if stored != nil && stored.AutoImplementIssueID != 0 {
    state, reviewer, at, err := a.reviewStateChecker.Check(item.Repo, item.Number)
    if err != nil { /* slog warn, fall through */ }
    if state != stored.ExternalReviewState {
        a.store.UpdatePRReviewState(stored.ID, state, reviewer, at)
        a.broker.Publish(sse.Event{
            Type: sse.EventPRReviewStateChanged,
            Data: sseData(map[string]any{
                "pr_id": stored.ID, "repo": item.Repo, "number": item.Number,
                "state": state, "reviewer": reviewer,
                "prev_state": stored.ExternalReviewState,
            }),
        })
        return true, &snap, nil // let HandleChange react
    }
    return false, nil, nil  // no state change, no review run
}
// fall through to existing not-changed return
```

We short-circuit BEFORE the standard review-run path so the daemon's
own PRs never get fed to the PR-review pipeline through this
codepath (the `SkipReasonSelfAuthored` guard would block it anyway,
but we want a clean separate flow).

**New SSE constant:**

```go
const EventPRReviewStateChanged = "pr_review_state_changed"
```

**Tier 3 HandleChange extension (`main.go:3254`):**

A new branch keyed on `auto_implement_issue_id != 0`. Phase 1 just
logs and returns nil. Phases 2/3 plug in here.

**Flutter (Phase 1 UI):**

Issue tile + issue detail show a small chip when the issue has
`latest_review.action_taken == "auto_implement"` AND the linked PR has
a non-empty `external_review_state`:
- `APPROVED` → green "PR Approved"
- `CHANGES_REQUESTED` → red "Changes Requested"
- `COMMENTED` → blue "PR Commented"

Wire into the existing `tracked_issue.dart` model: the daemon's
`GET /issues/:id` response should join the linked PR's
`external_review_state`. Backend change in `server/handlers.go`
(extend the issue-detail JSON to embed the PR's external state).

### Phase 2 — COMMENTED auto-response (opt-in, capped)

**Config (`config/config.go`):**

New nested struct under `ai`:

```toml
[ai.review_response]
enabled        = false   # default off
per_pr_24h     = 5       # max responses per PR per 24h
cooldown_secs  = 300     # min seconds between responses on the same PR
```

The default values are wired through `ApplyDefaults`. Zero or
negative falls back to the default. Per-repo overrides via
`ai.repos.<repo>.review_response.*` are out of scope (follow-up if
needed; defaults are global).

**New module `daemon/internal/issues/respond.go`:**

```go
type Responder struct {
    store  responderStore
    gh     responderGH
    exec   executor.Runner
    broker eventPublisher
    cfg    func() config.ReviewResponseConfig
    botLogin func() string
}

func (r *Responder) Run(ctx context.Context, pr *store.PR, originIssueID int64) error
```

Behaviour:

1. Read `cfg().Enabled` — if false, return nil. (Phase 2 is opt-in.)
2. Fetch the PR's issue comments (the conversation thread, not the
   line-comments). `gh.FetchPRConversation(repo, number)` — reuses
   the existing `FetchIssueCommentsOnly` since GitHub treats PR
   comments through the `/issues/{n}/comments` endpoint.
3. Find the latest external comment (author != bot). If none unread
   since the last bot post, return nil. "Read" tracked by
   `prs.last_responded_comment_at` (new column? actually we can
   piggyback on `external_review_at` — the COMMENTED state's
   `submitted_at` is the trigger; we record the comment ID we
   responded to in `external_reviewer` field's place, but that
   conflates… ok, new column: `last_responded_at`).
4. Per-PR cap check: `store.IncrementPRReviewResponseCount(pr.ID)`
   atomically returns the new count. If `count > per_pr_24h`, revert
   the increment, emit `EventReviewError` with
   `reason: "review_response_cap_exceeded"`, log, return.
5. Cooldown check: if `now - last_responded_at < cooldown_secs`,
   return nil (skip this tick).
6. Build prompt: original issue title + body (truncated) + PR diff
   header + the unread comment(s) since `last_responded_at`. All
   external text wrapped in the existing `sanitiseUntrustedFreeText`
   fences (#478).
7. Run agent with `executor.Runner` in **review-only mode** (no
   write tool): use the same prompt skeleton as triage but ask for
   a single conversational comment back. **No Edit/Write tool.**
8. Post the comment via `gh.PostComment(repo, number, body)`.
9. Update `prs.last_responded_at`.
10. Emit `EventIssueReviewCompleted` with
    `{"mode": "review_response"}` so the UI shows progress.

Triggered from Tier 3 `HandleChange` when the new
`external_review_state` is `COMMENTED`.

### Phase 3 — CHANGES_REQUESTED auto-fix (opt-in, hard lifetime cap)

**Config:**

```toml
[ai.review_fix]
enabled            = false   # default off
per_pr_lifetime    = 3       # max fix runs per PR forever
cooldown_secs      = 300
```

**Reuses `issues/pipeline.go` machinery with two changes:**

1. `Pipeline.runAutoImplementOnExistingBranch(...)` — a new method
   that checks out the PR's `headSHA`, runs the agent with a
   "you are addressing the following review feedback" prompt, then
   commits + pushes to the **same branch** (no `CreatePullRequest`).
2. Lifetime cap via `IncrementPRReviewFixCount`. If `count > per_pr_lifetime`,
   emit `EventIssueReviewError` with
   `reason: "review_fix_cap_exceeded"` and stop.

Triggered from Tier 3 `HandleChange` when the new state is
`CHANGES_REQUESTED`.

**Prompt scaffolding (`issues/prompt.go`):**

A new builder `BuildReviewFixPrompt(issue, pr, reviews, comments)`
that:
- Quotes the original issue body, the original auto_implement diff
  (truncated), the latest CHANGES_REQUESTED review's body, and any
  line-comments on the PR.
- All external content sanitised via `sanitiseUntrustedFreeText`.
- Tells the agent: "examine the requested changes; if they are
  in-scope and valid, apply them and commit. If they are out-of-scope
  or wrong, leave the working tree unchanged."
- Same "leave-untouched" escape hatch as auto_implement, so we land
  in `autoImplementNoChangesFallback` semantics for the existing
  fallback comment (now landing with `MarkerDone`, terminating the
  fix-loop cleanly).

**Re-arm logic:**

After a successful push, set `external_review_state = "fix_pushed"`
on the PR row. The next Tier 3 tick will compare against the live
reviews-list: if a reviewer submits a NEW CHANGES_REQUESTED after
seeing the fix, the state flips back and the cycle can repeat (until
the lifetime cap). If no new review appears, `external_review_state`
stays "fix_pushed" forever and we don't loop.

## Tests

**Phase 1 — observation:**

| Test | What it pins |
|---|---|
| `TestStore_PRReviewState_RoundTrip` | UpdatePRReviewState writes & GetPRByGithubID reads back state + reviewer + at. |
| `TestStore_MarkPRAutoImplementOrigin` | After Mark, the PR row carries the issue id. |
| `TestStore_IncrementPRReviewResponseCount_Atomic` | Concurrent increments produce sequential counts (sqlite single-writer is fine, the test is sequential but asserts return values). |
| `TestGitHub_GetPRReviews_ParsesFields` | Stubbed HTTP roundtripper returning a canned reviews JSON parses into `[]PRReview` with state/user/submitted_at populated. |
| `TestLatestExternalReviewState_ChangesRequestedDominates` | A list with one APPROVED and one CHANGES_REQUESTED returns CHANGES_REQUESTED. |
| `TestLatestExternalReviewState_BotReviewsIgnored` | A review whose author == bot login is filtered out. |
| `TestLatestExternalReviewState_DismissedIgnored` | DISMISSED reviews don't count. |
| `TestPipeline_AutoImplement_EnrollsPRInWatch` | After `auto_implement` creates a PR, the fake WatchEnroller saw `Enroll("pr", repo, number, githubID)`. |
| `TestPipeline_AutoImplement_MarksPRAutoImplementOrigin` | After PR creation, the store row carries the issue-id back-link. |
| `TestTier3CheckItem_AutoImplementPR_DetectsStateChange` | When the fake reviewStateChecker returns CHANGES_REQUESTED and the stored value is empty, UpdatePRReviewState is called and EventPRReviewStateChanged is published. |
| `TestTier3CheckItem_AutoImplementPR_NoStateChange_NoEvent` | Same flow but state matches stored → no SSE, no UpdatePR. |

**Phase 2 — auto-respond:**

| Test | What it pins |
|---|---|
| `TestResponder_DisabledByDefault_NoOp` | `Run` is a no-op when cfg.Enabled = false. |
| `TestResponder_PerPR24hCap` | After 5 successful runs (default), the 6th emits review_error and does not call the executor. |
| `TestResponder_CooldownRespected` | A run within `cooldown_secs` of `last_responded_at` returns nil without invoking the executor. |
| `TestResponder_SkipsWhenLatestCommentIsBot` | If the latest comment author == bot login, no response. |
| `TestResponder_NoWriteTool` | The executor sees only review-only tools (no Edit/Write); regression pin for prompt injection on conversational replies. |
| `TestResponder_SanitisesExternalText` | Untrusted comment body passes through `sanitiseUntrustedFreeText`. |

**Phase 3 — auto-fix:**

| Test | What it pins |
|---|---|
| `TestFixRunner_DisabledByDefault_NoOp` | Off by default. |
| `TestFixRunner_LifetimeCap` | After `per_pr_lifetime` runs, further triggers emit review_error. |
| `TestFixRunner_CheckoutsPRHeadBranch_NotMain` | The git fake observes `Checkout(prHead)`, not `Checkout(defaultBranch)`. |
| `TestFixRunner_SkipsWhenLatestReviewIsBot` | If the latest CHANGES_REQUESTED's author == bot, no run. |
| `TestFixRunner_NoChangesFallback_PostsDoneMarker` | Reuses `autoImplementNoChangesFallback`; final body carries `MarkerDone`. |
| `TestFixRunner_AfterSuccessfulPush_StateSetsFixPushed` | Re-arm pin: state ends as `"fix_pushed"` so the next Tier 3 tick compares against new reviews. |

**Tier 3 integration:**

| Test | What it pins |
|---|---|
| `TestTier3HandleChange_Commented_RoutesToResponder` | When state goes to COMMENTED and the Responder fake is wired, HandleChange invokes it. |
| `TestTier3HandleChange_ChangesRequested_RoutesToFixRunner` | Same for fix runner. |
| `TestTier3HandleChange_Approved_NoAction` | APPROVED routes nowhere (just logs). |
| `TestTier3HandleChange_NonAutoImplementPR_FallsThroughToStandardReview` | A regular PR (no `auto_implement_issue_id`) goes through the existing `runReview` path, unchanged. |

## Implementation order (TDD)

1. **Phase 1 store + GH wrapper.** Migration, store methods, GH list-reviews, aggregator. RED+GREEN per test above. No integration yet.
2. **Phase 1 pipeline hook.** Inject `WatchEnroller`, call `MarkPRAutoImplementOrigin` post-create, enrol in watch. RED+GREEN.
3. **Phase 1 Tier 3.** CheckItem extension + SSE event. RED+GREEN.
4. **Phase 1 server JSON.** Extend the issue-detail endpoint to embed PR's external state.
5. **Phase 1 Flutter chip.** Read the new field, render in `tracked_issue.dart` consumers.
6. **Phase 2 module.** `respond.go` skeleton + caps + cooldown. RED+GREEN with the executor mocked.
7. **Phase 2 Tier 3 wire.** HandleChange routes COMMENTED to Responder when enabled.
8. **Phase 3 module.** `fix.go` reusing pipeline checkout/commit/push primitives. RED+GREEN.
9. **Phase 3 Tier 3 wire.** HandleChange routes CHANGES_REQUESTED to FixRunner when enabled.
10. **Docs.** `configuration-guide.md`: new section "Review-state vigilance on auto-implement PRs" near the existing auto_implement notes. List the new config keys, the always-on observation behaviour, the per-PR caps, the explicit opt-in for phases 2 and 3.

After each step run `make test-docker` and `flutter analyze`.

## Risks

| Risk | Mitigation |
|---|---|
| Self-loop (bot's own push triggers re-review) | `SkipReasonSelfAuthored` already filters the standard PR-review pipeline; our new flow runs on `auto_implement_issue_id != 0` only, and explicitly checks `latest_review.author != botLogin` before running fix/respond. |
| Reviewer leaves 20 line-comments on one tick | The aggregator collapses to a single state per tick; respond/fix are idempotent per-tick (one trigger → one run, gated by cooldown). |
| New GH API call cost | `GetPRReviews` runs only on items where `auto_implement_issue_id != 0`. The vast majority of watched PRs are non-auto-implement and skip this call. |
| Flutter chip placement clashes with existing badges | The new chip rides next to the existing severity badge in the issue tile; an `AttentionBadge`-style helper keeps the styling consistent. |
| Schema migration runs on existing DBs | All ALTERs are idempotent `ADD COLUMN ... DEFAULT '...'`; modernc/sqlite ignores "duplicate column" errors via `db.Exec` returning err but no panic. Mirrors the existing pattern at `store.go:160-180`. |
| Phase 2/3 default-on by accident in production | The `Enabled bool` field on each config struct defaults to `false`. `ApplyDefaults` does not flip it. Tests pin the disabled-by-default behaviour. |
| Reviewer can game caps by force-pushing review state | The lifetime cap is `review_fix_count >= per_pr_lifetime`; once tripped, no amount of new reviews can re-arm without manual intervention (operator zeroes the column, or dismisses the PR). |
| Branch checkout for fix mode races concurrent auto_implement on same repo | Reuses the existing `repoctx` worktree manager (per-execution worktrees, #461), so concurrent runs are isolated. |

## Out of scope (follow-ups)

- Per-repo overrides for `review_response` / `review_fix` configs.
- Responding to PR line-comments individually (we collapse to a
  single conversational reply per tick).
- Dismissing or requesting-re-review on the reviewer side after a fix
  pushes (GitHub API supports it; not in this PR).
- Backfilling `auto_implement_issue_id` for PRs created before this
  PR landed (rolling forward only).
- Surfacing the new caps in the GUI as configurable knobs (operators
  edit TOML for now).

## Re-entry checklist (post-compact)

1. `cd /Users/imunoz/Projects/ai-platform/heimdallm && git checkout main && git pull --ff-only`
2. `git checkout -b feat/issue-482-pr-review-state-vigilance`
3. Read this plan top to bottom.
4. Hot-context files to re-read:
   - `daemon/internal/store/store.go` (schema + migrations 150-200)
   - `daemon/internal/store/prs.go` (the PR struct)
   - `daemon/internal/store/circuitbreaker.go` (cap pattern)
   - `daemon/internal/github/client.go` lines 520-600 (GetPRSnapshot pattern)
   - `daemon/internal/issues/pipeline.go` lines 815-882 (PR creation block)
   - `daemon/cmd/heimdallm/main.go` lines 3180-3350 (Tier 3 CheckItem + HandleChange)
   - `daemon/internal/scheduler/types.go` 125-135 (Tier3ItemChecker interface)
5. Start with TDD step 1 (store + GH wrapper). Use `make test-docker` after every change.
6. Branch name: `feat/issue-482-pr-review-state-vigilance`. PR opens **draft**.
