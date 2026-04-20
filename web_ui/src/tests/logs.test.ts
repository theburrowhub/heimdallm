import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { connectLogs, detectLevel } from '../lib/logs.js';

// Reuse the mock-EventSource pattern from sse.test.ts so the two SSE clients
// stay symmetric: one wires the broker, this one wires the log tail. The
// fake is local rather than shared because sse.test.ts puts it inline too,
// and keeping tests self-contained makes failures faster to read.
type Listener = (e: MessageEvent) => void;

class MockEventSource {
  static instances: MockEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  readyState = MockEventSource.CONNECTING;
  onopen: ((e: Event) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  closed = false;
  listeners: Record<string, Listener[]> = {};

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }

  addEventListener(type: string, cb: Listener): void {
    (this.listeners[type] ||= []).push(cb);
  }

  close(): void {
    this.closed = true;
    this.readyState = MockEventSource.CLOSED;
  }

  emit(type: string, data: string): void {
    const ev = new MessageEvent(type, { data });
    this.listeners[type]?.forEach((cb) => cb(ev));
  }

  fireOpen(): void {
    this.readyState = MockEventSource.OPEN;
    this.onopen?.(new Event('open'));
  }

  fireError(): void {
    this.onerror?.(new Event('error'));
  }
}

beforeEach(() => {
  MockEventSource.instances = [];
  vi.stubGlobal('EventSource', MockEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('detectLevel', () => {
  it.each([
    ['time=2026-04-17 level=INFO msg="hello"', 'INFO'],
    ['time=2026-04-17 level=WARN msg="hmm"', 'WARN'],
    ['time=2026-04-17 level=ERROR msg="bad"', 'ERROR'],
    ['time=2026-04-17 level=DEBUG msg="trace"', 'DEBUG']
  ])('returns the upper-case level embedded in %s', (line, level) => {
    expect(detectLevel(line)).toBe(level);
  });

  it('returns null for lines without level=', () => {
    expect(detectLevel('this line has no level info')).toBeNull();
  });

  it('does not match lower-case "level=info" — slog emits upper-case and a visual mismatch would drift', () => {
    expect(detectLevel('level=info msg="x"')).toBeNull();
  });
});

describe('connectLogs', () => {
  it('opens EventSource at the default path /api/logs/stream', () => {
    connectLogs();
    expect(MockEventSource.instances).toHaveLength(1);
    expect(MockEventSource.instances[0].url).toBe('/api/logs/stream');
  });

  it('extracts `line` from log_line events and pushes it into the store', () => {
    const handle = connectLogs();
    const es = MockEventSource.instances[0];
    const received: string[] = [];
    const unsub = handle.lines.subscribe((l) => {
      if (l !== null) received.push(l);
    });
    es.emit('log_line', JSON.stringify({ line: 'time=… level=INFO msg="one"' }));
    es.emit('log_line', JSON.stringify({ line: 'time=… level=ERROR msg="boom"' }));
    unsub();
    expect(received).toEqual(['time=… level=INFO msg="one"', 'time=… level=ERROR msg="boom"']);
  });

  it('falls back to the raw payload when log_line data is not JSON', () => {
    const handle = connectLogs();
    const es = MockEventSource.instances[0];
    const received: string[] = [];
    const unsub = handle.lines.subscribe((l) => {
      if (l !== null) received.push(l);
    });
    es.emit('log_line', 'unexpected plain text');
    unsub();
    expect(received).toEqual(['unexpected plain text']);
  });

  it('ignores JSON payloads without a string `line` field', () => {
    const handle = connectLogs();
    const es = MockEventSource.instances[0];
    const received: string[] = [];
    const unsub = handle.lines.subscribe((l) => {
      if (l !== null) received.push(l);
    });
    es.emit('log_line', JSON.stringify({ other: 'ignored' }));
    es.emit('log_line', JSON.stringify({ line: 42 }));
    unsub();
    expect(received).toEqual([]);
  });

  it('connected store reflects open/error transitions', () => {
    const handle = connectLogs();
    const es = MockEventSource.instances[0];
    expect(get(handle.connected)).toBe(false);
    es.fireOpen();
    expect(get(handle.connected)).toBe(true);
    es.fireError();
    expect(get(handle.connected)).toBe(false);
  });

  it('close() stops retry loop and closes the underlying EventSource', () => {
    vi.useFakeTimers();
    try {
      const handle = connectLogs();
      const es = MockEventSource.instances[0];
      es.readyState = MockEventSource.CLOSED;
      es.fireError();
      handle.close();
      // No new EventSource should be opened after close(), even when the
      // scheduled retry timer fires.
      vi.advanceTimersByTime(10_000);
      expect(MockEventSource.instances).toHaveLength(1);
      expect(es.closed).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it('retries with capped exponential backoff after a CLOSED error', () => {
    vi.useFakeTimers();
    try {
      const handle = connectLogs();
      const es = MockEventSource.instances[0];
      es.readyState = MockEventSource.CLOSED;
      es.fireError();
      vi.advanceTimersByTime(2_000);
      expect(MockEventSource.instances).toHaveLength(2);
      handle.close();
    } finally {
      vi.useRealTimers();
    }
  });
});
