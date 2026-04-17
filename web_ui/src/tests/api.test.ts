import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, fetchPR, fetchPRs, fetchStats, triggerReview, upsertAgent } from '../lib/api.js';

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function okJson(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' }
  });
}

describe('api.ts', () => {
  it('fetchPRs GETs /api/prs and returns a typed array', async () => {
    fetchMock.mockResolvedValue(okJson([{ id: 1, number: 42, title: 't' }]));
    const prs = await fetchPRs();
    expect(fetchMock).toHaveBeenCalledWith('/api/prs', expect.objectContaining({ method: 'GET' }));
    expect(prs).toHaveLength(1);
    expect(prs[0].id).toBe(1);
  });

  it('fetchPR parses stringified issues/suggestions in latest_review', async () => {
    fetchMock.mockResolvedValue(
      okJson({
        pr: {
          id: 1,
          latest_review: {
            id: 9,
            issues: '[{"file":"a.go","line":1,"description":"x","severity":"LOW"}]',
            suggestions: '["do the thing"]'
          }
        },
        reviews: [{ id: 9, issues: '[]', suggestions: '[]' }]
      })
    );
    const detail = await fetchPR(1);
    expect(detail.pr.latest_review!.issues).toEqual([
      { file: 'a.go', line: 1, description: 'x', severity: 'LOW' }
    ]);
    expect(detail.pr.latest_review!.suggestions).toEqual(['do the thing']);
    expect(detail.reviews[0].issues).toEqual([]);
    expect(detail.reviews[0].suggestions).toEqual([]);
  });

  it('triggerReview POSTs and resolves on 202', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 202 }));
    await expect(triggerReview(7)).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/prs/7/review',
      expect.objectContaining({ method: 'POST' })
    );
  });

  it('triggerReview throws ApiError on 500', async () => {
    const make500 = () => new Response('boom', { status: 500, statusText: 'Server Error' });
    fetchMock.mockResolvedValueOnce(make500());
    await expect(triggerReview(7)).rejects.toBeInstanceOf(ApiError);
    fetchMock.mockResolvedValueOnce(make500());
    await expect(triggerReview(7)).rejects.toMatchObject({
      status: 500,
      path: '/api/prs/7/review'
    });
  });

  it('upsertAgent POSTs JSON body with correct content-type', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 200 }));
    await upsertAgent({
      id: 'a',
      name: 'A',
      cli: 'claude',
      prompt: '',
      instructions: 'Review carefully.',
      cli_flags: '',
      is_default: false,
      created_at: '2026-04-17T00:00:00Z'
    });
    const [, init] = fetchMock.mock.calls[0];
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    expect(JSON.parse(init.body as string)).toMatchObject({ id: 'a', name: 'A', cli: 'claude' });
  });

  it('fetchStats returns parsed JSON object', async () => {
    fetchMock.mockResolvedValue(
      okJson({
        total_reviews: 3,
        by_severity: { HIGH: 1 },
        by_cli: {},
        top_repos: [],
        reviews_last_7_days: [],
        avg_issues_per_review: 1.5,
        review_timing: {
          sample_count: 3,
          avg_seconds: 50,
          median_seconds: 42,
          min_seconds: 10,
          max_seconds: 90,
          bucket_fast: 1,
          bucket_medium: 1,
          bucket_slow: 1,
          bucket_very_slow: 0
        }
      })
    );
    const stats = await fetchStats();
    expect(stats.total_reviews).toBe(3);
    expect(stats.avg_issues_per_review).toBe(1.5);
    expect(stats.review_timing.median_seconds).toBe(42);
  });
});
