import { get, writable } from 'svelte/store';
import { beforeEach, describe, expect, it } from 'vitest';
import { issueListRefresh, prListRefresh, reviewingIssues, reviewingPRs } from '../lib/stores.js';
import { initSseBridge, watchReconnectAndSweep } from '../lib/sseBridge.js';
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

describe('watchReconnectAndSweep', () => {
  it('clears reviewingPRs and reviewingIssues on false→true transition', () => {
    const connected = writable(false);
    watchReconnectAndSweep({ subscribe: connected.subscribe });

    // Prime both sets.
    reviewingPRs.set(new Set([1, 2]));
    reviewingIssues.set(new Set([3]));

    // No transition yet — sets should be untouched.
    expect(get(reviewingPRs).size).toBe(2);
    expect(get(reviewingIssues).size).toBe(1);

    // Still disconnected — no sweep.
    connected.set(false);
    expect(get(reviewingPRs).size).toBe(2);

    // First connect — counts as reconnect (false → true).
    connected.set(true);
    expect(get(reviewingPRs).size).toBe(0);
    expect(get(reviewingIssues).size).toBe(0);
  });

  it('does not sweep on the very first emission if it is true', () => {
    const connected = writable(true);
    reviewingPRs.set(new Set([99]));
    watchReconnectAndSweep({ subscribe: connected.subscribe });
    expect(get(reviewingPRs).size).toBe(1);
  });

  it('sweeps on every subsequent false→true transition', () => {
    const connected = writable(false);
    watchReconnectAndSweep({ subscribe: connected.subscribe });

    reviewingPRs.set(new Set([1]));
    connected.set(true);
    expect(get(reviewingPRs).size).toBe(0);

    reviewingPRs.set(new Set([2]));
    connected.set(false);
    connected.set(true);
    expect(get(reviewingPRs).size).toBe(0);
  });
});
