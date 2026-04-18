<script lang="ts">
  import { browser } from '$app/environment';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import IssueReviewCard from '$lib/components/IssueReviewCard.svelte';
  import { dismissIssue, fetchIssue, triggerIssueReview, undismissIssue } from '$lib/api.js';
  import { issueListRefresh, reviewingIssues } from '$lib/stores.js';
  import type { Issue, IssueReview } from '$lib/types.js';

  const id = $derived(Number($page.params.id));

  let issue: Issue | null = $state(null);
  let reviews: IssueReview[] = $state([]);
  let err: string | null = $state(null);
  let busy = $state(false);

  $effect(() => {
    void $issueListRefresh;
    if (!browser || !Number.isFinite(id)) return;
    // Clear stale state so the loading branch renders on id change.
    issue = null;
    reviews = [];
    err = null;
    fetchIssue(id)
      .then((d) => {
        issue = d.issue;
        reviews = d.reviews;
        err = null;
      })
      .catch((e: unknown) => (err = e instanceof Error ? e.message : String(e)));
  });

  const reviewing = $derived($reviewingIssues.has(id));

  const autoImplementPR: number | undefined = $derived(
    reviews
      .filter((r) => r.action_taken === 'auto_implement' && r.pr_created > 0)
      .slice()
      .sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())[0]
      ?.pr_created
  );

  async function onReview(): Promise<void> {
    if (!issue) return;
    busy = true;
    try {
      reviewingIssues.update((s) => new Set(s).add(id));
      await triggerIssueReview(id);
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
      reviewingIssues.update((s) => {
        const n = new Set(s);
        n.delete(id);
        return n;
      });
    } finally {
      busy = false;
    }
  }

  async function onDismissToggle(): Promise<void> {
    if (!issue) return;
    busy = true;
    try {
      if (issue.dismissed) {
        await undismissIssue(id);
        issueListRefresh.update((n) => n + 1);
      } else {
        await dismissIssue(id);
        // Dismissed issues disappear from /issues; navigate back.
        // No early return — let `finally` reset `busy` even if goto rejects.
        void goto('/issues');
      }
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if err && !issue}
  <p class="text-sm text-red-600">Could not load issue: {err}</p>
{:else if !issue}
  <p class="text-sm text-gray-500">Loading…</p>
{:else}
  <article>
    <header class="mb-4 rounded-md border border-gray-200 bg-white p-4">
      <h1 class="text-xl font-bold">{issue.title}</h1>
      <p class="mt-1 text-sm text-gray-500">
        <span class="font-mono">{issue.repo}</span>
        · #{issue.number}
        · {issue.author}
        · <span class="capitalize">{issue.state}</span>
      </p>
      {#if issue.labels.length > 0}
        <ul class="mt-2 flex flex-wrap gap-1">
          {#each issue.labels as l (l)}
            <li class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium text-gray-700">
              {l}
            </li>
          {/each}
        </ul>
      {/if}
      {#if issue.assignees.length > 0}
        <p class="mt-2 text-xs text-gray-500">
          Assignees: {issue.assignees.join(', ')}
        </p>
      {/if}
      <p class="mt-2 text-xs">
        <a
          href="https://github.com/{issue.repo}/issues/{issue.number}"
          class="text-indigo-600 hover:underline"
          target="_blank"
          rel="noreferrer"
        >
          View on GitHub →
        </a>
      </p>
      {#if autoImplementPR}
        <p class="mt-2 text-xs">
          <a href="/prs/{autoImplementPR}" class="text-indigo-600 hover:underline">
            View created PR →
          </a>
        </p>
      {/if}
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
        {issue.dismissed ? 'Undismiss' : 'Dismiss'}
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
          <IssueReviewCard review={r} />
        {/each}
      {/if}
    </section>
  </article>
{/if}
