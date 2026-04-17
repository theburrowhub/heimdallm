import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { connectEvents } from '../lib/sse.js';

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

  // test helpers
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

describe('connectEvents', () => {
  it('opens EventSource at /events', () => {
    connectEvents();
    expect(MockEventSource.instances).toHaveLength(1);
    expect(MockEventSource.instances[0].url).toBe('/events');
  });

  it('pushes typed events into the events store', () => {
    const handle = connectEvents();
    const es = MockEventSource.instances[0];
    const received: unknown[] = [];
    const unsub = handle.events.subscribe((e) => {
      if (e) received.push(e);
    });
    es.emit('pr_detected', JSON.stringify({ id: 1 }));
    es.emit('review_completed', '{"pr_id":1}');
    unsub();
    expect(received).toEqual([
      { type: 'pr_detected', data: { id: 1 } },
      { type: 'review_completed', data: { pr_id: 1 } }
    ]);
  });

  it('connected store reflects open/error transitions', () => {
    const handle = connectEvents();
    const es = MockEventSource.instances[0];
    expect(get(handle.connected)).toBe(false);
    es.fireOpen();
    expect(get(handle.connected)).toBe(true);
    es.fireError();
    expect(get(handle.connected)).toBe(false);
  });

  it('close() calls EventSource.close() and sets connected to false', () => {
    const handle = connectEvents();
    const es = MockEventSource.instances[0];
    es.fireOpen();
    expect(get(handle.connected)).toBe(true);
    handle.close();
    expect(es.closed).toBe(true);
    expect(get(handle.connected)).toBe(false);
  });
});
