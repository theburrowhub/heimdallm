import type { Readable } from 'svelte/store';
import { issueListRefresh, prListRefresh, reviewingIssues, reviewingPRs } from './stores.js';
import type { SseEvent } from './types.js';

// Clears the in-flight review sets whenever the SSE connection transitions
// from disconnected to connected. Rationale: if the daemon emitted
// `review_completed` or `issue_review_completed` while we were offline, we
// won't know — and the tile would otherwise show "reviewing…" forever.
// Accepts brief UI inaccuracy (an active review appears not-in-flight until
// the daemon re-emits `review_started`) in exchange for eventual consistency.
//
// Does NOT sweep on the very first emission to avoid clearing a legitimate
// in-flight set populated by an optimistic client-side update before the
// first SSE connection even opens.
export function watchReconnectAndSweep(connected: Readable<boolean>): () => void {
  let seen = false;
  let previous = false;
  return connected.subscribe((now) => {
    if (!seen) {
      seen = true;
      previous = now;
      return;
    }
    if (now && !previous) {
      reviewingPRs.set(new Set());
      reviewingIssues.set(new Set());
    }
    previous = now;
  });
}

type SetStore = typeof reviewingPRs;

function mutateSet(store: SetStore, mutate: (s: Set<number>) => void): void {
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
        if (typeof data.pr_id === 'number') mutateSet(reviewingPRs, (s) => s.add(data.pr_id!));
        break;
      case 'review_completed':
        if (typeof data.pr_id === 'number') mutateSet(reviewingPRs, (s) => s.delete(data.pr_id!));
        prListRefresh.update((n) => n + 1);
        break;
      case 'review_error':
        if (typeof data.pr_id === 'number') mutateSet(reviewingPRs, (s) => s.delete(data.pr_id!));
        break;
      case 'issue_detected':
        issueListRefresh.update((n) => n + 1);
        break;
      case 'issue_review_started':
        if (typeof data.issue_id === 'number')
          mutateSet(reviewingIssues, (s) => s.add(data.issue_id!));
        break;
      case 'issue_review_completed':
        if (typeof data.issue_id === 'number')
          mutateSet(reviewingIssues, (s) => s.delete(data.issue_id!));
        issueListRefresh.update((n) => n + 1);
        break;
      case 'issue_review_error':
        if (typeof data.issue_id === 'number')
          mutateSet(reviewingIssues, (s) => s.delete(data.issue_id!));
        break;
      case 'issue_implemented':
        // New issue→PR link. The daemon emits this instead of
        // `issue_review_completed` for the auto_implement success path, so
        // we must also clear the in-flight marker or the tile's
        // "reviewing…" chip would never go away.
        if (typeof data.issue_id === 'number')
          mutateSet(reviewingIssues, (s) => s.delete(data.issue_id!));
        issueListRefresh.update((n) => n + 1);
        prListRefresh.update((n) => n + 1);
        break;
    }
  });
}
