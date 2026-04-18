<script lang="ts">
  import { browser } from '$app/environment';
  import CollapseHeader from '$lib/components/CollapseHeader.svelte';
  import ConnectionPill from '$lib/components/ConnectionPill.svelte';
  import IssueTile from '$lib/components/IssueTile.svelte';
  import PRTile from '$lib/components/PRTile.svelte';
  import StatsCards from '$lib/components/StatsCards.svelte';
  import { fetchIssues, fetchPRs, fetchStats } from '$lib/api.js';
  import { persistedBoolean, persistedString } from '$lib/persisted.js';
  import { byPriority, byUpdated } from '$lib/sort.js';
  import { auth, issueListRefresh, prListRefresh } from '$lib/stores.js';
  import type { Issue, PR, Stats } from '$lib/types.js';

  let prs: PR[] = $state([]);
  let issues: Issue[] = $state([]);
  let stats: Stats | null = $state(null);
  let err: string | null = $state(null);
  let loading = $state(true);

  $effect(() => {
    void $prListRefresh;
    if (!browser) return;
    loading = true;
    fetchPRs()
      .then((r) => (prs = r))
      .catch((e: unknown) => (err = e instanceof Error ? e.message : String(e)))
      .finally(() => (loading = false));
    fetchStats()
      .then((r) => (stats = r))
      .catch(() => {});
  });

  $effect(() => {
    void $issueListRefresh;
    if (!browser) return;
    fetchIssues()
      .then((r) => (issues = r))
      .catch(() => {});
  });

  const sort = persistedString('dashboard.sort', 'priority');
  const reviewsExpanded = persistedBoolean('dashboard.reviewsExpanded', true);
  const prsExpanded = persistedBoolean('dashboard.prsExpanded', true);
  const issuesExpanded = persistedBoolean('dashboard.issuesExpanded', true);

  const sorter: (a: PR, b: PR) => number = $derived($sort === 'priority' ? byPriority : byUpdated);

  const meLower = $derived(($auth.login ?? '').toLowerCase());
  const hasLogin = $derived(meLower !== '');

  const activePRs: PR[] = $derived(prs.filter((p) => !p.dismissed));

  // When login is unknown (fetchMe failed / daemon has no meFn), fall back
  // to a single "PRs" bucket so the dashboard isn't silently wrong.
  const myReviews: PR[] = $derived(
    hasLogin
      ? activePRs.filter((p) => p.author.toLowerCase() !== meLower).sort(sorter)
      : activePRs.sort(sorter)
  );
  const myPRs: PR[] = $derived(
    hasLogin ? activePRs.filter((p) => p.author.toLowerCase() === meLower).sort(sorter) : []
  );
  const trackedIssues: Issue[] = $derived(issues.filter((i) => !i.dismissed));

  const empty = $derived(
    myReviews.length === 0 && myPRs.length === 0 && trackedIssues.length === 0
  );
</script>

<section class="mb-6 flex items-center gap-3">
  <h1 class="text-3xl font-bold">Dashboard</h1>
  <ConnectionPill />
</section>

<StatsCards {stats} />

<section class="mt-6 mb-3 flex items-center gap-2">
  <span class="text-xs text-gray-500">Sort:</span>
  <button
    type="button"
    aria-pressed={$sort === 'priority'}
    class="rounded px-2 py-1 text-xs font-medium {$sort === 'priority'
      ? 'bg-indigo-100 text-indigo-700'
      : 'text-gray-600 hover:bg-gray-100'}"
    onclick={() => sort.set('priority')}
  >
    Priority
  </button>
  <button
    type="button"
    aria-pressed={$sort === 'newest'}
    class="rounded px-2 py-1 text-xs font-medium {$sort === 'newest'
      ? 'bg-indigo-100 text-indigo-700'
      : 'text-gray-600 hover:bg-gray-100'}"
    onclick={() => sort.set('newest')}
  >
    Newest
  </button>
</section>

{#if err}
  <p class="text-sm text-red-600">Could not load PRs: {err}</p>
{:else if loading && prs.length === 0 && issues.length === 0}
  <p class="mt-6 text-center text-sm text-gray-500">Loading…</p>
{:else if empty}
  <p class="mt-6 text-center text-sm text-gray-500">No activity yet.</p>
{:else}
  <div class="overflow-hidden rounded-md border border-gray-200 bg-white">
    {#if myReviews.length > 0}
      <CollapseHeader
        title={hasLogin ? 'My Reviews' : 'Pull Requests'}
        count={myReviews.length}
        expanded={$reviewsExpanded}
        onToggle={() => reviewsExpanded.update((v) => !v)}
      />
      {#if $reviewsExpanded}
        {#each myReviews as pr (pr.id)}
          <PRTile {pr} />
        {/each}
      {/if}
    {/if}

    {#if myPRs.length > 0}
      <CollapseHeader
        title="My PRs"
        count={myPRs.length}
        expanded={$prsExpanded}
        onToggle={() => prsExpanded.update((v) => !v)}
      />
      {#if $prsExpanded}
        {#each myPRs as pr (pr.id)}
          <PRTile {pr} />
        {/each}
      {/if}
    {/if}

    {#if trackedIssues.length > 0}
      <CollapseHeader
        title="Tracked Issues"
        count={trackedIssues.length}
        expanded={$issuesExpanded}
        onToggle={() => issuesExpanded.update((v) => !v)}
      />
      {#if $issuesExpanded}
        {#each trackedIssues as issue (issue.id)}
          <IssueTile {issue} />
        {/each}
      {/if}
    {/if}
  </div>
{/if}
