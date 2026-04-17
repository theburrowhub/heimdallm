<script lang="ts">
  import type { Stats } from '$lib/types.js';

  let { stats }: { stats: Stats | null } = $props();

  const cells = $derived([
    { label: 'Total reviews', value: stats?.total_reviews },
    { label: 'Avg findings / review', value: stats?.avg_issues_per_review?.toFixed(1) },
    { label: 'Median time (s)', value: stats?.review_timing?.median_seconds?.toFixed(1) },
    { label: 'High-severity', value: stats?.by_severity?.HIGH ?? 0 }
  ]);
</script>

<section class="grid grid-cols-2 gap-4 md:grid-cols-4">
  {#each cells as cell (cell.label)}
    <div class="rounded-lg border border-gray-200 bg-white p-4">
      <dt class="text-xs uppercase tracking-wide text-gray-500">{cell.label}</dt>
      <dd class="mt-1 text-2xl font-semibold">{cell.value ?? '—'}</dd>
    </div>
  {/each}
</section>
