<script lang="ts">
  import '../app.css';
  import { browser } from '$app/environment';
  import { page } from '$app/stores';
  import { connectEvents, type EventsHandle } from '$lib/sse.js';
  import { initSseBridge, watchReconnectAndSweep } from '$lib/sseBridge.js';
  import { auth } from '$lib/stores.js';
  import { initTheme, setThemeChoice, type ThemeChoice } from '$lib/theme.js';
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

  let themeChoice = $state<ThemeChoice>('system');

  onMount(() => {
    if (!browser) return;
    themeChoice = initTheme();
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

  function chooseTheme(choice: ThemeChoice) {
    themeChoice = choice;
    setThemeChoice(choice);
  }

  const navItems = [
    { href: '/', label: 'Dashboard' },
    { href: '/prs', label: 'PRs' },
    { href: '/issues', label: 'Issues' },
    { href: '/agents', label: 'Agents' },
    { href: '/config', label: 'Config' },
    { href: '/logs', label: 'Logs' }
  ];

  const themeOptions: { value: ThemeChoice; title: string }[] = [
    { value: 'light', title: 'Light' },
    { value: 'system', title: 'System' },
    { value: 'dark', title: 'Dark' }
  ];
</script>

<header class="border-b border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
  <nav class="mx-auto flex max-w-6xl items-center justify-between gap-4 px-6 py-3">
    <a
      href="/"
      class="flex items-center gap-2 text-lg font-semibold tracking-tight text-gray-900 dark:text-gray-100"
    >
      <img src="/icon.png" alt="" width="24" height="24" class="h-6 w-6 shrink-0" />
      Heimdallm
    </a>
    <ul class="flex items-center gap-4 text-sm">
      {#each navItems as item (item.href)}
        <li>
          <a
            href={item.href}
            aria-current={$page.url.pathname === item.href ? 'page' : undefined}
            class="hover:text-indigo-600 dark:hover:text-indigo-400 {$page.url.pathname ===
            item.href
              ? 'font-medium text-indigo-600 dark:text-indigo-400'
              : 'text-gray-600 dark:text-gray-400'}"
          >
            {item.label}
          </a>
        </li>
      {/each}
    </ul>
    <div class="flex items-center gap-3">
      <div
        aria-label="Theme"
        class="inline-flex overflow-hidden rounded-md border border-gray-200 text-xs dark:border-gray-700"
      >
        <!--
          Not using role="radiogroup" / role="radio": the WAI-ARIA radio
          pattern requires arrow-key navigation between options, which adds
          complexity this simple 3-choice toggle does not need. Plain
          buttons with aria-pressed convey the same state to AT and keep
          Tab-navigation behaviour unchanged.
        -->
        {#each themeOptions as opt (opt.value)}
          <button
            type="button"
            aria-pressed={themeChoice === opt.value}
            title={opt.title}
            onclick={() => chooseTheme(opt.value)}
            class="flex items-center justify-center px-2 py-1.5 focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500 focus-visible:ring-inset {themeChoice ===
            opt.value
              ? 'bg-indigo-600 text-white dark:bg-indigo-500'
              : 'bg-white text-gray-600 hover:bg-gray-50 dark:bg-gray-900 dark:text-gray-400 dark:hover:bg-gray-800'}"
          >
            {#if opt.value === 'light'}
              <svg
                class="h-4 w-4"
                viewBox="0 0 20 20"
                fill="none"
                stroke="currentColor"
                stroke-width="1.6"
                stroke-linecap="round"
                aria-hidden="true"
              >
                <circle cx="10" cy="10" r="3.2" />
                <path d="M10 2.5v1.8M10 15.7v1.8M3.5 10H1.7M18.3 10h-1.8" />
                <path d="M5.2 5.2l1.3 1.3M13.5 13.5l1.3 1.3M5.2 14.8l1.3-1.3M13.5 6.5l1.3-1.3" />
              </svg>
            {:else if opt.value === 'system'}
              <svg
                class="h-4 w-4"
                viewBox="0 0 20 20"
                fill="none"
                stroke="currentColor"
                stroke-width="1.6"
                stroke-linejoin="round"
                aria-hidden="true"
              >
                <rect x="2.5" y="3.5" width="15" height="10" rx="1.5" />
                <path d="M7 17h6M10 13.5V17" stroke-linecap="round" />
              </svg>
            {:else}
              <svg
                class="h-4 w-4"
                viewBox="0 0 20 20"
                fill="none"
                stroke="currentColor"
                stroke-width="1.6"
                stroke-linecap="round"
                stroke-linejoin="round"
                aria-hidden="true"
              >
                <path d="M16.5 12.3A7 7 0 1 1 7.7 3.5a5.6 5.6 0 0 0 8.8 8.8z" />
              </svg>
            {/if}
            <span class="sr-only">{opt.title}</span>
          </button>
        {/each}
      </div>
      <span class="text-sm text-gray-500 dark:text-gray-400" data-testid="login">
        {$auth.login ?? '—'}
      </span>
    </div>
  </nav>
</header>

{#if $auth.authError}
  <div
    class="border-b border-red-200 bg-red-50 px-6 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300"
  >
    Daemon unreachable — check <code>HEIMDALLM_API_URL</code>. ({$auth.authError})
  </div>
{/if}

<main class="mx-auto max-w-6xl px-6 py-8">
  {@render children()}
</main>
