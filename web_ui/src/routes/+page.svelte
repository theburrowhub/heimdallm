<script lang="ts">
  import { browser } from '$app/environment';
  import { fetchStats } from '$lib/api.js';
  import type { Stats } from '$lib/types.js';
  import { getContext, onMount } from 'svelte';
  import type { Readable } from 'svelte/store';

  const connected = getContext<Readable<boolean>>('sse-connected');

  let stats = $state<Stats | null>(null);
  let statsError = $state<string | null>(null);

  onMount(async () => {
    if (!browser) return;
    try {
      stats = await fetchStats();
    } catch (e) {
      statsError = e instanceof Error ? e.message : String(e);
    }
  });

  const pillClass = $derived(
    $connected ? 'bg-green-100 text-green-700' : 'bg-amber-100 text-amber-700'
  );
  const pillLabel = $derived($connected ? 'connected' : 'disconnected');
</script>

<section class="flex items-center gap-3">
  <h1 class="text-3xl font-bold">Heimdallm</h1>
  <span class="rounded-full px-2 py-0.5 text-xs font-medium {pillClass}" data-testid="sse-pill">
    {pillLabel}
  </span>
</section>

<p class="mt-1 text-sm text-gray-500">v0.1 scaffold — dashboard lands in #31.</p>

<section class="mt-8 grid grid-cols-2 gap-4 md:grid-cols-4">
  {#each [{ label: 'Total reviews', value: stats?.total_reviews }, { label: 'Avg findings / review', value: stats?.avg_issues_per_review?.toFixed(1) }, { label: 'Median time (s)', value: stats?.review_timing?.median_seconds?.toFixed(1) }, { label: 'High-severity', value: stats?.by_severity?.HIGH ?? 0 }] as cell (cell.label)}
    <div class="rounded-lg border border-gray-200 bg-white p-4">
      <dt class="text-xs uppercase tracking-wide text-gray-500">{cell.label}</dt>
      <dd class="mt-1 text-2xl font-semibold">
        {cell.value ?? '—'}
      </dd>
    </div>
  {/each}
</section>

{#if statsError}
  <p class="mt-4 text-sm text-red-600">Could not load stats: {statsError}</p>
{/if}
