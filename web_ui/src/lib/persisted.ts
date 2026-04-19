import { writable, type Writable } from 'svelte/store';

function subscribeWithSkip<T>(store: Writable<T>, onChange: (v: T) => void): void {
  // Svelte's `writable.subscribe` fires synchronously with the current value —
  // that first callback is a no-op write for persisted stores. Skip it so a
  // cold start doesn't perform a redundant localStorage write on every boot.
  let first = true;
  store.subscribe((v) => {
    if (first) {
      first = false;
      return;
    }
    onChange(v);
  });
}

// SSR-safe. Returns a writable<boolean> backed by localStorage under `key`.
// On first read, if localStorage has a stored value it is parsed; otherwise
// `initial` is used. On every write, the new value is persisted.
export function persistedBoolean(key: string, initial: boolean): Writable<boolean> {
  const start = read(key, initial, (raw) =>
    raw === 'true' ? true : raw === 'false' ? false : null
  );
  const store = writable<boolean>(start);
  subscribeWithSkip(store, (v) => write(key, String(v)));
  return store;
}

export function persistedString(key: string, initial: string): Writable<string> {
  const start = read<string>(key, initial, (raw) => raw);
  const store = writable<string>(start);
  subscribeWithSkip(store, (v) => write(key, v));
  return store;
}

function read<T>(key: string, fallback: T, parse: (raw: string) => T | null): T {
  if (typeof localStorage === 'undefined') return fallback;
  const raw = localStorage.getItem(key);
  if (raw == null) return fallback;
  const parsed = parse(raw);
  return parsed == null ? fallback : parsed;
}

function write(key: string, value: string): void {
  if (typeof localStorage === 'undefined') return;
  localStorage.setItem(key, value);
}
