# Web UI Routes — Design Spec

**Date**: 2026-04-17
**Issue**: [#31](https://github.com/theburrowhub/heimdallm/issues/31) — *feat: web UI — rutas Dashboard, PRs e Issues*
**Depends on**: [#30](https://github.com/theburrowhub/heimdallm/issues/30) → [PR #60](https://github.com/theburrowhub/heimdallm/pull/60) (`feat/web-ui-scaffold`, open)
**Scope**: Five SvelteKit routes (`/`, `/prs`, `/prs/{id}`, `/issues`, `/issues/{id}`) on top of the #30 scaffold, plus the Fase-2 type-parity fixes #60 explicitly defers to #31.

---

## 1. Dependency strategy

PR #60 is open at the time of this spec. Rather than wait for it to merge or duplicate the scaffold, this work **stacks on top of #60**:

- Branch: `feat/web-ui-routes` (name subject to final naming convention), forked from `origin/feat/web-ui-scaffold`.
- GitHub PR base: `feat/web-ui-scaffold` — keeps the #31 diff focused on #31's work only.
- When #60 merges to `main`, GitHub retargets the PR automatically. If retargeting fails, a single `git rebase origin/main` resolves it.

This matches the stacked-PR pattern the repo already uses (e.g. PR #59 based on PR #56).

## 2. Inherited follow-ups from #60

PR #60's description explicitly defers these to #31:

1. **Fase-2 type parity**:
   - `types.ts` currently models `Issue.assignees` / `Issue.labels` as `string[]`, but the daemon serializes them as JSON-encoded strings on the wire (same shape Dart handles with `_parseReviewMap` for review findings). Either parse client-side in `api.ts`, or — preferred — confirm the daemon's issue handler already unwraps them (per the issue-tracking-UI spec, §2.4 decision: "Parse in the daemon handler"). Implementation step should verify the daemon's actual wire output and adjust.
   - `SseEventType` union omits `issue_detected`, `issue_review_started`, `issue_review_completed`, `issue_review_error`. Add them.
2. **`sse.ts` `KNOWN_EVENT_TYPES`** — register the four new issue events so the `EventSource` listener fires.

These changes are pre-requisites for the Dashboard's unified feed and the `/issues` routes; folding them into #31 avoids a preliminary "tighten types" PR.

## 3. Architecture overview

All routes are **client-rendered**. No SvelteKit `load` functions, no SSR beyond what the adapter-node default provides. This matches the scaffold's `/+page.svelte` pattern (`onMount → fetchStats()`) and avoids introducing SSR to an auth-gated internal tool where SSR offers no user benefit.

Data flow:

```
 ┌─────────────────────────────────────────────────────┐
 │  +layout.svelte                                      │
 │    connectEvents() → EventSource → events store      │
 │    initSseBridge(events) → store effects (below)     │
 └─────────────────────────────────────────────────────┘
                    │
                    ▼
 ┌──────────────────────────────┐         ┌───────────────────────┐
 │  stores.ts                   │◀────────│  SSE bridge           │
 │    prListRefresh: writable   │  bumps  │  pr_detected → +1     │
 │    issueListRefresh          │         │  review_started → add │
 │    reviewingPRs: Set<number> │         │  review_completed →   │
 │    reviewingIssues           │         │    delete + bump      │
 └──────────────────────────────┘         │  review_error → del   │
                    │                     │  (issue equivalents)  │
                    ▼                     └───────────────────────┘
 ┌──────────────────────────────────────────────────────────────┐
 │  Route +page.svelte                                            │
 │    $effect(() => { $prListRefresh; fetchPRs().then(...) })     │
 └──────────────────────────────────────────────────────────────┘
```

The refresh-counter pattern mirrors Flutter's `prListRefreshProvider` (see `flutter_app/lib/features/dashboard/dashboard_providers.dart`) so Svelte and Dart implementations stay conceptually aligned.

## 4. Stores & SSE bridge

Extend `src/lib/stores.ts` (scaffold already has `auth`):

```ts
export const prListRefresh = writable(0);
export const issueListRefresh = writable(0);
export const reviewingPRs = writable<Set<number>>(new Set());
export const reviewingIssues = writable<Set<number>>(new Set());
```

New file `src/lib/sseBridge.ts`:

```ts
export function initSseBridge(events: Readable<SseEvent | null>) {
  events.subscribe((e) => {
    if (!e) return;
    switch (e.type) {
      case 'pr_detected':        prListRefresh.update(n => n + 1); break;
      case 'review_started':     reviewingPRs.update(s => { s.add(e.data.pr_id); return new Set(s); }); break;
      case 'review_completed':   reviewingPRs.update(s => { s.delete(e.data.pr_id); return new Set(s); });
                                 prListRefresh.update(n => n + 1); break;
      case 'review_error':       reviewingPRs.update(s => { s.delete(e.data.pr_id); return new Set(s); }); break;
      case 'issue_detected':     issueListRefresh.update(n => n + 1); break;
      case 'issue_review_started':   reviewingIssues.update(s => { s.add(e.data.issue_id); return new Set(s); }); break;
      case 'issue_review_completed': reviewingIssues.update(s => { s.delete(e.data.issue_id); return new Set(s); });
                                     issueListRefresh.update(n => n + 1); break;
      case 'issue_review_error':     reviewingIssues.update(s => { s.delete(e.data.issue_id); return new Set(s); }); break;
    }
  });
}
```

Called once from `+layout.svelte`:

```svelte
onMount(() => {
  sse = connectEvents();
  connUnsub = sse.connected.subscribe(v => connected.set(v));
  initSseBridge(sse.events);
});
```

Per-route fetch idiom (Svelte 5 runes):

```svelte
<script lang="ts">
  import { prListRefresh } from '$lib/stores';
  let prs = $state<PR[]>([]);
  let err = $state<string | null>(null);
  $effect(() => {
    $prListRefresh;
    fetchPRs().then(r => prs = r).catch(e => err = String(e));
  });
</script>
```

## 5. Routes

### 5.1 `/` — Dashboard

File: `src/routes/+page.svelte` (replaces scaffold placeholder).

**Vertical layout:**

1. **Header row** — `<h1>Dashboard</h1>` + `<ConnectionPill />`.
2. **Stats band** — four cards (`total_reviews`, `avg_issues_per_review`, `review_timing.median_seconds`, high-severity count from `by_severity.HIGH`). Extracted to `<StatsCards stats={stats} />`.
3. **Sort toggle** — "Priority" / "Newest" pill pair. Choice stored in `dashboardSort` (localStorage-backed, default `priority`).
4. **Three collapsible sections**:
   - **My Reviews** — `prs.filter(p => p.author.toLowerCase() !== me.login.toLowerCase() && !p.dismissed)`
   - **My PRs** — `prs.filter(p => p.author.toLowerCase() === me.login.toLowerCase() && !p.dismissed)`
   - **Tracked Issues** — `issues.filter(i => !i.dismissed)`

   Each uses `<CollapseHeader title count expanded onToggle />` followed by the tile list when expanded. Expansion state per section persisted via localStorage keys `dashboard.reviewsExpanded`, `.prsExpanded`, `.issuesExpanded` (default `true`).
5. **Empty state** — "No activity yet" centered when both lists are empty.

**Sort semantics** (matching Flutter):

- *Priority*: primary sort by `latest_review?.severity` (critical → high → medium → low → none), tiebreak by `updated_at` desc.
- *Newest*: `updated_at` desc.

**Data**: `fetchPRs()`, `fetchIssues()`, `fetchStats()`, `fetchMe()`. All four re-run on their respective refresh-counter bumps (stats doesn't strictly need a counter; it re-runs on `prListRefresh` bumps as a proxy).

### 5.2 `/prs` — PR list

File: `src/routes/prs/+page.svelte`.

- **Filters** read from `$page.url.searchParams`: `repo`, `severity`, `state` (default `open`, options `open|closed|all`).
- **Filter UI** in `<FilterBar />`:
  - `repo` — select populated from the unique set of `pr.repo` in the fetched list.
  - `severity` — select with enum `[critical, high, medium, low, any]`.
  - `state` — select `[open, closed, all]`.
  - Changing a value calls `goto('?' + params.toString(), { keepFocus: true, replaceState: true })`.
- **List**: `<PRTile pr={pr} />` rows, sorted `updated_at` desc. Dismissed PRs are hidden (filtered by the daemon handler by default).
- **Click tile** → `goto('/prs/{id}')`.
- **Reviewing indicator**: small spinner next to page title when `$reviewingPRs.size > 0`.

### 5.3 `/prs/{id}` — PR detail

File: `src/routes/prs/[id]/+page.svelte`.

Single-column layout (no two-panel; see Q5 rationale):

1. **Metadata card**: title → repo · `#number` · author · state chip · labels chips · `<a>` to GitHub.
2. **Action bar**: `"Review now"` button + `"Dismiss"` / `"Undismiss"` button (depending on `dismissed`). Buttons show inline spinner when `$reviewingPRs.has(id)`.
3. **Review history**: vertical list of `<ReviewCard variant="pr" review={r} />`. Card shows summary, severity badge, findings (collapsible if >5), suggestions list, relative timestamp.

**Actions**:

- *Review now* → optimistic `reviewingPRs.add(id)` → `triggerReview(id)` → SSE `review_started` (already added, no-op) → `review_completed` clears the set and bumps `prListRefresh`, causing the review history to refetch.
- *Dismiss* → `dismissPR(id)` → on success, `goto('/prs')` (back to list).
- *Undismiss* → `undismissPR(id)` → refetch detail.

**Data**: `fetchPR(id)` → `{ pr, reviews }`. Re-runs on `$prListRefresh` bump.

### 5.4 `/issues` — Issue list

File: `src/routes/issues/+page.svelte`.

- **Filters** via query params: `repo`, `severity`, `mode` (values `review_only | auto_implement | all`, default `all`).
- **`mode` filter** derives from the issue's `labels` array — presence of `auto_implement` or `review_only` label. No wire-format change needed.
- **List**: `<IssueTile issue={issue} />` rows. Default sort: `updated_at` desc if present, fall back to `fetched_at` desc.
- **Tile content**: title · repo · `#number` · author · labels chips (with `auto_implement` / `review_only` visually distinguished — colored left-border or dedicated chip style) · severity badge from `latest_review?.triage.severity` or `PENDING`.

### 5.5 `/issues/{id}` — Issue detail

File: `src/routes/issues/[id]/+page.svelte`.

Same single-column structure as `/prs/{id}`:

1. **Metadata card**: title · repo · `#number` · author · labels chips · assignees · state chip · GitHub link. **Conditional "View created PR"** link: shown when any review has `action_taken === 'auto_implement'` and `pr_created > 0`; links to `/prs/{pr_created}`.
2. **Action bar**: `"Review now"` + `"Dismiss"` / `"Undismiss"`.
3. **Review history**: `<ReviewCard variant="issue" review={r} />`. Card shows summary, triage block (severity / category / suggested_assignee), suggestions list.

**Data**: `fetchIssue(id)` → `{ issue, reviews }`. Re-runs on `$issueListRefresh` bump.

## 6. Shared components

All in `src/lib/components/`:

| Component | Used by | Key props |
|---|---|---|
| `PRTile.svelte` | `/prs`, Dashboard (My Reviews, My PRs) | `pr: PR` |
| `IssueTile.svelte` | `/issues`, Dashboard (Tracked Issues) | `issue: Issue` |
| `SeverityBadge.svelte` | tiles, review cards | `severity: string` |
| `CollapseHeader.svelte` | Dashboard sections | `title`, `count`, `expanded`, `onToggle` |
| `FilterBar.svelte` | `/prs`, `/issues` | `filters`, `onChange`, `variant: 'pr' \| 'issue'` |
| `ConnectionPill.svelte` | `+layout`, `/` | uses `sse-connected` context |
| `StatsCards.svelte` | `/` | `stats: Stats` |
| `ReviewCard.svelte` | both detail pages | `review`, `variant: 'pr' \| 'issue'` |

Two utility modules:

- `src/lib/severity.ts` — `severityClass(severity): string` maps `critical → red-600 / red-100 bg`, `high → orange-600 / orange-100 bg`, `medium → amber-600 / amber-100 bg`, `low → gray-500 / gray-100 bg`, falls back to gray for unknowns.
- `src/lib/persisted.ts` — `persistedBoolean(key, default)` and `persistedString(key, default)` writable stores backed by `localStorage`, SSR-safe (no access during server render).

## 7. Types & SSE updates

Modify `src/lib/types.ts`:

```ts
export type SseEventType =
  | 'pr_detected' | 'review_started' | 'review_completed' | 'review_error'
  | 'issue_detected' | 'issue_review_started' | 'issue_review_completed' | 'issue_review_error';
```

Issue `assignees`/`labels` — implementation step verifies daemon wire format (the issue-tracking-UI spec §2.4 says daemon unwraps JSON strings; confirm this is live in main). If the daemon ships strings, add `parseIssue` in `api.ts` mirroring `parseReview`.

Modify `src/lib/sse.ts`:

```ts
const KNOWN_EVENT_TYPES: SseEventType[] = [
  'pr_detected', 'review_started', 'review_completed', 'review_error',
  'issue_detected', 'issue_review_started', 'issue_review_completed', 'issue_review_error'
];
```

## 8. Testing

Continue the scaffold's **vitest-only** pattern. No Playwright or Svelte Testing Library in this PR.

| File | Tests added | Covers |
|---|---:|---|
| `src/tests/api.test.ts` | +3 | `parseIssue` unwraps JSON-string `assignees`/`labels`; accepts already-arrays; `fetchIssues` applies it over the list. |
| `src/tests/sse.test.ts` | +4 | Each issue event parses and emits on the `events` store. |
| `src/tests/stores.test.ts` (new) | ~6 | `initSseBridge` wires every event type correctly (counter bumps + set mutations + error paths). |
| `src/tests/severity.test.ts` (new) | ~2 | Known severities return known classes; unknown falls back. |
| `src/tests/persisted.test.ts` (new) | ~3 | Default when absent, persists on write, parses existing value, SSR-safe. |

**Total net new: ~18 unit tests** on top of the scaffold's 15.

**Manual test plan** (PR description):

- [ ] `cd web_ui && npm run check` — 0 errors / 0 warnings
- [ ] `cd web_ui && npm test` — all passing
- [ ] `cd web_ui && npm run lint` — clean
- [ ] `cd web_ui && npm run build` — adapter-node output in `build/`
- [ ] Live daemon: `/` shows three collapsible sections with real PRs/issues; sort toggle and collapsed state persist across reload.
- [ ] `/prs?repo=foo&severity=high&state=open` filters the list; clearing a filter restores; URL changes are back-buttonable.
- [ ] `/prs/{id}`: "Review now" triggers a review; tile spinner clears on SSE `review_completed`; "Dismiss" removes from `/prs` list; "Undismiss" on detail page restores it.
- [ ] `/issues` + `/issues/{id}`: same acceptance as PRs; "View created PR" link appears when a review has `action_taken === 'auto_implement'` and `pr_created > 0`.
- [ ] SSE disconnect / reconnect: layout auth banner toggles correctly.

## 9. Files changed summary

**New files (18):**

- `src/lib/components/{PRTile,IssueTile,SeverityBadge,CollapseHeader,FilterBar,ConnectionPill,StatsCards,ReviewCard}.svelte`
- `src/lib/{severity,persisted,sseBridge}.ts`
- `src/routes/prs/+page.svelte`
- `src/routes/prs/[id]/+page.svelte`
- `src/routes/issues/+page.svelte`
- `src/routes/issues/[id]/+page.svelte`
- `src/tests/{stores,severity,persisted}.test.ts`

**Modified files (8):**

- `src/lib/types.ts` — expand `SseEventType`; adjust `Issue` if daemon ships JSON strings.
- `src/lib/api.ts` — add `parseIssue`, apply in issue fetchers.
- `src/lib/sse.ts` — expand `KNOWN_EVENT_TYPES`.
- `src/lib/stores.ts` — add 4 stores.
- `src/routes/+layout.svelte` — call `initSseBridge(sse.events)` after `connectEvents()`.
- `src/routes/+page.svelte` — replace placeholder with real Dashboard.
- `src/tests/api.test.ts` — +3 tests.
- `src/tests/sse.test.ts` — +4 tests.

**Unchanged (explicitly):** `src/lib/server/token.ts`, `src/routes/api/[...path]/+server.ts`, `src/routes/events/+server.ts`, `Dockerfile`, `package.json`, `svelte.config.js`, `vite.config.ts`, `eslint.config.js`.

## 10. Out of scope

- `/agents`, `/config`, `/logs` routes (→ #32 or later).
- Dark mode / theming.
- Mobile breakpoints below ~640px.
- docker-compose integration (→ #33).
- SvelteKit `load`-based SSR.
- Component-level tests (Svelte Testing Library / happy-dom) — a scaffold-level decision.
- Inline per-row Review / Dismiss buttons on list pages — detail-page-only per issue scope.

## 11. Acceptance criteria mapping

| Issue #31 AC | Evidence |
|---|---|
| Todas las rutas renderizan datos reales del daemon | `$effect` hooks call `fetchPRs`/`fetchIssues`/`fetchPR`/`fetchIssue`/`fetchStats` in every `+page.svelte`. |
| Actualizaciones en tiempo real via SSE sin recargar la página | `initSseBridge` + refresh-counter stores; list pages depend on counters in `$effect`. |
| Responsive (funciona en ventana de browser estándar) | Single-column detail layouts + Tailwind's default responsive grid on the stats band; tested at common desktop widths. Mobile-below-640px explicitly deferred. |
| Depende de: #30 | PR stacked on `feat/web-ui-scaffold`; Fase-2 type follow-ups inherited and resolved in this PR. |
