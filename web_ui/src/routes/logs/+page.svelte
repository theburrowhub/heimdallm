<script lang="ts">
  import { browser } from '$app/environment';
  import { connectLogs, detectLevel, type LogLevel, type LogsHandle } from '$lib/logs.js';
  import { onDestroy } from 'svelte';

  // How many lines we hold in memory. Enough to scroll back meaningfully
  // without letting a chatty daemon eat a GB of heap after an hour.
  const MAX_LINES = 2000;

  interface Entry {
    idx: number;
    text: string;
    level: LogLevel | null;
  }

  let entries: Entry[] = $state([]);
  let wrap = $state(false);
  let autoScroll = $state(true);
  let connected = $state(false);
  let viewport: HTMLDivElement | undefined = $state();
  let nextIdx = 0;

  let logsHandle: LogsHandle | undefined;
  let linesUnsub: (() => void) | undefined;
  let connUnsub: (() => void) | undefined;

  $effect(() => {
    if (!browser) return;
    logsHandle = connectLogs();
    linesUnsub = logsHandle.lines.subscribe((line) => {
      if (line === null) return;
      const entry: Entry = { idx: nextIdx++, text: line, level: detectLevel(line) };
      entries = entries.length >= MAX_LINES ? [...entries.slice(1), entry] : [...entries, entry];
      // Schedule scroll after Svelte flushes the DOM update; checking inside
      // onAfterUpdate would require a newer API. setTimeout(0) is enough.
      if (autoScroll) {
        setTimeout(scrollToBottom, 0);
      }
    });
    connUnsub = logsHandle.connected.subscribe((v) => (connected = v));
    return () => {
      linesUnsub?.();
      connUnsub?.();
      logsHandle?.close();
    };
  });

  onDestroy(() => {
    linesUnsub?.();
    connUnsub?.();
    logsHandle?.close();
  });

  function scrollToBottom(): void {
    if (!viewport) return;
    viewport.scrollTop = viewport.scrollHeight;
  }

  // When the user scrolls away from the bottom, pause auto-scroll so they
  // can read history without being yanked back. Resume when they scroll
  // back within a 24-pixel dead zone of the bottom.
  function onScroll(): void {
    if (!viewport) return;
    const distanceFromBottom = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight;
    autoScroll = distanceFromBottom < 24;
  }

  function clear(): void {
    entries = [];
    nextIdx = 0;
  }

  function jumpToBottom(): void {
    autoScroll = true;
    scrollToBottom();
  }

  function levelClass(level: LogLevel | null): string {
    switch (level) {
      case 'ERROR':
        return 'text-red-700';
      case 'WARN':
        return 'text-amber-700';
      case 'INFO':
        return 'text-gray-800';
      case 'DEBUG':
        return 'text-gray-500';
      default:
        return 'text-gray-700';
    }
  }
</script>

<section class="space-y-4">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold">Logs</h1>
      <p class="text-sm text-gray-500">
        Live daemon log stream. Keeps the last {MAX_LINES} lines in memory.
      </p>
    </div>
    <div class="flex items-center gap-3 text-sm">
      <span class="flex items-center gap-1">
        <span
          aria-hidden="true"
          class="inline-block h-2 w-2 rounded-full {connected ? 'bg-green-500' : 'bg-red-500'}"
        ></span>
        {connected ? 'Connected' : 'Reconnecting…'}
      </span>
      <label class="flex items-center gap-1">
        <input type="checkbox" bind:checked={wrap} data-testid="toggle-wrap" />
        Wrap
      </label>
      <button
        class="rounded border border-gray-300 px-2 py-1 hover:bg-gray-50"
        onclick={clear}
        data-testid="clear-logs"
      >
        Clear
      </button>
      {#if !autoScroll}
        <button
          class="rounded bg-indigo-600 px-2 py-1 text-white hover:bg-indigo-500"
          onclick={jumpToBottom}
          data-testid="jump-bottom"
        >
          ↓ Follow
        </button>
      {/if}
    </div>
  </header>

  <div
    bind:this={viewport}
    onscroll={onScroll}
    class="h-[70vh] overflow-auto rounded border border-gray-200 bg-gray-950 p-3 font-mono text-xs text-gray-100"
    data-testid="logs-viewport"
  >
    {#if entries.length === 0}
      <p class="text-gray-500">Waiting for log lines…</p>
    {:else}
      {#each entries as entry (entry.idx)}
        <div
          class="{wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre'} {levelClass(
            entry.level
          )}"
          data-level={entry.level ?? ''}
        >
          {entry.text}
        </div>
      {/each}
    {/if}
  </div>
</section>
