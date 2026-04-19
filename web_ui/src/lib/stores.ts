import { writable } from 'svelte/store';

export interface AuthState {
  login: string | null;
  authError: string | null;
  ready: boolean;
}

export const auth = writable<AuthState>({ login: null, authError: null, ready: false });

// Refresh counters — incrementing these forces list fetchers to re-run.
// Used by the SSE bridge (see sseBridge.ts) and by any page that needs
// to invalidate after a mutation.
export const prListRefresh = writable(0);
export const issueListRefresh = writable(0);

// In-flight review trackers. A PR id is present while a review is
// running, so tiles and buttons can show a spinner.
export const reviewingPRs = writable<Set<number>>(new Set());
export const reviewingIssues = writable<Set<number>>(new Set());
