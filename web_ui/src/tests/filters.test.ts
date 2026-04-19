import { describe, expect, it } from 'vitest';
import { filterIssues, filterPRs } from '../lib/filters.js';
import type { Issue, PR } from '../lib/types.js';

function mkPR(overrides: Partial<PR> = {}): PR {
  return {
    id: 1,
    github_id: 1,
    repo: 'o/r',
    number: 1,
    title: 't',
    author: 'a',
    url: 'u',
    state: 'open',
    updated_at: '2026-04-10T00:00:00Z',
    fetched_at: '2026-04-10T00:00:00Z',
    dismissed: false,
    ...overrides
  };
}

function mkIssue(overrides: Partial<Issue> = {}): Issue {
  return {
    id: 1,
    github_id: 1,
    repo: 'o/r',
    number: 1,
    title: 't',
    body: '',
    author: 'a',
    assignees: [],
    labels: [],
    state: 'open',
    created_at: '2026-04-10T00:00:00Z',
    fetched_at: '2026-04-10T00:00:00Z',
    dismissed: false,
    ...overrides
  };
}

describe('filterPRs', () => {
  it('filters by repo when set, passes through when empty', () => {
    const prs = [mkPR({ id: 1, repo: 'a/b' }), mkPR({ id: 2, repo: 'c/d' })];
    expect(
      filterPRs(prs, { repo: 'a/b', severity: 'any', state: 'open' }).map((p) => p.id)
    ).toEqual([1]);
    expect(filterPRs(prs, { repo: '', severity: 'any', state: 'open' }).map((p) => p.id)).toEqual([
      1, 2
    ]);
  });

  it('filters by state case-insensitively; "all" is passthrough', () => {
    const prs = [mkPR({ id: 1, state: 'open' }), mkPR({ id: 2, state: 'CLOSED' })];
    expect(filterPRs(prs, { repo: '', severity: 'any', state: 'open' }).map((p) => p.id)).toEqual([
      1
    ]);
    expect(filterPRs(prs, { repo: '', severity: 'any', state: 'closed' }).map((p) => p.id)).toEqual(
      [2]
    );
    expect(filterPRs(prs, { repo: '', severity: 'any', state: 'all' }).map((p) => p.id)).toEqual([
      1, 2
    ]);
  });

  it('filters by severity case-insensitively; "any" is passthrough', () => {
    const prs = [
      mkPR({ id: 1, latest_review: { severity: 'HIGH' } as PR['latest_review'] }),
      mkPR({ id: 2, latest_review: { severity: 'low' } as PR['latest_review'] }),
      mkPR({ id: 3 })
    ];
    expect(filterPRs(prs, { repo: '', severity: 'high', state: 'all' }).map((p) => p.id)).toEqual([
      1
    ]);
    expect(filterPRs(prs, { repo: '', severity: 'any', state: 'all' }).map((p) => p.id)).toEqual([
      1, 2, 3
    ]);
  });
});

describe('filterIssues', () => {
  it('drops dismissed issues always', () => {
    const issues = [mkIssue({ id: 1 }), mkIssue({ id: 2, dismissed: true })];
    expect(
      filterIssues(issues, { repo: '', severity: 'any', mode: 'all' }).map((i) => i.id)
    ).toEqual([1]);
  });

  it('filters by mode via labels; "all" is passthrough', () => {
    const issues = [
      mkIssue({ id: 1, labels: ['auto_implement'] }),
      mkIssue({ id: 2, labels: ['review_only'] }),
      mkIssue({ id: 3, labels: ['feature'] })
    ];
    expect(
      filterIssues(issues, { repo: '', severity: 'any', mode: 'auto_implement' }).map((i) => i.id)
    ).toEqual([1]);
    expect(
      filterIssues(issues, { repo: '', severity: 'any', mode: 'all' }).map((i) => i.id)
    ).toEqual([1, 2, 3]);
  });

  it('filters by severity from triage, case-insensitive', () => {
    const issues = [
      mkIssue({ id: 1, latest_review: { triage: { severity: 'HIGH' } } as Issue['latest_review'] }),
      mkIssue({ id: 2, latest_review: { triage: { severity: 'low' } } as Issue['latest_review'] }),
      mkIssue({ id: 3 })
    ];
    expect(
      filterIssues(issues, { repo: '', severity: 'high', mode: 'all' }).map((i) => i.id)
    ).toEqual([1]);
  });
});
