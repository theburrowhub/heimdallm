import { writable } from 'svelte/store';

export interface AuthState {
  login: string | null;
  authError: string | null;
  ready: boolean;
}

export const auth = writable<AuthState>({ login: null, authError: null, ready: false });
