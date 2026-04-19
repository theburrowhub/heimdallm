<script lang="ts">
  import { browser } from '$app/environment';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import PRReviewCard from '$lib/components/PRReviewCard.svelte';
  import { dismissPR, fetchPR, triggerReview, undismissPR } from '$lib/api.js';
  import { prListRefresh, reviewingPRs } from '$lib/stores.js';
  import type { PR, Review } from '$lib/types.js';

  const id = $derived(Number($page.params.id));

  let pr: PR | null = $state(null);
  let reviews: Review[] = $state([]);
  let err: string | null = $state(null);
  let busy: boolean = $state(false);

  $effect(() => {
    void $prListRefresh; // re-run on SSE-driven bump
    if (!browser || !Number.isFinite(id)) return;
    pr = null;
    reviews = [];
    err = null;
    fetchPR(id)
      .then((d) => {
        pr = d.pr;
        reviews = d.reviews;
        err = null;
      })
      .catch((e) => {
        err = e instanceof Error ? e.message : String(e);
      });
  });

  const reviewing = $derived($reviewingPRs.has(id));

  async function onReview(): Promise<void> {
    if (!pr) return;
    busy = true;
    try {
      // Optimistic: mark reviewing before SSE confirms.
      reviewingPRs.update((s) => new Set(s).add(id));
      await triggerReview(id);
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
      reviewingPRs.update((s) => {
        const n = new Set(s);
        n.delete(id);
        return n;
      });
    } finally {
      busy = false;
    }
  }

  async function onDismissToggle(): Promise<void> {
    if (!pr) return;
    busy = true;
    try {
      if (pr.dismissed) {
        await undismissPR(id);
        prListRefresh.update((n) => n + 1);
      } else {
        await dismissPR(id);
        // Dismissed PRs disappear from /prs; navigate back.
        // Let the `finally` block run so `busy` is reset even if goto rejects.
        void goto('/prs');
      }
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if err && !pr}
  <p class="text-sm text-red-600">Could not load PR: {err}</p>
{:else if !pr}
  <p class="text-sm text-gray-500">Loading…</p>
{:else}
  <article>
    <header class="mb-4 rounded-md border border-gray-200 bg-white p-4">
      <h1 class="text-xl font-bold">{pr.title}</h1>
      <p class="mt-1 text-sm text-gray-500">
        <span class="font-mono">{pr.repo}</span>
        · #{pr.number}
        · {pr.author}
        · <span class="capitalize">{pr.state}</span>
      </p>
      <p class="mt-2 text-xs">
        <a href={pr.url} class="text-indigo-600 hover:underline" target="_blank" rel="noreferrer">
          View on GitHub →
        </a>
      </p>
    </header>

    <div class="mb-4 flex items-center gap-2">
      <button
        type="button"
        class="rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
        onclick={onReview}
        disabled={busy || reviewing}
      >
        {reviewing ? 'Reviewing…' : 'Review now'}
      </button>
      <button
        type="button"
        class="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
        onclick={onDismissToggle}
        disabled={busy}
      >
        {pr.dismissed ? 'Undismiss' : 'Dismiss'}
      </button>
      {#if err}
        <span class="text-xs text-red-600">{err}</span>
      {/if}
    </div>

    <section>
      <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-gray-500">
        Review history ({reviews.length})
      </h2>
      {#if reviews.length === 0}
        <p class="text-sm text-gray-500">No reviews yet.</p>
      {:else}
        {#each reviews as r (r.id)}
          <PRReviewCard review={r} />
        {/each}
      {/if}
    </section>
  </article>
{/if}
