# Web UI Routes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the five SvelteKit routes specified in issue #31 (`/`, `/prs`, `/prs/{id}`, `/issues`, `/issues/{id}`) on top of the #30 scaffold, plus the Fase-2 type-parity fixes inherited from PR #60.

**Architecture:** All routes are client-rendered on top of the scaffold's SvelteKit + Svelte 5 runes + Tailwind + typed API client. SSE events drive refresh-counter stores that re-run `$effect`-based fetchers. Shared tiles, badges, and filter UI live in `src/lib/components/`. URL query params hold filter state for shareability.

**Tech Stack:** SvelteKit 2, Svelte 5 (runes), TypeScript, TailwindCSS 4, vitest + jsdom (unit tests), adapter-node (prod). Node 22 LTS.

**Design spec:** `docs/superpowers/specs/2026-04-17-web-ui-routes-design.md`
**Depends on:** PR #60 (`feat/web-ui-scaffold`, open at time of writing)

---

## Task 0: Branch setup

**Files:**
- No file changes; environment setup only

- [ ] **Step 1: Fetch and verify scaffold branch**

```bash
cd /home/vbueno/Desarrollo/workspaces/heimdallm-002
git fetch origin feat/web-ui-scaffold
git log origin/feat/web-ui-scaffold --oneline -5
```

Expected: a list of `feat(web_ui): ...` commits ending with `09604c4 fix(web_ui): share single SSE connection via layout context`.

- [ ] **Step 2: Create the stacked branch**

```bash
git checkout -b feat/web-ui-routes origin/feat/web-ui-scaffold
```

Expected: `Switched to a new branch 'feat/web-ui-routes'`. Working tree now contains `web_ui/`.

- [ ] **Step 3: Install dependencies**

```bash
cd web_ui && npm ci
```

Expected: install completes without errors. Node version ≥ 22 (check with `node --version` if it fails).

- [ ] **Step 4: Verify scaffold is green before we change anything**

```bash
cd web_ui
npm run check
npm test
npm run lint
npm run build
```

Expected: all four pass (scaffold baseline: 0 check errors, 15 tests passing, lint clean, `build/` generated).

If anything fails at this step, stop and fix the scaffold before proceeding — you do not want to debug scaffold regressions while implementing routes.

- [ ] **Step 5: Commit checkpoint (branch exists, nothing else changed)**

Nothing to commit yet — this is a read-only verification task. Move to Task 1.

---

## Task 1: Expand SseEventType union in types.ts

**Files:**
- Modify: `web_ui/src/lib/types.ts` (the `SseEventType` line)

- [ ] **Step 1: Open types.ts and locate the SSE event type union**

Read `web_ui/src/lib/types.ts`. Find:

```ts
export type SseEventType = 'pr_detected' | 'review_started' | 'review_completed' | 'review_error';
```

- [ ] **Step 2: Replace with the full union**

```ts
// SSE event types emitted by the daemon's sse.Broker. Must match the
// constants in daemon/internal/sse/broker.go exactly or listeners in
// sse.ts won't fire.
export type SseEventType =
  | 'pr_detected'
  | 'review_started'
  | 'review_completed'
  | 'review_error'
  | 'issue_detected'
  | 'issue_review_started'
  | 'issue_review_completed'
  | 'issue_review_error';
```

- [ ] **Step 3: Run the type-checker**

```bash
cd web_ui && npm run check
```

Expected: 0 errors. No downstream code references these names yet, so adding them is safe.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/lib/types.ts
git commit -m "feat(web_ui): add issue SSE event types"
```

---

## Task 2: Register issue events in sse.ts

**Files:**
- Modify: `web_ui/src/lib/sse.ts:4-9` (the `KNOWN_EVENT_TYPES` constant)
- Test: `web_ui/src/tests/sse.test.ts`

- [ ] **Step 1: Write failing tests first**

Append to `web_ui/src/tests/sse.test.ts` inside the existing `describe('connectEvents', () => { ... })` block (before the final closing brace of the describe):

```ts
it('parses and emits issue_detected events', () => {
  const { events } = connectEvents();
  MockEventSource.instances[0].fireOpen();
  MockEventSource.instances[0].emit('issue_detected', JSON.stringify({ issue_id: 42 }));
  const last = get(events);
  expect(last).toEqual({ type: 'issue_detected', data: { issue_id: 42 } });
});

it('parses and emits issue_review_started events', () => {
  const { events } = connectEvents();
  MockEventSource.instances[0].fireOpen();
  MockEventSource.instances[0].emit('issue_review_started', JSON.stringify({ issue_id: 7 }));
  expect(get(events)).toEqual({ type: 'issue_review_started', data: { issue_id: 7 } });
});

it('parses and emits issue_review_completed events', () => {
  const { events } = connectEvents();
  MockEventSource.instances[0].fireOpen();
  MockEventSource.instances[0].emit('issue_review_completed', JSON.stringify({ issue_id: 7 }));
  expect(get(events)).toEqual({ type: 'issue_review_completed', data: { issue_id: 7 } });
});

it('parses and emits issue_review_error events', () => {
  const { events } = connectEvents();
  MockEventSource.instances[0].fireOpen();
  MockEventSource.instances[0].emit('issue_review_error', JSON.stringify({ issue_id: 7, error: 'nope' }));
  expect(get(events)).toEqual({ type: 'issue_review_error', data: { issue_id: 7, error: 'nope' } });
});
```

- [ ] **Step 2: Run tests — expect the 4 new ones to fail**

```bash
cd web_ui && npx vitest run src/tests/sse.test.ts
```

Expected: 4 new tests fail (events never emit because `KNOWN_EVENT_TYPES` doesn't include them). Scaffold's existing sse tests still pass.

- [ ] **Step 3: Update `KNOWN_EVENT_TYPES` in sse.ts**

Edit `web_ui/src/lib/sse.ts`. Replace:

```ts
const KNOWN_EVENT_TYPES: SseEventType[] = [
  'pr_detected',
  'review_started',
  'review_completed',
  'review_error'
];
```

with:

```ts
const KNOWN_EVENT_TYPES: SseEventType[] = [
  'pr_detected',
  'review_started',
  'review_completed',
  'review_error',
  'issue_detected',
  'issue_review_started',
  'issue_review_completed',
  'issue_review_error'
];
```

- [ ] **Step 4: Run tests — expect all to pass**

```bash
cd web_ui && npx vitest run src/tests/sse.test.ts
```

Expected: all passing (scaffold 4 + new 4 = 8).

- [ ] **Step 5: Commit**

```bash
git add web_ui/src/lib/sse.ts web_ui/src/tests/sse.test.ts
git commit -m "feat(web_ui): register issue SSE events in KNOWN_EVENT_TYPES"
```

---

## Task 3: Add `parseIssue` for wire-format unwrapping

**Files:**
- Modify: `web_ui/src/lib/api.ts` (add helper; apply in `fetchIssues`, `fetchIssue`)
- Test: `web_ui/src/tests/api.test.ts`

**Context:** The daemon's `store.Issue` persists `assignees` and `labels` as JSON strings. The issue-tracking-UI spec (`docs/superpowers/specs/2026-04-17-issue-tracking-ui-design.md` §2.4) states the daemon handler unwraps them before serializing. We add a client-side `parseIssue` that is **tolerant of both** shapes (string or array) so a daemon that hasn't unwrapped yet still works, and so array-shaped responses are passed through untouched.

- [ ] **Step 1: Write the failing tests**

Append to `web_ui/src/tests/api.test.ts` inside the existing `describe('api.ts', () => { ... })` block:

```ts
it('fetchIssues unwraps JSON-string assignees and labels', async () => {
  fetchMock.mockResolvedValue(
    okJson([
      {
        id: 1,
        github_id: 100,
        repo: 'o/r',
        number: 1,
        title: 't',
        body: 'b',
        author: 'a',
        assignees: '["alice","bob"]',
        labels: '["bug","auto_implement"]',
        state: 'open',
        created_at: '2026-04-17T00:00:00Z',
        fetched_at: '2026-04-17T00:00:01Z',
        dismissed: false
      }
    ])
  );
  const issues = await fetchIssues();
  expect(issues[0].assignees).toEqual(['alice', 'bob']);
  expect(issues[0].labels).toEqual(['bug', 'auto_implement']);
});

it('fetchIssues passes through already-array assignees and labels', async () => {
  fetchMock.mockResolvedValue(
    okJson([
      {
        id: 1,
        assignees: ['alice'],
        labels: ['bug'],
        // remaining fields elided — parseIssue must not fail on partial input
      }
    ])
  );
  const issues = await fetchIssues();
  expect(issues[0].assignees).toEqual(['alice']);
  expect(issues[0].labels).toEqual(['bug']);
});

it('fetchIssue unwraps the same fields on the nested issue and its latest_review', async () => {
  fetchMock.mockResolvedValue(
    okJson({
      issue: {
        id: 1,
        assignees: '[]',
        labels: '["review_only"]',
        latest_review: {
          id: 9,
          issue_id: 1,
          triage: '{"severity":"high"}',
          suggestions: '["do the thing"]'
        }
      },
      reviews: [
        {
          id: 9,
          issue_id: 1,
          triage: '{"severity":"high"}',
          suggestions: '["do the thing"]'
        }
      ]
    })
  );
  const detail = await fetchIssue(1);
  expect(detail.issue.assignees).toEqual([]);
  expect(detail.issue.labels).toEqual(['review_only']);
  expect(detail.reviews[0].triage).toEqual({ severity: 'high' });
  expect(detail.reviews[0].suggestions).toEqual(['do the thing']);
});
```

Also, at the top of `api.test.ts` extend the import line to include the issue fetchers:

```ts
import { ApiError, fetchIssue, fetchIssues, fetchPR, fetchPRs, fetchStats, triggerReview, upsertAgent } from '../lib/api.js';
```

- [ ] **Step 2: Run tests — expect the 3 new ones to fail**

```bash
cd web_ui && npx vitest run src/tests/api.test.ts
```

Expected: 3 new tests fail (fields remain as strings or objects).

- [ ] **Step 3: Add `parseIssue` and `parseIssueReview` helpers in api.ts**

In `web_ui/src/lib/api.ts`, just after the existing `parsePR` function, add:

```ts
// The daemon's issue review stores `triage` and `suggestions` as JSON-encoded
// strings; the issue itself stores `assignees` and `labels` the same way.
// Handlers may or may not unwrap them depending on version. These helpers
// accept both shapes (string or already-parsed) so callers always receive
// typed values.

function parseMaybeJson<T>(raw: unknown, fallback: T): T {
  if (typeof raw === 'string') {
    return raw ? (JSON.parse(raw) as T) : fallback;
  }
  if (raw == null) return fallback;
  return raw as T;
}

function parseIssueReview(raw: unknown): IssueReview {
  const r = { ...(raw as Record<string, unknown>) };
  r.triage = parseMaybeJson(r.triage, {});
  r.suggestions = parseMaybeJson(r.suggestions, []);
  return r as unknown as IssueReview;
}

function parseIssue(raw: unknown): Issue {
  const i = { ...(raw as Record<string, unknown>) };
  i.assignees = parseMaybeJson<string[]>(i.assignees, []);
  i.labels = parseMaybeJson<string[]>(i.labels, []);
  if (i.latest_review) i.latest_review = parseIssueReview(i.latest_review);
  return i as unknown as Issue;
}
```

- [ ] **Step 4: Apply in `fetchIssues` and `fetchIssue`**

Replace:

```ts
export function fetchIssues(): Promise<Issue[]> {
  return request<Issue[]>('GET', '/api/issues');
}

export function fetchIssue(id: number): Promise<IssueDetail> {
  return request<IssueDetail>('GET', `/api/issues/${id}`);
}
```

with:

```ts
export async function fetchIssues(): Promise<Issue[]> {
  const raws = await request<unknown[]>('GET', '/api/issues');
  return raws.map(parseIssue);
}

export async function fetchIssue(id: number): Promise<IssueDetail> {
  const raw = await request<{ issue: unknown; reviews?: unknown[] }>('GET', `/api/issues/${id}`);
  return {
    issue: parseIssue(raw.issue),
    reviews: (raw.reviews ?? []).map(parseIssueReview)
  };
}
```

- [ ] **Step 5: Run tests — expect all to pass**

```bash
cd web_ui && npx vitest run src/tests/api.test.ts
```

Expected: all passing (scaffold 6 + new 3 = 9).

- [ ] **Step 6: Run the full type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 7: Commit**

```bash
git add web_ui/src/lib/api.ts web_ui/src/tests/api.test.ts
git commit -m "feat(web_ui): parse issue wire-format JSON strings client-side"
```

---

## Task 4: Extend stores.ts with refresh counters and reviewing sets

**Files:**
- Modify: `web_ui/src/lib/stores.ts`

- [ ] **Step 1: Append to stores.ts**

Append to `web_ui/src/lib/stores.ts`:

```ts
// Refresh counters — incrementing these forces list fetchers to re-run.
// Used by the SSE bridge (see sseBridge.ts) and by any page that needs
// to invalidate after a mutation.
export const prListRefresh = writable(0);
export const issueListRefresh = writable(0);

// In-flight review trackers. A PR id is present while a review is
// running, so tiles and buttons can show a spinner.
export const reviewingPRs = writable<Set<number>>(new Set());
export const reviewingIssues = writable<Set<number>>(new Set());
```

- [ ] **Step 2: Run type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Commit**

```bash
git add web_ui/src/lib/stores.ts
git commit -m "feat(web_ui): add refresh counters and reviewing sets"
```

---

## Task 5: Create `sseBridge.ts` with TDD

**Files:**
- Create: `web_ui/src/lib/sseBridge.ts`
- Test: `web_ui/src/tests/stores.test.ts`

- [ ] **Step 1: Write failing tests**

Create `web_ui/src/tests/stores.test.ts`:

```ts
import { get, writable } from 'svelte/store';
import { beforeEach, describe, expect, it } from 'vitest';
import {
  issueListRefresh,
  prListRefresh,
  reviewingIssues,
  reviewingPRs
} from '../lib/stores.js';
import { initSseBridge } from '../lib/sseBridge.js';
import type { SseEvent } from '../lib/types.js';

function makeEvents() {
  return writable<SseEvent | null>(null);
}

beforeEach(() => {
  prListRefresh.set(0);
  issueListRefresh.set(0);
  reviewingPRs.set(new Set());
  reviewingIssues.set(new Set());
});

describe('initSseBridge', () => {
  it('increments prListRefresh on pr_detected', () => {
    const events = makeEvents();
    initSseBridge({ subscribe: events.subscribe });
    events.set({ type: 'pr_detected', data: {} });
    expect(get(prListRefresh)).toBe(1);
  });

  it('adds pr_id to reviewingPRs on review_started', () => {
    const events = makeEvents();
    initSseBridge({ subscribe: events.subscribe });
    events.set({ type: 'review_started', data: { pr_id: 42 } });
    expect(get(reviewingPRs).has(42)).toBe(true);
  });

  it('removes pr_id and bumps prListRefresh on review_completed', () => {
    const events = makeEvents();
    initSseBridge({ subscribe: events.subscribe });
    events.set({ type: 'review_started', data: { pr_id: 42 } });
    events.set({ type: 'review_completed', data: { pr_id: 42 } });
    expect(get(reviewingPRs).has(42)).toBe(false);
    expect(get(prListRefresh)).toBe(1);
  });

  it('removes pr_id without bumping on review_error', () => {
    const events = makeEvents();
    initSseBridge({ subscribe: events.subscribe });
    events.set({ type: 'review_started', data: { pr_id: 42 } });
    events.set({ type: 'review_error', data: { pr_id: 42 } });
    expect(get(reviewingPRs).has(42)).toBe(false);
    expect(get(prListRefresh)).toBe(0);
  });

  it('handles all four issue review events symmetrically', () => {
    const events = makeEvents();
    initSseBridge({ subscribe: events.subscribe });
    events.set({ type: 'issue_detected', data: {} });
    expect(get(issueListRefresh)).toBe(1);

    events.set({ type: 'issue_review_started', data: { issue_id: 7 } });
    expect(get(reviewingIssues).has(7)).toBe(true);

    events.set({ type: 'issue_review_completed', data: { issue_id: 7 } });
    expect(get(reviewingIssues).has(7)).toBe(false);
    expect(get(issueListRefresh)).toBe(2);

    events.set({ type: 'issue_review_started', data: { issue_id: 8 } });
    events.set({ type: 'issue_review_error', data: { issue_id: 8 } });
    expect(get(reviewingIssues).has(8)).toBe(false);
  });

  it('bumps both refresh counters and clears reviewingIssues on issue_implemented', () => {
    const events = makeEvents();
    initSseBridge({ subscribe: events.subscribe });

    // Simulate the auto_implement sequence: started → implemented (no
    // review_completed in between — that's the daemon's actual behavior).
    events.set({ type: 'issue_review_started', data: { issue_id: 7 } });
    expect(get(reviewingIssues).has(7)).toBe(true);

    events.set({
      type: 'issue_implemented',
      data: { issue_id: 7, number: 42, repo: 'o/r', pr_created: 99, branch: 'auto/fix-42' }
    });
    expect(get(reviewingIssues).has(7)).toBe(false);
    expect(get(issueListRefresh)).toBe(1);
    expect(get(prListRefresh)).toBe(1);
  });

  it('ignores a null event (no crash, no state change)', () => {
    const events = makeEvents();
    initSseBridge({ subscribe: events.subscribe });
    events.set(null);
    expect(get(prListRefresh)).toBe(0);
    expect(get(issueListRefresh)).toBe(0);
  });
});
```

- [ ] **Step 2: Run tests — expect import to fail**

```bash
cd web_ui && npx vitest run src/tests/stores.test.ts
```

Expected: failure on import of `sseBridge.js` (file does not exist).

- [ ] **Step 3: Create `sseBridge.ts`**

Create `web_ui/src/lib/sseBridge.ts`:

```ts
import type { Readable } from 'svelte/store';
import {
  issueListRefresh,
  prListRefresh,
  reviewingIssues,
  reviewingPRs
} from './stores.js';
import type { SseEvent } from './types.js';

function withSet(
  store: typeof reviewingPRs,
  mutate: (s: Set<number>) => void
): void {
  store.update((s) => {
    const next = new Set(s);
    mutate(next);
    return next;
  });
}

export function initSseBridge(events: Readable<SseEvent | null>): () => void {
  return events.subscribe((e) => {
    if (!e) return;
    const data = (e.data ?? {}) as { pr_id?: number; issue_id?: number };
    switch (e.type) {
      case 'pr_detected':
        prListRefresh.update((n) => n + 1);
        break;
      case 'review_started':
        if (typeof data.pr_id === 'number') withSet(reviewingPRs, (s) => s.add(data.pr_id!));
        break;
      case 'review_completed':
        if (typeof data.pr_id === 'number') withSet(reviewingPRs, (s) => s.delete(data.pr_id!));
        prListRefresh.update((n) => n + 1);
        break;
      case 'review_error':
        if (typeof data.pr_id === 'number') withSet(reviewingPRs, (s) => s.delete(data.pr_id!));
        break;
      case 'issue_detected':
        issueListRefresh.update((n) => n + 1);
        break;
      case 'issue_review_started':
        if (typeof data.issue_id === 'number') withSet(reviewingIssues, (s) => s.add(data.issue_id!));
        break;
      case 'issue_review_completed':
        if (typeof data.issue_id === 'number') withSet(reviewingIssues, (s) => s.delete(data.issue_id!));
        issueListRefresh.update((n) => n + 1);
        break;
      case 'issue_review_error':
        if (typeof data.issue_id === 'number') withSet(reviewingIssues, (s) => s.delete(data.issue_id!));
        break;
      case 'issue_implemented':
        // New issue→PR link. The daemon emits this instead of
        // `issue_review_completed` for the auto_implement success path, so
        // we must also clear the in-flight marker or the tile's
        // "reviewing…" chip would never go away.
        if (typeof data.issue_id === 'number')
          withSet(reviewingIssues, (s) => s.delete(data.issue_id!));
        issueListRefresh.update((n) => n + 1);
        prListRefresh.update((n) => n + 1);
        break;
    }
  });
}
```

> **Note on scaffold evolution:** The design spec originally listed 8 SSE event types. After the design was written, the scaffold's commit `147e992` added a 9th event `issue_implemented` (emitted when `auto_implement` creates a PR, payload `{issue_id, number, repo, pr_created, branch}`). The bridge handles it by bumping both refresh counters AND clearing the in-flight `reviewingIssues` marker, because the auto_implement success path emits `issue_implemented` instead of `issue_review_completed` — so without the explicit clear, the tile's "reviewing…" spinner would never disappear.

- [ ] **Step 4: Run tests — expect all to pass**

```bash
cd web_ui && npx vitest run src/tests/stores.test.ts
```

Expected: 6 tests passing.

- [ ] **Step 5: Run full type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 6: Commit**

```bash
git add web_ui/src/lib/sseBridge.ts web_ui/src/tests/stores.test.ts
git commit -m "feat(web_ui): SSE bridge translates daemon events to store effects"
```

---

## Task 6: Wire `initSseBridge` into the layout

**Files:**
- Modify: `web_ui/src/routes/+layout.svelte`

- [ ] **Step 1: Add the import and call**

In `web_ui/src/routes/+layout.svelte`, locate the existing `<script>` imports and the `onMount` block.

Add to the imports:

```ts
import { initSseBridge } from '$lib/sseBridge.js';
```

Inside `onMount`, add a local unsubscriber after the existing `connUnsub` line and before the closing brace:

```ts
let bridgeUnsub: (() => void) | undefined;

onMount(() => {
  if (!browser) return;
  sse = connectEvents();
  connUnsub = sse.connected.subscribe((v) => connected.set(v));
  bridgeUnsub = initSseBridge(sse.events);
});
```

Also update `onDestroy`:

```ts
onDestroy(() => {
  bridgeUnsub?.();
  connUnsub?.();
  sse?.close();
});
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Smoke test the dev server**

```bash
cd web_ui && npm run dev
```

Open `http://localhost:5173`. Expected: page renders (stats cards may 502 without daemon — OK). No JS errors in browser console.

Stop the dev server (`Ctrl-C`).

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/routes/+layout.svelte
git commit -m "feat(web_ui): wire SSE bridge into layout"
```

---

## Task 7: `severity.ts` utility module

**Files:**
- Create: `web_ui/src/lib/severity.ts`
- Test: `web_ui/src/tests/severity.test.ts`

- [ ] **Step 1: Write failing tests**

Create `web_ui/src/tests/severity.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { severityClass, severityOrder } from '../lib/severity.js';

describe('severityClass', () => {
  it('returns red classes for critical', () => {
    expect(severityClass('critical')).toBe('bg-red-100 text-red-700');
  });
  it('returns orange classes for high', () => {
    expect(severityClass('high')).toBe('bg-orange-100 text-orange-700');
  });
  it('returns amber classes for medium', () => {
    expect(severityClass('medium')).toBe('bg-amber-100 text-amber-700');
  });
  it('returns gray classes for low and for unknown', () => {
    expect(severityClass('low')).toBe('bg-gray-100 text-gray-600');
    expect(severityClass('whatever')).toBe('bg-gray-100 text-gray-600');
    expect(severityClass('')).toBe('bg-gray-100 text-gray-600');
  });
  it('is case-insensitive', () => {
    expect(severityClass('HIGH')).toBe('bg-orange-100 text-orange-700');
    expect(severityClass('Critical')).toBe('bg-red-100 text-red-700');
  });
});

describe('severityOrder', () => {
  it('ranks critical highest, unknown lowest', () => {
    const sorted = ['low', 'critical', 'medium', 'unknown', 'high'].sort(
      (a, b) => severityOrder(b) - severityOrder(a)
    );
    expect(sorted).toEqual(['critical', 'high', 'medium', 'low', 'unknown']);
  });
});
```

- [ ] **Step 2: Run tests — expect import failure**

```bash
cd web_ui && npx vitest run src/tests/severity.test.ts
```

Expected: failure on import (module does not exist).

- [ ] **Step 3: Create `severity.ts`**

Create `web_ui/src/lib/severity.ts`:

```ts
const CLASSES: Record<string, string> = {
  critical: 'bg-red-100 text-red-700',
  high: 'bg-orange-100 text-orange-700',
  medium: 'bg-amber-100 text-amber-700',
  low: 'bg-gray-100 text-gray-600'
};

const ORDER: Record<string, number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1
};

export function severityClass(sev: string): string {
  return CLASSES[sev.toLowerCase()] ?? 'bg-gray-100 text-gray-600';
}

export function severityOrder(sev: string): number {
  return ORDER[sev.toLowerCase()] ?? 0;
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
cd web_ui && npx vitest run src/tests/severity.test.ts
```

Expected: all 6 assertions across 6 tests pass.

- [ ] **Step 5: Commit**

```bash
git add web_ui/src/lib/severity.ts web_ui/src/tests/severity.test.ts
git commit -m "feat(web_ui): severity class and ordering utility"
```

---

## Task 8: `persisted.ts` — localStorage-backed writables

**Files:**
- Create: `web_ui/src/lib/persisted.ts`
- Test: `web_ui/src/tests/persisted.test.ts`

- [ ] **Step 1: Write failing tests**

Create `web_ui/src/tests/persisted.test.ts`:

```ts
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { persistedBoolean, persistedString } from '../lib/persisted.js';

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  localStorage.clear();
});

describe('persistedBoolean', () => {
  it('uses default when key is absent', () => {
    const s = persistedBoolean('key1', true);
    expect(get(s)).toBe(true);
  });

  it('reads existing value from localStorage', () => {
    localStorage.setItem('key2', 'false');
    const s = persistedBoolean('key2', true);
    expect(get(s)).toBe(false);
  });

  it('persists on write', () => {
    const s = persistedBoolean('key3', true);
    s.set(false);
    expect(localStorage.getItem('key3')).toBe('false');
  });

  it('tolerates malformed stored value by falling back to default', () => {
    localStorage.setItem('key4', 'not a bool');
    const s = persistedBoolean('key4', true);
    expect(get(s)).toBe(true);
  });
});

describe('persistedString', () => {
  it('uses default when key is absent', () => {
    const s = persistedString('sk1', 'newest');
    expect(get(s)).toBe('newest');
  });

  it('persists on write', () => {
    const s = persistedString('sk2', 'newest');
    s.set('priority');
    expect(localStorage.getItem('sk2')).toBe('priority');
  });
});
```

- [ ] **Step 2: Run tests — expect import failure**

```bash
cd web_ui && npx vitest run src/tests/persisted.test.ts
```

Expected: failure (module does not exist).

- [ ] **Step 3: Create `persisted.ts`**

Create `web_ui/src/lib/persisted.ts`:

```ts
import { writable, type Writable } from 'svelte/store';

// SSR-safe. Returns a writable<boolean> backed by localStorage under `key`.
// On first read, if localStorage has a stored value it is parsed; otherwise
// `initial` is used. On every write, the new value is persisted.
export function persistedBoolean(key: string, initial: boolean): Writable<boolean> {
  const start = read(key, initial, (raw) => (raw === 'true' ? true : raw === 'false' ? false : null));
  const store = writable<boolean>(start);
  store.subscribe((v) => write(key, String(v)));
  return store;
}

export function persistedString(key: string, initial: string): Writable<string> {
  const start = read(key, initial, (raw) => raw);
  const store = writable<string>(start);
  store.subscribe((v) => write(key, v));
  return store;
}

function read<T>(key: string, fallback: T, parse: (raw: string) => T | null): T {
  if (typeof localStorage === 'undefined') return fallback;
  const raw = localStorage.getItem(key);
  if (raw == null) return fallback;
  const parsed = parse(raw);
  return parsed == null ? fallback : parsed;
}

function write(key: string, value: string): void {
  if (typeof localStorage === 'undefined') return;
  localStorage.setItem(key, value);
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
cd web_ui && npx vitest run src/tests/persisted.test.ts
```

Expected: all 6 tests pass.

- [ ] **Step 5: Commit**

```bash
git add web_ui/src/lib/persisted.ts web_ui/src/tests/persisted.test.ts
git commit -m "feat(web_ui): localStorage-backed persisted stores"
```

---

## Task 9: `SeverityBadge.svelte`

**Files:**
- Create: `web_ui/src/lib/components/SeverityBadge.svelte`

- [ ] **Step 1: Create the component**

Create `web_ui/src/lib/components/SeverityBadge.svelte`:

```svelte
<script lang="ts">
  import { severityClass } from '$lib/severity.js';

  let { severity }: { severity: string | null | undefined } = $props();

  const label = $derived((severity ?? 'none').toUpperCase());
  const cls = $derived(
    severity ? severityClass(severity) : 'bg-gray-100 text-gray-500'
  );
</script>

<span class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium {cls}">
  {label}
</span>
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Commit**

```bash
git add web_ui/src/lib/components/SeverityBadge.svelte
git commit -m "feat(web_ui): SeverityBadge component"
```

---

## Task 10: `CollapseHeader.svelte`

**Files:**
- Create: `web_ui/src/lib/components/CollapseHeader.svelte`

- [ ] **Step 1: Create the component**

Create `web_ui/src/lib/components/CollapseHeader.svelte`:

```svelte
<script lang="ts">
  interface Props {
    title: string;
    count: number;
    expanded: boolean;
    onToggle: () => void;
  }

  let { title, count, expanded, onToggle }: Props = $props();
</script>

<button
  type="button"
  onclick={onToggle}
  class="flex w-full items-center gap-2 border-b border-gray-200 bg-gray-50 px-4 py-2 text-left hover:bg-gray-100"
  aria-expanded={expanded}
>
  <svg
    class="h-4 w-4 shrink-0 text-gray-500 transition-transform {expanded ? 'rotate-90' : ''}"
    viewBox="0 0 20 20"
    fill="currentColor"
    aria-hidden="true"
  >
    <path fill-rule="evenodd" d="M7.293 14.707a1 1 0 010-1.414L10.586 10 7.293 6.707a1 1 0 011.414-1.414l4 4a1 1 0 010 1.414l-4 4a1 1 0 01-1.414 0z" clip-rule="evenodd" />
  </svg>
  <span class="text-sm font-semibold text-gray-700">{title}</span>
  <span class="rounded-full bg-gray-200 px-2 py-0.5 text-xs text-gray-600">{count}</span>
</button>
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Commit**

```bash
git add web_ui/src/lib/components/CollapseHeader.svelte
git commit -m "feat(web_ui): CollapseHeader component"
```

---

## Task 11: `PRTile.svelte`

**Files:**
- Create: `web_ui/src/lib/components/PRTile.svelte`

- [ ] **Step 1: Create the component**

Create `web_ui/src/lib/components/PRTile.svelte`:

```svelte
<script lang="ts">
  import { reviewingPRs } from '$lib/stores.js';
  import type { PR } from '$lib/types.js';
  import SeverityBadge from './SeverityBadge.svelte';

  let { pr }: { pr: PR } = $props();

  const isReviewing = $derived($reviewingPRs.has(pr.id));
  const severity = $derived(pr.latest_review?.severity ?? null);
</script>

<a
  href="/prs/{pr.id}"
  class="block border-b border-gray-100 px-4 py-3 hover:bg-gray-50"
>
  <div class="flex items-start gap-3">
    <div class="min-w-0 flex-1">
      <p class="truncate text-sm font-medium text-gray-900">{pr.title}</p>
      <p class="mt-0.5 truncate text-xs text-gray-500">
        <span class="font-mono">{pr.repo}</span>
        · #{pr.number}
        · {pr.author}
      </p>
    </div>
    <div class="flex shrink-0 items-center gap-2">
      {#if isReviewing}
        <span class="text-xs text-indigo-600" data-testid="reviewing-spinner">reviewing…</span>
      {/if}
      <SeverityBadge {severity} />
    </div>
  </div>
</a>
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Commit**

```bash
git add web_ui/src/lib/components/PRTile.svelte
git commit -m "feat(web_ui): PRTile component"
```

---

## Task 12: `IssueTile.svelte`

**Files:**
- Create: `web_ui/src/lib/components/IssueTile.svelte`

- [ ] **Step 1: Create the component**

Create `web_ui/src/lib/components/IssueTile.svelte`:

```svelte
<script lang="ts">
  import { reviewingIssues } from '$lib/stores.js';
  import type { Issue } from '$lib/types.js';
  import SeverityBadge from './SeverityBadge.svelte';

  let { issue }: { issue: Issue } = $props();

  const isReviewing = $derived($reviewingIssues.has(issue.id));

  const triage = $derived(issue.latest_review?.triage as { severity?: string } | undefined);
  const severity = $derived(triage?.severity ?? null);

  const mode = $derived.by(() => {
    if (issue.labels.includes('auto_implement')) return 'auto_implement' as const;
    if (issue.labels.includes('review_only')) return 'review_only' as const;
    return null;
  });
</script>

<a
  href="/issues/{issue.id}"
  class="block border-b border-gray-100 px-4 py-3 hover:bg-gray-50"
>
  <div class="flex items-start gap-3">
    <div class="min-w-0 flex-1">
      <p class="truncate text-sm font-medium text-gray-900">{issue.title}</p>
      <p class="mt-0.5 truncate text-xs text-gray-500">
        <span class="font-mono">{issue.repo}</span>
        · #{issue.number}
        · {issue.author}
      </p>
      {#if mode}
        <span
          class="mt-1 inline-flex rounded px-1.5 py-0.5 text-[10px] font-medium
          {mode === 'auto_implement'
            ? 'bg-indigo-100 text-indigo-700'
            : 'bg-blue-100 text-blue-700'}"
        >
          {mode}
        </span>
      {/if}
    </div>
    <div class="flex shrink-0 items-center gap-2">
      {#if isReviewing}
        <span class="text-xs text-indigo-600">reviewing…</span>
      {/if}
      {#if severity}
        <SeverityBadge {severity} />
      {:else}
        <span class="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500">
          PENDING
        </span>
      {/if}
    </div>
  </div>
</a>
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Commit**

```bash
git add web_ui/src/lib/components/IssueTile.svelte
git commit -m "feat(web_ui): IssueTile component"
```

---

## Task 13: `FilterBar.svelte`

**Files:**
- Create: `web_ui/src/lib/components/FilterBar.svelte`

- [ ] **Step 1: Create the component**

Create `web_ui/src/lib/components/FilterBar.svelte`:

```svelte
<script lang="ts">
  interface Filters {
    repo: string;
    severity: string;
    // One of state (PRs) or mode (issues).
    state?: string;
    mode?: string;
  }

  interface Props {
    filters: Filters;
    repos: string[];          // unique repos available in current dataset
    variant: 'pr' | 'issue';
    onChange: (next: Filters) => void;
  }

  let { filters, repos, variant, onChange }: Props = $props();

  const severities = ['any', 'critical', 'high', 'medium', 'low'];
  const prStates = ['open', 'closed', 'all'];
  const issueModes = ['all', 'auto_implement', 'review_only'];

  function update(field: keyof Filters, value: string): void {
    onChange({ ...filters, [field]: value });
  }
</script>

<div class="mb-4 flex flex-wrap items-center gap-3 rounded-md border border-gray-200 bg-gray-50 p-3">
  <label class="flex items-center gap-1 text-xs text-gray-600">
    <span>Repo:</span>
    <select
      class="rounded border border-gray-300 bg-white px-2 py-1 text-xs"
      value={filters.repo}
      onchange={(e) => update('repo', (e.currentTarget as HTMLSelectElement).value)}
    >
      <option value="">any</option>
      {#each repos as repo (repo)}
        <option value={repo}>{repo}</option>
      {/each}
    </select>
  </label>

  <label class="flex items-center gap-1 text-xs text-gray-600">
    <span>Severity:</span>
    <select
      class="rounded border border-gray-300 bg-white px-2 py-1 text-xs"
      value={filters.severity || 'any'}
      onchange={(e) => update('severity', (e.currentTarget as HTMLSelectElement).value)}
    >
      {#each severities as s (s)}
        <option value={s}>{s}</option>
      {/each}
    </select>
  </label>

  {#if variant === 'pr'}
    <label class="flex items-center gap-1 text-xs text-gray-600">
      <span>State:</span>
      <select
        class="rounded border border-gray-300 bg-white px-2 py-1 text-xs"
        value={filters.state ?? 'open'}
        onchange={(e) => update('state', (e.currentTarget as HTMLSelectElement).value)}
      >
        {#each prStates as s (s)}
          <option value={s}>{s}</option>
        {/each}
      </select>
    </label>
  {:else}
    <label class="flex items-center gap-1 text-xs text-gray-600">
      <span>Mode:</span>
      <select
        class="rounded border border-gray-300 bg-white px-2 py-1 text-xs"
        value={filters.mode ?? 'all'}
        onchange={(e) => update('mode', (e.currentTarget as HTMLSelectElement).value)}
      >
        {#each issueModes as m (m)}
          <option value={m}>{m}</option>
        {/each}
      </select>
    </label>
  {/if}
</div>
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Commit**

```bash
git add web_ui/src/lib/components/FilterBar.svelte
git commit -m "feat(web_ui): FilterBar component with PR/issue variants"
```

---

## Task 14: `ReviewCard.svelte`

**Files:**
- Create: `web_ui/src/lib/components/ReviewCard.svelte`

- [ ] **Step 1: Create the component**

Create `web_ui/src/lib/components/ReviewCard.svelte`:

```svelte
<script lang="ts">
  import type { IssueReview, Review } from '$lib/types.js';
  import SeverityBadge from './SeverityBadge.svelte';

  interface Props {
    review: Review | IssueReview;
    variant: 'pr' | 'issue';
  }

  let { review, variant }: Props = $props();

  const asPR = $derived(variant === 'pr' ? (review as Review) : null);
  const asIssue = $derived(variant === 'issue' ? (review as IssueReview) : null);

  const triage = $derived(asIssue?.triage as {
    severity?: string;
    category?: string;
    suggested_assignee?: string;
  } | undefined);
</script>

<article class="mb-3 rounded-md border border-gray-200 bg-white p-4">
  <header class="mb-2 flex items-center justify-between gap-2">
    <div class="flex items-center gap-2">
      {#if asPR}
        <SeverityBadge severity={asPR.severity} />
      {:else if triage?.severity}
        <SeverityBadge severity={triage.severity} />
      {/if}
      <span class="text-xs text-gray-500">{review.cli_used}</span>
    </div>
    <time class="text-xs text-gray-400" datetime={review.created_at}>
      {new Date(review.created_at).toLocaleString()}
    </time>
  </header>

  {#if review.summary}
    <p class="text-sm text-gray-800">{review.summary}</p>
  {/if}

  {#if asPR && asPR.issues.length > 0}
    <section class="mt-3">
      <h4 class="text-xs font-semibold uppercase tracking-wide text-gray-500">
        Findings ({asPR.issues.length})
      </h4>
      <ul class="mt-1 space-y-1 text-sm">
        {#each asPR.issues as f (f.file + f.line + f.description)}
          <li class="flex items-start gap-2">
            <SeverityBadge severity={f.severity} />
            <span class="font-mono text-xs text-gray-500">{f.file}:{f.line}</span>
            <span class="text-gray-800">{f.description}</span>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if asIssue && triage}
    <section class="mt-3 rounded bg-gray-50 p-2 text-xs text-gray-700">
      {#if triage.severity}<div><strong>Severity:</strong> {triage.severity}</div>{/if}
      {#if triage.category}<div><strong>Category:</strong> {triage.category}</div>{/if}
      {#if triage.suggested_assignee}<div><strong>Suggested assignee:</strong> {triage.suggested_assignee}</div>{/if}
    </section>
  {/if}

  {#if review.suggestions && review.suggestions.length > 0}
    <section class="mt-3">
      <h4 class="text-xs font-semibold uppercase tracking-wide text-gray-500">Suggestions</h4>
      <ul class="mt-1 list-disc space-y-1 pl-5 text-sm text-gray-800">
        {#each review.suggestions as s, i (i)}
          <li>{typeof s === 'string' ? s : JSON.stringify(s)}</li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if asIssue && asIssue.action_taken === 'auto_implement' && asIssue.pr_created > 0}
    <p class="mt-3 text-xs">
      <a href="/prs/{asIssue.pr_created}" class="text-indigo-600 hover:underline">
        View created PR →
      </a>
    </p>
  {/if}
</article>
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Commit**

```bash
git add web_ui/src/lib/components/ReviewCard.svelte
git commit -m "feat(web_ui): ReviewCard component with PR/issue variants"
```

---

## Task 15: Extract `ConnectionPill` and `StatsCards` from the scaffold landing page

**Files:**
- Create: `web_ui/src/lib/components/ConnectionPill.svelte`
- Create: `web_ui/src/lib/components/StatsCards.svelte`

- [ ] **Step 1: Create `ConnectionPill.svelte`**

Create `web_ui/src/lib/components/ConnectionPill.svelte`:

```svelte
<script lang="ts">
  import { getContext } from 'svelte';
  import type { Readable } from 'svelte/store';

  const connected = getContext<Readable<boolean>>('sse-connected');

  const cls = $derived(
    $connected ? 'bg-green-100 text-green-700' : 'bg-amber-100 text-amber-700'
  );
  const label = $derived($connected ? 'connected' : 'disconnected');
</script>

<span class="rounded-full px-2 py-0.5 text-xs font-medium {cls}" data-testid="sse-pill">
  {label}
</span>
```

- [ ] **Step 2: Create `StatsCards.svelte`**

Create `web_ui/src/lib/components/StatsCards.svelte`:

```svelte
<script lang="ts">
  import type { Stats } from '$lib/types.js';

  let { stats }: { stats: Stats | null } = $props();

  const cells = $derived([
    { label: 'Total reviews', value: stats?.total_reviews },
    { label: 'Avg findings / review', value: stats?.avg_issues_per_review?.toFixed(1) },
    { label: 'Median time (s)', value: stats?.review_timing?.median_seconds?.toFixed(1) },
    { label: 'High-severity', value: stats?.by_severity?.HIGH ?? 0 }
  ]);
</script>

<section class="grid grid-cols-2 gap-4 md:grid-cols-4">
  {#each cells as cell (cell.label)}
    <div class="rounded-lg border border-gray-200 bg-white p-4">
      <dt class="text-xs uppercase tracking-wide text-gray-500">{cell.label}</dt>
      <dd class="mt-1 text-2xl font-semibold">{cell.value ?? '—'}</dd>
    </div>
  {/each}
</section>
```

- [ ] **Step 3: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/lib/components/ConnectionPill.svelte web_ui/src/lib/components/StatsCards.svelte
git commit -m "feat(web_ui): extract ConnectionPill and StatsCards components"
```

---

## Task 16: `/prs` — PR list page

**Files:**
- Create: `web_ui/src/routes/prs/+page.svelte`

- [ ] **Step 1: Create the page**

Create `web_ui/src/routes/prs/+page.svelte`:

```svelte
<script lang="ts">
  import { browser } from '$app/environment';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import PRTile from '$lib/components/PRTile.svelte';
  import { fetchPRs } from '$lib/api.js';
  import { severityOrder } from '$lib/severity.js';
  import { prListRefresh, reviewingPRs } from '$lib/stores.js';
  import type { PR } from '$lib/types.js';

  let prs = $state<PR[]>([]);
  let err = $state<string | null>(null);
  let loading = $state(true);

  $effect(() => {
    $prListRefresh; // re-run on SSE-driven bump
    if (!browser) return;
    loading = true;
    fetchPRs()
      .then((r) => {
        prs = r;
        err = null;
      })
      .catch((e) => {
        err = e instanceof Error ? e.message : String(e);
      })
      .finally(() => (loading = false));
  });

  const repo     = $derived($page.url.searchParams.get('repo') ?? '');
  const severity = $derived($page.url.searchParams.get('severity') ?? 'any');
  const state    = $derived($page.url.searchParams.get('state') ?? 'open');

  const repos = $derived(Array.from(new Set(prs.map((p) => p.repo))).sort());

  const filtered = $derived.by(() => {
    const list = prs.filter((p) => {
      if (repo && p.repo !== repo) return false;
      if (state !== 'all' && p.state !== state) return false;
      if (severity !== 'any') {
        const s = p.latest_review?.severity?.toLowerCase() ?? '';
        if (s !== severity) return false;
      }
      return true;
    });
    return list.sort(
      (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
    );
  });

  function applyFilters(next: { repo: string; severity: string; state?: string }): void {
    const params = new URLSearchParams();
    if (next.repo) params.set('repo', next.repo);
    if (next.severity && next.severity !== 'any') params.set('severity', next.severity);
    if (next.state && next.state !== 'open') params.set('state', next.state);
    const qs = params.toString();
    goto(qs ? `/prs?${qs}` : '/prs', { keepFocus: true, replaceState: true, noScroll: true });
  }
</script>

<section class="flex items-center gap-3">
  <h1 class="text-2xl font-bold">PR Reviews</h1>
  {#if $reviewingPRs.size > 0}
    <span class="text-xs text-indigo-600">{$reviewingPRs.size} reviewing…</span>
  {/if}
</section>

<FilterBar
  filters={{ repo, severity, state }}
  repos={repos}
  variant="pr"
  onChange={applyFilters}
/>

{#if err}
  <p class="text-sm text-red-600">Could not load PRs: {err}</p>
{:else if loading && prs.length === 0}
  <p class="text-sm text-gray-500">Loading…</p>
{:else if filtered.length === 0}
  <p class="text-sm text-gray-500">No PRs match the current filters.</p>
{:else}
  <div class="overflow-hidden rounded-md border border-gray-200 bg-white">
    {#each filtered as pr (pr.id)}
      <PRTile {pr} />
    {/each}
  </div>
{/if}
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors. If it reports unused `severityOrder`, that's because we didn't use it on this page (PRs sort by `updated_at`, not severity). Remove the import:

```ts
// Remove: import { severityOrder } from '$lib/severity.js';
```

Re-run `npm run check` — expected: 0 errors.

- [ ] **Step 3: Dev-server smoke test**

```bash
cd web_ui && npm run dev
```

Visit `http://localhost:5173/prs` — expected: "No PRs match the current filters" (if no daemon) or a list (with daemon).

Stop the dev server.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/routes/prs/+page.svelte
git commit -m "feat(web_ui): /prs list with URL-driven filters"
```

---

## Task 17: `/prs/[id]` — PR detail page

**Files:**
- Create: `web_ui/src/routes/prs/[id]/+page.svelte`

- [ ] **Step 1: Create the page**

Create `web_ui/src/routes/prs/[id]/+page.svelte`:

```svelte
<script lang="ts">
  import { browser } from '$app/environment';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import ReviewCard from '$lib/components/ReviewCard.svelte';
  import { dismissPR, fetchPR, triggerReview, undismissPR } from '$lib/api.js';
  import { prListRefresh, reviewingPRs } from '$lib/stores.js';
  import type { PR, Review } from '$lib/types.js';

  const id = $derived(Number($page.params.id));

  let pr = $state<PR | null>(null);
  let reviews = $state<Review[]>([]);
  let err = $state<string | null>(null);
  let busy = $state(false);

  $effect(() => {
    $prListRefresh;
    if (!browser || !Number.isFinite(id)) return;
    fetchPR(id)
      .then((d) => {
        pr = d.pr;
        reviews = d.reviews;
        err = null;
      })
      .catch((e) => {
        err = e instanceof Error ? e.message : String(e);
      });
  });

  const reviewing = $derived($reviewingPRs.has(id));

  async function onReview(): Promise<void> {
    if (!pr) return;
    busy = true;
    try {
      // Optimistic: mark reviewing before SSE confirms.
      reviewingPRs.update((s) => new Set(s).add(id));
      await triggerReview(id);
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
      reviewingPRs.update((s) => {
        const n = new Set(s);
        n.delete(id);
        return n;
      });
    } finally {
      busy = false;
    }
  }

  async function onDismissToggle(): Promise<void> {
    if (!pr) return;
    busy = true;
    try {
      if (pr.dismissed) {
        await undismissPR(id);
        prListRefresh.update((n) => n + 1);
      } else {
        await dismissPR(id);
        // Dismissed PRs disappear from /prs; navigate back.
        goto('/prs');
        return;
      }
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if err && !pr}
  <p class="text-sm text-red-600">Could not load PR: {err}</p>
{:else if !pr}
  <p class="text-sm text-gray-500">Loading…</p>
{:else}
  <article>
    <header class="mb-4 rounded-md border border-gray-200 bg-white p-4">
      <h1 class="text-xl font-bold">{pr.title}</h1>
      <p class="mt-1 text-sm text-gray-500">
        <span class="font-mono">{pr.repo}</span>
        · #{pr.number}
        · {pr.author}
        · <span class="capitalize">{pr.state}</span>
      </p>
      <p class="mt-2 text-xs">
        <a href={pr.url} class="text-indigo-600 hover:underline" target="_blank" rel="noreferrer">
          View on GitHub →
        </a>
      </p>
    </header>

    <div class="mb-4 flex items-center gap-2">
      <button
        type="button"
        class="rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
        onclick={onReview}
        disabled={busy || reviewing}
      >
        {reviewing ? 'Reviewing…' : 'Review now'}
      </button>
      <button
        type="button"
        class="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
        onclick={onDismissToggle}
        disabled={busy}
      >
        {pr.dismissed ? 'Undismiss' : 'Dismiss'}
      </button>
      {#if err}
        <span class="text-xs text-red-600">{err}</span>
      {/if}
    </div>

    <section>
      <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-gray-500">
        Review history ({reviews.length})
      </h2>
      {#if reviews.length === 0}
        <p class="text-sm text-gray-500">No reviews yet.</p>
      {:else}
        {#each reviews as r (r.id)}
          <ReviewCard review={r} variant="pr" />
        {/each}
      {/if}
    </section>
  </article>
{/if}
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Dev-server smoke test**

```bash
cd web_ui && npm run dev
```

Visit `http://localhost:5173/prs/1` — expected: "Could not load PR" (no daemon) or the PR detail (with daemon).

Stop the dev server.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/routes/prs/[id]/+page.svelte
git commit -m "feat(web_ui): /prs/[id] detail page with actions"
```

---

## Task 18: `/issues` — Issue list page

**Files:**
- Create: `web_ui/src/routes/issues/+page.svelte`

- [ ] **Step 1: Create the page**

Create `web_ui/src/routes/issues/+page.svelte`:

```svelte
<script lang="ts">
  import { browser } from '$app/environment';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import IssueTile from '$lib/components/IssueTile.svelte';
  import { fetchIssues } from '$lib/api.js';
  import { issueListRefresh, reviewingIssues } from '$lib/stores.js';
  import type { Issue } from '$lib/types.js';

  let issues = $state<Issue[]>([]);
  let err = $state<string | null>(null);
  let loading = $state(true);

  $effect(() => {
    $issueListRefresh;
    if (!browser) return;
    loading = true;
    fetchIssues()
      .then((r) => {
        issues = r;
        err = null;
      })
      .catch((e) => (err = e instanceof Error ? e.message : String(e)))
      .finally(() => (loading = false));
  });

  const repo     = $derived($page.url.searchParams.get('repo') ?? '');
  const severity = $derived($page.url.searchParams.get('severity') ?? 'any');
  const mode     = $derived($page.url.searchParams.get('mode') ?? 'all');

  const repos = $derived(Array.from(new Set(issues.map((i) => i.repo))).sort());

  const filtered = $derived.by(() => {
    const list = issues.filter((i) => {
      if (i.dismissed) return false;
      if (repo && i.repo !== repo) return false;
      if (severity !== 'any') {
        const triage = i.latest_review?.triage as { severity?: string } | undefined;
        const s = triage?.severity?.toLowerCase() ?? '';
        if (s !== severity) return false;
      }
      if (mode !== 'all') {
        if (!i.labels.includes(mode)) return false;
      }
      return true;
    });
    return list.sort((a, b) => {
      const ta = new Date(a.fetched_at).getTime();
      const tb = new Date(b.fetched_at).getTime();
      return tb - ta;
    });
  });

  function applyFilters(next: { repo: string; severity: string; mode?: string }): void {
    const params = new URLSearchParams();
    if (next.repo) params.set('repo', next.repo);
    if (next.severity && next.severity !== 'any') params.set('severity', next.severity);
    if (next.mode && next.mode !== 'all') params.set('mode', next.mode);
    const qs = params.toString();
    goto(qs ? `/issues?${qs}` : '/issues', { keepFocus: true, replaceState: true, noScroll: true });
  }
</script>

<section class="flex items-center gap-3">
  <h1 class="text-2xl font-bold">Issues</h1>
  {#if $reviewingIssues.size > 0}
    <span class="text-xs text-indigo-600">{$reviewingIssues.size} reviewing…</span>
  {/if}
</section>

<FilterBar
  filters={{ repo, severity, mode }}
  repos={repos}
  variant="issue"
  onChange={applyFilters}
/>

{#if err}
  <p class="text-sm text-red-600">Could not load issues: {err}</p>
{:else if loading && issues.length === 0}
  <p class="text-sm text-gray-500">Loading…</p>
{:else if filtered.length === 0}
  <p class="text-sm text-gray-500">No issues match the current filters.</p>
{:else}
  <div class="overflow-hidden rounded-md border border-gray-200 bg-white">
    {#each filtered as issue (issue.id)}
      <IssueTile {issue} />
    {/each}
  </div>
{/if}
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Dev-server smoke test**

```bash
cd web_ui && npm run dev
```

Visit `http://localhost:5173/issues` — expected: no crash, loading state, then "No issues match…" (without daemon) or list (with daemon).

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/routes/issues/+page.svelte
git commit -m "feat(web_ui): /issues list with URL-driven filters"
```

---

## Task 19: `/issues/[id]` — Issue detail page

**Files:**
- Create: `web_ui/src/routes/issues/[id]/+page.svelte`

- [ ] **Step 1: Create the page**

Create `web_ui/src/routes/issues/[id]/+page.svelte`:

```svelte
<script lang="ts">
  import { browser } from '$app/environment';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import ReviewCard from '$lib/components/ReviewCard.svelte';
  import {
    dismissIssue,
    fetchIssue,
    triggerIssueReview,
    undismissIssue
  } from '$lib/api.js';
  import { issueListRefresh, reviewingIssues } from '$lib/stores.js';
  import type { Issue, IssueReview } from '$lib/types.js';

  const id = $derived(Number($page.params.id));

  let issue = $state<Issue | null>(null);
  let reviews = $state<IssueReview[]>([]);
  let err = $state<string | null>(null);
  let busy = $state(false);

  $effect(() => {
    $issueListRefresh;
    if (!browser || !Number.isFinite(id)) return;
    fetchIssue(id)
      .then((d) => {
        issue = d.issue;
        reviews = d.reviews;
        err = null;
      })
      .catch((e) => (err = e instanceof Error ? e.message : String(e)));
  });

  const reviewing = $derived($reviewingIssues.has(id));

  const autoImplementPR = $derived(
    reviews.find((r) => r.action_taken === 'auto_implement' && r.pr_created > 0)?.pr_created
  );

  async function onReview(): Promise<void> {
    if (!issue) return;
    busy = true;
    try {
      reviewingIssues.update((s) => new Set(s).add(id));
      await triggerIssueReview(id);
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
      reviewingIssues.update((s) => {
        const n = new Set(s);
        n.delete(id);
        return n;
      });
    } finally {
      busy = false;
    }
  }

  async function onDismissToggle(): Promise<void> {
    if (!issue) return;
    busy = true;
    try {
      if (issue.dismissed) {
        await undismissIssue(id);
        issueListRefresh.update((n) => n + 1);
      } else {
        await dismissIssue(id);
        goto('/issues');
        return;
      }
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if err && !issue}
  <p class="text-sm text-red-600">Could not load issue: {err}</p>
{:else if !issue}
  <p class="text-sm text-gray-500">Loading…</p>
{:else}
  <article>
    <header class="mb-4 rounded-md border border-gray-200 bg-white p-4">
      <h1 class="text-xl font-bold">{issue.title}</h1>
      <p class="mt-1 text-sm text-gray-500">
        <span class="font-mono">{issue.repo}</span>
        · #{issue.number}
        · {issue.author}
        · <span class="capitalize">{issue.state}</span>
      </p>
      {#if issue.labels.length > 0}
        <ul class="mt-2 flex flex-wrap gap-1">
          {#each issue.labels as l (l)}
            <li class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium text-gray-700">
              {l}
            </li>
          {/each}
        </ul>
      {/if}
      {#if issue.assignees.length > 0}
        <p class="mt-2 text-xs text-gray-500">
          Assignees: {issue.assignees.join(', ')}
        </p>
      {/if}
      <p class="mt-2 text-xs">
        <a
          href="https://github.com/{issue.repo}/issues/{issue.number}"
          class="text-indigo-600 hover:underline"
          target="_blank"
          rel="noreferrer"
        >
          View on GitHub →
        </a>
      </p>
      {#if autoImplementPR}
        <p class="mt-2 text-xs">
          <a href="/prs/{autoImplementPR}" class="text-indigo-600 hover:underline">
            View created PR →
          </a>
        </p>
      {/if}
    </header>

    <div class="mb-4 flex items-center gap-2">
      <button
        type="button"
        class="rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
        onclick={onReview}
        disabled={busy || reviewing}
      >
        {reviewing ? 'Reviewing…' : 'Review now'}
      </button>
      <button
        type="button"
        class="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
        onclick={onDismissToggle}
        disabled={busy}
      >
        {issue.dismissed ? 'Undismiss' : 'Dismiss'}
      </button>
      {#if err}
        <span class="text-xs text-red-600">{err}</span>
      {/if}
    </div>

    <section>
      <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-gray-500">
        Review history ({reviews.length})
      </h2>
      {#if reviews.length === 0}
        <p class="text-sm text-gray-500">No reviews yet.</p>
      {:else}
        {#each reviews as r (r.id)}
          <ReviewCard review={r} variant="issue" />
        {/each}
      {/if}
    </section>
  </article>
{/if}
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Dev-server smoke test**

```bash
cd web_ui && npm run dev
```

Visit `http://localhost:5173/issues/1` — expected: no crash.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/routes/issues/[id]/+page.svelte
git commit -m "feat(web_ui): /issues/[id] detail page with actions"
```

---

## Task 20: `/` — Dashboard page (replace scaffold placeholder)

**Files:**
- Modify: `web_ui/src/routes/+page.svelte` (full rewrite)

- [ ] **Step 1: Replace the entire file**

Overwrite `web_ui/src/routes/+page.svelte` with:

```svelte
<script lang="ts">
  import { browser } from '$app/environment';
  import CollapseHeader from '$lib/components/CollapseHeader.svelte';
  import ConnectionPill from '$lib/components/ConnectionPill.svelte';
  import IssueTile from '$lib/components/IssueTile.svelte';
  import PRTile from '$lib/components/PRTile.svelte';
  import StatsCards from '$lib/components/StatsCards.svelte';
  import { fetchIssues, fetchMe, fetchPRs, fetchStats } from '$lib/api.js';
  import { persistedBoolean, persistedString } from '$lib/persisted.js';
  import { severityOrder } from '$lib/severity.js';
  import { issueListRefresh, prListRefresh } from '$lib/stores.js';
  import type { Issue, PR, Stats } from '$lib/types.js';

  let prs = $state<PR[]>([]);
  let issues = $state<Issue[]>([]);
  let stats = $state<Stats | null>(null);
  let me = $state<string>('');
  let err = $state<string | null>(null);

  $effect(() => {
    $prListRefresh;
    if (!browser) return;
    fetchPRs().then((r) => (prs = r)).catch((e) => (err = String(e)));
    fetchStats().then((r) => (stats = r)).catch(() => {});
  });

  $effect(() => {
    $issueListRefresh;
    if (!browser) return;
    fetchIssues().then((r) => (issues = r)).catch(() => {});
  });

  $effect(() => {
    if (!browser) return;
    fetchMe().then((r) => (me = r.login)).catch(() => {});
  });

  const sort = persistedString('dashboard.sort', 'priority');
  const reviewsExpanded = persistedBoolean('dashboard.reviewsExpanded', true);
  const prsExpanded = persistedBoolean('dashboard.prsExpanded', true);
  const issuesExpanded = persistedBoolean('dashboard.issuesExpanded', true);

  function byUpdated(a: PR, b: PR): number {
    return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();
  }
  function byPriority(a: PR, b: PR): number {
    const d = severityOrder(b.latest_review?.severity ?? '') - severityOrder(a.latest_review?.severity ?? '');
    return d !== 0 ? d : byUpdated(a, b);
  }

  const sorter = $derived($sort === 'priority' ? byPriority : byUpdated);

  const meLower = $derived(me.toLowerCase());

  const myReviews = $derived(
    prs
      .filter((p) => !p.dismissed && p.author.toLowerCase() !== meLower)
      .sort(sorter)
  );
  const myPRs = $derived(
    prs
      .filter((p) => !p.dismissed && p.author.toLowerCase() === meLower)
      .sort(sorter)
  );
  const trackedIssues = $derived(issues.filter((i) => !i.dismissed));

  const empty = $derived(prs.length === 0 && issues.length === 0);
</script>

<section class="mb-6 flex items-center gap-3">
  <h1 class="text-3xl font-bold">Dashboard</h1>
  <ConnectionPill />
</section>

<StatsCards {stats} />

<section class="mt-6 mb-3 flex items-center gap-2">
  <span class="text-xs text-gray-500">Sort:</span>
  <button
    type="button"
    class="rounded px-2 py-1 text-xs font-medium {$sort === 'priority'
      ? 'bg-indigo-100 text-indigo-700'
      : 'text-gray-600 hover:bg-gray-100'}"
    onclick={() => sort.set('priority')}
  >
    Priority
  </button>
  <button
    type="button"
    class="rounded px-2 py-1 text-xs font-medium {$sort === 'newest'
      ? 'bg-indigo-100 text-indigo-700'
      : 'text-gray-600 hover:bg-gray-100'}"
    onclick={() => sort.set('newest')}
  >
    Newest
  </button>
</section>

{#if err}
  <p class="text-sm text-red-600">Could not load: {err}</p>
{:else if empty}
  <p class="mt-6 text-center text-sm text-gray-500">No activity yet.</p>
{:else}
  <div class="overflow-hidden rounded-md border border-gray-200 bg-white">
    {#if myReviews.length > 0}
      <CollapseHeader
        title="My Reviews"
        count={myReviews.length}
        expanded={$reviewsExpanded}
        onToggle={() => reviewsExpanded.update((v) => !v)}
      />
      {#if $reviewsExpanded}
        {#each myReviews as pr (pr.id)}
          <PRTile {pr} />
        {/each}
      {/if}
    {/if}

    {#if myPRs.length > 0}
      <CollapseHeader
        title="My PRs"
        count={myPRs.length}
        expanded={$prsExpanded}
        onToggle={() => prsExpanded.update((v) => !v)}
      />
      {#if $prsExpanded}
        {#each myPRs as pr (pr.id)}
          <PRTile {pr} />
        {/each}
      {/if}
    {/if}

    {#if trackedIssues.length > 0}
      <CollapseHeader
        title="Tracked Issues"
        count={trackedIssues.length}
        expanded={$issuesExpanded}
        onToggle={() => issuesExpanded.update((v) => !v)}
      />
      {#if $issuesExpanded}
        {#each trackedIssues as issue (issue.id)}
          <IssueTile {issue} />
        {/each}
      {/if}
    {/if}
  </div>
{/if}
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors.

- [ ] **Step 3: Dev-server smoke test**

```bash
cd web_ui && npm run dev
```

Visit `http://localhost:5173/` — expected: Dashboard header with connection pill, stats cards (may be `—` without daemon), sort toggle; sections only render when data exists.

Stop the dev server.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/routes/+page.svelte
git commit -m "feat(web_ui): / Dashboard with sections, sort, and persisted state"
```

---

## Task 21: Full verification pass

**Files:**
- No new code; run all checks.

- [ ] **Step 1: Lint**

```bash
cd web_ui && npm run lint
```

Expected: clean. If prettier flags formatting, run `npm run format` and re-commit:

```bash
npm run format
git add -A
git commit -m "style(web_ui): apply prettier"
```

- [ ] **Step 2: Type-check**

```bash
cd web_ui && npm run check
```

Expected: 0 errors, 0 warnings.

- [ ] **Step 3: Unit tests**

```bash
cd web_ui && npm test
```

Expected: all passing — scaffold baseline (15) + Task 2 (+4) + Task 3 (+3) + Task 5 (+6) + Task 7 (+6) + Task 8 (+6) = **40 tests**. If the exact count differs, check whether the per-task tests landed as planned.

- [ ] **Step 4: Production build**

```bash
cd web_ui && npm run build
```

Expected: `build/handler.js` generated, no errors.

- [ ] **Step 5: Docker build (sanity)**

```bash
cd web_ui && docker build -t heimdallm-web:routes .
```

Expected: image built, same size band as the scaffold (~160 MB).

- [ ] **Step 6: Manual test plan against live daemon**

Prereqs: a running heimdallm daemon with at least one PR and one issue tracked.

Run the web UI dev server against it:

```bash
cd web_ui && HEIMDALLM_API_URL=http://localhost:7842 npm run dev
```

Then verify each acceptance row in `docs/superpowers/specs/2026-04-17-web-ui-routes-design.md` §8 "Manual test plan". Keep notes; anything that fails goes back to the relevant task.

- [ ] **Step 7: Commit anything discovered in Step 6 fix-ups**

If any manual findings required code changes, commit them with clear messages (one per fix) before opening the PR.

---

## Self-review

**Spec coverage** (each design-spec section → tasks):

| Spec section | Tasks |
|---|---|
| §1 Dependency strategy | Task 0 (branch setup) |
| §2 Inherited follow-ups | Task 1, 2, 3 |
| §3 Architecture overview | Tasks 4, 5, 6 establish the store/bridge pattern; Tasks 16–20 use it |
| §4 Stores & SSE bridge | Task 4 (stores), Task 5 (bridge), Task 6 (wiring) |
| §5.1 `/` Dashboard | Task 20 |
| §5.2 `/prs` list | Task 16 |
| §5.3 `/prs/{id}` detail | Task 17 |
| §5.4 `/issues` list | Task 18 |
| §5.5 `/issues/{id}` detail | Task 19 |
| §6 Shared components | Tasks 9–15 |
| §7 Types & SSE updates | Tasks 1, 2, 3 |
| §8 Testing | TDD throughout Tasks 2, 3, 5, 7, 8; manual plan in Task 21 |

**Placeholder scan:** No `TBD`, no `TODO`, no "add error handling" / "similar to" / "write tests for the above" — every step has explicit code, paths, and expected outcomes.

**Type consistency:**
- `parseIssue`, `parseIssueReview`, `parseMaybeJson` — names stable across Task 3 and usage sites.
- `initSseBridge` — stable across Task 5 and Task 6.
- `prListRefresh`, `issueListRefresh`, `reviewingPRs`, `reviewingIssues` — stable across stores.ts, sseBridge.ts, and every page.
- `severityClass`, `severityOrder` — stable across Task 7 and Task 20.
- `persistedBoolean`, `persistedString` — stable across Task 8 and Task 20.
- Component props: `pr: PR`, `issue: Issue`, `stats: Stats | null`, `severity: string | null | undefined` consistent across components.

**Scope check:** One branch, one PR, ~22 tasks. Well-bounded. No sub-project decomposition needed.
