import { readable, writable, type Readable } from 'svelte/store';
import type { SseEvent, SseEventType } from './types.js';

const KNOWN_EVENT_TYPES: SseEventType[] = [
  'pr_detected',
  'review_started',
  'review_completed',
  'review_error',
  'issue_detected',
  'issue_review_started',
  'issue_review_completed',
  'issue_review_error',
  'issue_implemented'
];

// Native EventSource auto-reconnects only after a 200 response. Our proxy
// can legitimately respond 502/503 (daemon unreachable / token missing),
// and at that point the browser gives up permanently. This module adds a
// manual retry loop with capped exponential backoff so the UI recovers
// once the daemon comes back.
const INITIAL_RETRY_MS = 2_000;
const MAX_RETRY_MS = 60_000;

export interface EventsHandle {
  events: Readable<SseEvent | null>;
  connected: Readable<boolean>;
  close: () => void;
}

function parse(data: string): unknown {
  try {
    return JSON.parse(data);
  } catch {
    return data;
  }
}

export function connectEvents(path = '/events'): EventsHandle {
  const connected = writable(false);
  let emit: ((e: SseEvent) => void) | undefined;
  const events = readable<SseEvent | null>(null, (set) => {
    emit = (e) => set(e);
    return () => {
      emit = undefined;
    };
  });

  let source: EventSource | undefined;
  let retryMs = INITIAL_RETRY_MS;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let closed = false;

  const open = (): void => {
    if (closed) return;
    source = new EventSource(path);

    source.onopen = () => {
      retryMs = INITIAL_RETRY_MS;
      connected.set(true);
    };

    source.onerror = () => {
      connected.set(false);
      // EventSource will auto-reconnect on its own for 200-terminated streams.
      // For non-200 initial responses the browser stops retrying and moves
      // readyState to CLOSED. Detect that and retry manually with backoff.
      if (source?.readyState === EventSource.CLOSED) {
        source.close();
        source = undefined;
        retryTimer = setTimeout(open, retryMs);
        retryMs = Math.min(retryMs * 2, MAX_RETRY_MS);
      }
    };

    for (const type of KNOWN_EVENT_TYPES) {
      source.addEventListener(type, (ev) => {
        const msg = ev as MessageEvent;
        emit?.({ type, data: parse(msg.data) });
      });
    }
  };

  open();

  return {
    events,
    connected,
    close: () => {
      closed = true;
      if (retryTimer) clearTimeout(retryTimer);
      source?.close();
      source = undefined;
      connected.set(false);
    }
  };
}
