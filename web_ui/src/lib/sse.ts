import { readable, writable, type Readable } from 'svelte/store';
import type { SseEvent, SseEventType } from './types.js';

const KNOWN_EVENT_TYPES: SseEventType[] = [
  'pr_detected',
  'review_started',
  'review_completed',
  'review_error'
];

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

  const source = new EventSource(path);

  source.onopen = () => connected.set(true);
  source.onerror = () => connected.set(false);

  for (const type of KNOWN_EVENT_TYPES) {
    source.addEventListener(type, (ev) => {
      const msg = ev as MessageEvent;
      emit?.({ type, data: parse(msg.data) });
    });
  }

  return {
    events,
    connected,
    close: () => {
      source.close();
      connected.set(false);
    }
  };
}
