import { describe, expect, it } from 'vitest';
import { byPriority, byUpdated, desc } from '../lib/sort.js';
import type { PR } from '../lib/types.js';

function mkPR(overrides: Partial<PR> = {}): PR {
  return {
    id: 1,
    github_id: 100,
    repo: 'o/r',
    number: 1,
    title: 't',
    author: 'a',
    url: 'https://github.com/o/r/pull/1',
    state: 'open',
    updated_at: '2026-04-10T00:00:00Z',
    fetched_at: '2026-04-10T00:00:00Z',
    dismissed: false,
    ...overrides
  };
}

describe('byUpdated', () => {
  it('sorts newer first', () => {
    const a = mkPR({ id: 1, updated_at: '2026-04-10T00:00:00Z' });
    const b = mkPR({ id: 2, updated_at: '2026-04-15T00:00:00Z' });
    expect([a, b].sort(byUpdated).map((p) => p.id)).toEqual([2, 1]);
  });

  it('treats missing/malformed dates as oldest', () => {
    const a = mkPR({ id: 1, updated_at: '2026-04-10T00:00:00Z' });
    const b = mkPR({ id: 2, updated_at: 'not-a-date' as string });
    expect([a, b].sort(byUpdated).map((p) => p.id)).toEqual([1, 2]);
  });

  it('is stable for equal timestamps', () => {
    const a = mkPR({ id: 1, updated_at: '2026-04-10T00:00:00Z' });
    const b = mkPR({ id: 2, updated_at: '2026-04-10T00:00:00Z' });
    const c = mkPR({ id: 3, updated_at: '2026-04-10T00:00:00Z' });
    expect([a, b, c].sort(byUpdated).map((p) => p.id)).toEqual([1, 2, 3]);
  });
});

describe('byPriority', () => {
  it('sorts critical highest, unknown lowest', () => {
    const low = mkPR({ id: 1, latest_review: { severity: 'low' } as PR['latest_review'] });
    const critical = mkPR({
      id: 2,
      latest_review: { severity: 'critical' } as PR['latest_review']
    });
    const medium = mkPR({ id: 3, latest_review: { severity: 'medium' } as PR['latest_review'] });
    const none = mkPR({ id: 4 });
    const high = mkPR({ id: 5, latest_review: { severity: 'high' } as PR['latest_review'] });
    expect([low, critical, medium, none, high].sort(byPriority).map((p) => p.id)).toEqual([
      2, 5, 3, 1, 4
    ]);
  });

  it('tiebreaks equal severity by updated_at desc', () => {
    const older = mkPR({
      id: 1,
      updated_at: '2026-04-01T00:00:00Z',
      latest_review: { severity: 'high' } as PR['latest_review']
    });
    const newer = mkPR({
      id: 2,
      updated_at: '2026-04-10T00:00:00Z',
      latest_review: { severity: 'high' } as PR['latest_review']
    });
    expect([older, newer].sort(byPriority).map((p) => p.id)).toEqual([2, 1]);
  });
});

describe('desc(field)', () => {
  it('creates a descending comparator for a date-typed field', () => {
    const cmp = desc<{ fetched_at: string }>('fetched_at');
    const a = { fetched_at: '2026-04-01T00:00:00Z' };
    const b = { fetched_at: '2026-04-10T00:00:00Z' };
    expect([a, b].sort(cmp)).toEqual([b, a]);
  });
});
