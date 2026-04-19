<script lang="ts">
  import { reviewingPRs } from '$lib/stores.js';
  import type { PR } from '$lib/types.js';
  import SeverityBadge from './SeverityBadge.svelte';

  let { pr }: { pr: PR } = $props();

  const isReviewing = $derived($reviewingPRs.has(pr.id));
  const severity = $derived(pr.latest_review?.severity ?? null);
</script>

<a href="/prs/{pr.id}" class="block border-b border-gray-100 px-4 py-3 hover:bg-gray-50">
  <div class="flex items-start gap-3">
    <div class="min-w-0 flex-1">
      <p class="truncate text-sm font-medium text-gray-900">{pr.title}</p>
      <p class="mt-0.5 truncate text-xs text-gray-500">
        <span class="font-mono">{pr.repo}</span>
        · #{pr.number}
        · {pr.author}
      </p>
    </div>
    <div class="flex shrink-0 items-center gap-2">
      {#if isReviewing}
        <span class="text-xs text-indigo-600" data-testid="reviewing-spinner">reviewing…</span>
      {/if}
      <SeverityBadge {severity} />
    </div>
  </div>
</a>
