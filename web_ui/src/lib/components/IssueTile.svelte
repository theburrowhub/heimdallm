<script lang="ts">
  import { reviewingIssues } from '$lib/stores.js';
  import type { Issue } from '$lib/types.js';
  import SeverityBadge from './SeverityBadge.svelte';

  let { issue }: { issue: Issue } = $props();

  const isReviewing = $derived($reviewingIssues.has(issue.id));

  const triage = $derived(issue.latest_review?.triage as { severity?: string } | undefined);
  const severity = $derived(triage?.severity ?? null);

  const mode = $derived.by(() => {
    if (issue.labels.includes('auto_implement')) return 'auto_implement' as const;
    if (issue.labels.includes('review_only')) return 'review_only' as const;
    return null;
  });
</script>

<a href="/issues/{issue.id}" class="block border-b border-gray-100 px-4 py-3 hover:bg-gray-50">
  <div class="flex items-start gap-3">
    <div class="min-w-0 flex-1">
      <p class="truncate text-sm font-medium text-gray-900">{issue.title}</p>
      <p class="mt-0.5 truncate text-xs text-gray-500">
        <span class="font-mono">{issue.repo}</span>
        · #{issue.number}
        · {issue.author}
      </p>
      {#if mode}
        <span
          class="mt-1 inline-flex rounded px-1.5 py-0.5 text-[10px] font-medium
          {mode === 'auto_implement'
            ? 'bg-indigo-100 text-indigo-700'
            : 'bg-blue-100 text-blue-700'}"
        >
          {mode}
        </span>
      {/if}
    </div>
    <div class="flex shrink-0 items-center gap-2">
      {#if isReviewing}
        <span class="text-xs text-indigo-600">reviewing…</span>
      {/if}
      {#if severity}
        <SeverityBadge {severity} />
      {:else}
        <span class="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500">
          PENDING
        </span>
      {/if}
    </div>
  </div>
</a>
