<script lang="ts">
  import '../app.css';
  import { browser } from '$app/environment';
  import { page } from '$app/stores';
  import { connectEvents, type EventsHandle } from '$lib/sse.js';
  import { initSseBridge, watchReconnectAndSweep } from '$lib/sseBridge.js';
  import { auth } from '$lib/stores.js';
  import { onDestroy, onMount, setContext } from 'svelte';
  import { writable, type Readable } from 'svelte/store';

  let { children } = $props();

  // Shared connection state. Populated by onMount; exposed to child pages
  // via context so they don't open their own EventSource.
  const connected = writable(false);
  setContext<Readable<boolean>>('sse-connected', { subscribe: connected.subscribe });

  let sse: EventsHandle | undefined;
  let connUnsub: (() => void) | undefined;
  let bridgeUnsub: (() => void) | undefined;
  let reconnectUnsub: (() => void) | undefined;

  onMount(() => {
    if (!browser) return;
    sse = connectEvents();
    connUnsub = sse.connected.subscribe((v) => connected.set(v));
    bridgeUnsub = initSseBridge(sse.events);
    reconnectUnsub = watchReconnectAndSweep(sse.connected);
  });

  onDestroy(() => {
    reconnectUnsub?.();
    bridgeUnsub?.();
    connUnsub?.();
    sse?.close();
  });

  const navItems = [
    { href: '/', label: 'Dashboard' },
    { href: '/prs', label: 'PRs' },
    { href: '/issues', label: 'Issues' },
    { href: '/agents', label: 'Agents' },
    { href: '/config', label: 'Config' },
    { href: '/logs', label: 'Logs' }
  ];
</script>

<header class="border-b border-gray-200 bg-white">
  <nav class="mx-auto flex max-w-6xl items-center justify-between gap-4 px-6 py-3">
    <a href="/" class="text-lg font-semibold tracking-tight">Heimdallm</a>
    <ul class="flex items-center gap-4 text-sm">
      {#each navItems as item (item.href)}
        <li>
          <a
            href={item.href}
            aria-current={$page.url.pathname === item.href ? 'page' : undefined}
            class="hover:text-indigo-600 {$page.url.pathname === item.href
              ? 'text-indigo-600 font-medium'
              : 'text-gray-600'}"
          >
            {item.label}
          </a>
        </li>
      {/each}
    </ul>
    <span class="text-sm text-gray-500" data-testid="login">
      {$auth.login ?? '—'}
    </span>
  </nav>
</header>

{#if $auth.authError}
  <div class="border-b border-red-200 bg-red-50 px-6 py-2 text-sm text-red-700">
    Daemon unreachable — check <code>HEIMDALLM_API_URL</code>. ({$auth.authError})
  </div>
{/if}

<main class="mx-auto max-w-6xl px-6 py-8">
  {@render children()}
</main>
