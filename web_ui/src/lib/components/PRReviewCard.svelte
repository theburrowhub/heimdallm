<script lang="ts">
  import type { Review } from '$lib/types.js';
  import SeverityBadge from './SeverityBadge.svelte';

  let { review }: { review: Review } = $props();
</script>

<article class="mb-3 rounded-md border border-gray-200 bg-white p-4">
  <header class="mb-2 flex items-center justify-between gap-2">
    <div class="flex items-center gap-2">
      <SeverityBadge severity={review.severity} />
      <span class="text-xs text-gray-500">{review.cli_used}</span>
    </div>
    <time class="text-xs text-gray-400" datetime={review.created_at}>
      {new Date(review.created_at).toLocaleString()}
    </time>
  </header>

  {#if review.summary}
    <p class="text-sm text-gray-800">{review.summary}</p>
  {/if}

  {#if review.issues.length > 0}
    <section class="mt-3">
      <h4 class="text-xs font-semibold uppercase tracking-wide text-gray-500">
        Findings ({review.issues.length})
      </h4>
      <ul class="mt-1 space-y-1 text-sm">
        {#each review.issues as f (f.file + f.line + f.description)}
          <li class="flex items-start gap-2">
            <SeverityBadge severity={f.severity} />
            <span class="font-mono text-xs text-gray-500">{f.file}:{f.line}</span>
            <span class="text-gray-800">{f.description}</span>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if review.suggestions.length > 0}
    <section class="mt-3">
      <h4 class="text-xs font-semibold uppercase tracking-wide text-gray-500">Suggestions</h4>
      <ul class="mt-1 list-disc space-y-1 pl-5 text-sm text-gray-800">
        {#each review.suggestions as s, i (i)}
          <li>{typeof s === 'string' ? s : JSON.stringify(s)}</li>
        {/each}
      </ul>
    </section>
  {/if}
</article>
