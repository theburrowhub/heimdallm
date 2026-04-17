import { fetchMe } from '$lib/api.js';
import { auth } from '$lib/stores.js';
import type { LayoutLoad } from './$types.js';

export const ssr = false; // browser-only — auth state + SSE live in the client

export const load: LayoutLoad = async () => {
  try {
    const me = await fetchMe();
    auth.set({ login: me.login ?? null, authError: null, ready: true });
  } catch (err) {
    const message = err instanceof Error ? err.message : 'daemon unreachable';
    auth.set({ login: null, authError: message, ready: true });
  }
  return {};
};
