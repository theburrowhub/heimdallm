import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { persistedBoolean, persistedString } from '../lib/persisted.js';

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  localStorage.clear();
});

describe('persistedBoolean', () => {
  it('uses default when key is absent', () => {
    const s = persistedBoolean('key1', true);
    expect(get(s)).toBe(true);
  });

  it('reads existing value from localStorage', () => {
    localStorage.setItem('key2', 'false');
    const s = persistedBoolean('key2', true);
    expect(get(s)).toBe(false);
  });

  it('persists on write', () => {
    const s = persistedBoolean('key3', true);
    s.set(false);
    expect(localStorage.getItem('key3')).toBe('false');
  });

  it('tolerates malformed stored value by falling back to default', () => {
    localStorage.setItem('key4', 'not a bool');
    const s = persistedBoolean('key4', true);
    expect(get(s)).toBe(true);
  });
});

describe('persistedString', () => {
  it('uses default when key is absent', () => {
    const s = persistedString('sk1', 'newest');
    expect(get(s)).toBe('newest');
  });

  it('persists on write', () => {
    const s = persistedString('sk2', 'newest');
    s.set('priority');
    expect(localStorage.getItem('sk2')).toBe('priority');
  });
});

describe('subscribe behavior', () => {
  it('does not re-write the initial value on cold start', () => {
    const spy = vi.spyOn(Storage.prototype, 'setItem');
    persistedBoolean('coldStartKey', true);
    expect(spy).not.toHaveBeenCalledWith('coldStartKey', 'true');
    spy.mockRestore();
  });
});
