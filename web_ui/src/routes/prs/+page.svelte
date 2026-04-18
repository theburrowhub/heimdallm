<script lang="ts">
  import { browser } from '$app/environment';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import PRTile from '$lib/components/PRTile.svelte';
  import { fetchPRs } from '$lib/api.js';
  import { byUpdated } from '$lib/sort.js';
  import { prListRefresh, reviewingPRs } from '$lib/stores.js';
  import type { PR } from '$lib/types.js';

  let prs: PR[] = $state([]);
  let err: string | null = $state(null);
  let loading: boolean = $state(true);

  $effect(() => {
    void $prListRefresh; // re-run on SSE-driven bump
    if (!browser) return;
    loading = true;
    fetchPRs()
      .then((r) => {
        prs = r;
        err = null;
      })
      .catch((e) => {
        err = e instanceof Error ? e.message : String(e);
      })
      .finally(() => (loading = false));
  });

  const repo = $derived($page.url.searchParams.get('repo') ?? '');
  const severity = $derived($page.url.searchParams.get('severity') ?? 'any');
  const prState = $derived($page.url.searchParams.get('state') ?? 'open');

  const repos: string[] = $derived(Array.from(new Set(prs.map((p: PR) => p.repo))).sort());

  const filtered = $derived.by(() => {
    const list = prs.filter((p: PR) => {
      if (repo && p.repo !== repo) return false;
      if (prState !== 'all' && p.state?.toLowerCase() !== prState) return false;
      if (severity !== 'any') {
        const s = p.latest_review?.severity?.toLowerCase() ?? '';
        if (s !== severity) return false;
      }
      return true;
    });
    return list.sort(byUpdated);
  });

  function applyFilters(next: { repo: string; severity: string; state?: string }): void {
    const params = new URLSearchParams();
    if (next.repo) params.set('repo', next.repo);
    if (next.severity && next.severity !== 'any') params.set('severity', next.severity);
    if (next.state && next.state !== 'open') params.set('state', next.state);
    const qs = params.toString();
    goto(qs ? `/prs?${qs}` : '/prs', { keepFocus: true, replaceState: true, noScroll: true });
  }
</script>

<section class="flex items-center gap-3">
  <h1 class="text-2xl font-bold">PR Reviews</h1>
  {#if $reviewingPRs.size > 0}
    <span class="text-xs text-indigo-600">{$reviewingPRs.size} reviewing…</span>
  {/if}
</section>

<FilterBar
  filters={{ repo, severity, state: prState }}
  {repos}
  variant="pr"
  onChange={applyFilters}
/>

{#if err}
  <p class="text-sm text-red-600">Could not load PRs: {err}</p>
{:else if loading && prs.length === 0}
  <p class="text-sm text-gray-500">Loading…</p>
{:else if filtered.length === 0}
  <p class="text-sm text-gray-500">No PRs match the current filters.</p>
{:else}
  <div class="overflow-hidden rounded-md border border-gray-200 bg-white">
    {#each filtered as pr (pr.id)}
      <PRTile {pr} />
    {/each}
  </div>
{/if}
