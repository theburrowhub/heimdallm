// Log line streaming client for the daemon's GET /logs/stream SSE endpoint.
// Kept separate from sse.ts, which owns the broker's review-events channel:
// the log stream carries a different event type (`log_line`) and a different
// payload shape (`{"line": "..."}`), and is cheap to open/close on demand
// from a single page, so mixing it into the shared global connection would
// add complexity without payoff.

import { readable, writable, type Readable } from 'svelte/store';

const INITIAL_RETRY_MS = 2_000;
const MAX_RETRY_MS = 30_000;

export interface LogsHandle {
  lines: Readable<string | null>;
  connected: Readable<boolean>;
  close: () => void;
}

interface LogLinePayload {
  line: string;
}

function parsePayload(data: string): string | null {
  try {
    const parsed = JSON.parse(data) as Partial<LogLinePayload>;
    if (typeof parsed.line === 'string') return parsed.line;
    return null;
  } catch {
    // Fall back to the raw data so the viewer still shows something useful
    // if the daemon ever starts emitting non-JSON lines (a bug we'd want to
    // surface rather than silently swallow).
    return data;
  }
}

/**
 * connectLogs opens a long-lived EventSource against /api/logs/stream and
 * exposes every incoming log line through the `lines` store. The store emits
 * each line by mutating to a new value — consumers should subscribe rather
 * than read snapshot, same pattern sse.ts uses for broker events.
 *
 * Connection is retried with capped exponential backoff when the daemon is
 * briefly unreachable; `connected` flips false during outages so the page
 * can show a reconnect banner.
 */
export function connectLogs(path = '/api/logs/stream'): LogsHandle {
  const connected = writable(false);
  let emit: ((line: string) => void) | undefined;
  const lines = readable<string | null>(null, (set) => {
    emit = (line) => set(line);
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
      if (source?.readyState === EventSource.CLOSED) {
        source.close();
        source = undefined;
        retryTimer = setTimeout(open, retryMs);
        retryMs = Math.min(retryMs * 2, MAX_RETRY_MS);
      }
    };

    source.addEventListener('log_line', (ev) => {
      const msg = ev as MessageEvent;
      const line = parsePayload(msg.data);
      if (line !== null) emit?.(line);
    });
  };

  open();

  return {
    lines,
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

// ---------------------------------------------------------------------------
// Level detection
// ---------------------------------------------------------------------------

export type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';

// Matches slog's default text handler output (`level=INFO`, `level=WARN`, …).
// Case-sensitive — slog emits upper-case; we intentionally do not accept
// mixed case so the visual level never drifts from what's in the file.
const LEVEL_RE = /\blevel=(DEBUG|INFO|WARN|ERROR)\b/;

export function detectLevel(line: string): LogLevel | null {
  const m = line.match(LEVEL_RE);
  return m ? (m[1] as LogLevel) : null;
}
