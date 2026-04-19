import { severityOrder } from './severity.js';
import type { PR } from './types.js';

// Parses an ISO date to epoch millis, returning 0 for empty/malformed input
// so sorts are stable and predictable even if the daemon serves partial data.
function timestamp(value: string | undefined): number {
  if (!value) return 0;
  const t = new Date(value).getTime();
  return Number.isNaN(t) ? 0 : t;
}

export function byUpdated(a: PR, b: PR): number {
  return timestamp(b.updated_at) - timestamp(a.updated_at);
}

export function byPriority(a: PR, b: PR): number {
  const d =
    severityOrder(b.latest_review?.severity ?? '') - severityOrder(a.latest_review?.severity ?? '');
  return d !== 0 ? d : byUpdated(a, b);
}

// Generic descending-by-date comparator factory. Used for issues where the
// only sort key is `fetched_at` (issues have no `updated_at` on the wire).
export function desc<T extends object>(field: keyof T): (a: T, b: T) => number {
  return (a, b) =>
    timestamp(String((b[field] as string | undefined) ?? '')) -
    timestamp(String((a[field] as string | undefined) ?? ''));
}
