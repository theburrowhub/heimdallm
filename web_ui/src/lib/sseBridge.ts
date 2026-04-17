import type { Readable } from 'svelte/store';
import { issueListRefresh, prListRefresh, reviewingIssues, reviewingPRs } from './stores.js';
import type { SseEvent } from './types.js';

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
        // New issue→PR link: refresh both lists.
        issueListRefresh.update((n) => n + 1);
        prListRefresh.update((n) => n + 1);
        break;
    }
  });
}
