<script lang="ts">
  import { browser } from '$app/environment';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import IssueTile from '$lib/components/IssueTile.svelte';
  import { fetchIssues } from '$lib/api.js';
  import { desc } from '$lib/sort.js';
  import { issueListRefresh, reviewingIssues } from '$lib/stores.js';
  import type { Issue } from '$lib/types.js';

  let issues: Issue[] = $state([]);
  let err: string | null = $state(null);
  let loading = $state(true);

  $effect(() => {
    void $issueListRefresh;
    if (!browser) return;
    loading = true;
    fetchIssues()
      .then((r) => {
        issues = r;
        err = null;
      })
      .catch((e: unknown) => (err = e instanceof Error ? e.message : String(e)))
      .finally(() => (loading = false));
  });

  const repo = $derived($page.url.searchParams.get('repo') ?? '');
  const severity = $derived($page.url.searchParams.get('severity') ?? 'any');
  const mode = $derived($page.url.searchParams.get('mode') ?? 'all');

  const repos: string[] = $derived(Array.from(new Set(issues.map((i: Issue) => i.repo))).sort());

  const filtered = $derived.by<Issue[]>(() => {
    const list = issues.filter((i) => {
      if (i.dismissed) return false;
      if (repo && i.repo !== repo) return false;
      if (severity !== 'any') {
        const triage = i.latest_review?.triage as { severity?: string } | undefined;
        const s = triage?.severity?.toLowerCase() ?? '';
        if (s !== severity) return false;
      }
      if (mode !== 'all') {
        if (!i.labels.includes(mode)) return false;
      }
      return true;
    });
    return list.sort(desc<Issue>('fetched_at'));
  });

  function applyFilters(next: { repo: string; severity: string; mode?: string }): void {
    const params = new URLSearchParams();
    if (next.repo) params.set('repo', next.repo);
    if (next.severity && next.severity !== 'any') params.set('severity', next.severity);
    if (next.mode && next.mode !== 'all') params.set('mode', next.mode);
    const qs = params.toString();
    goto(qs ? `/issues?${qs}` : '/issues', { keepFocus: true, replaceState: true, noScroll: true });
  }
</script>

<section class="flex items-center gap-3">
  <h1 class="text-2xl font-bold">Issues</h1>
  {#if $reviewingIssues.size > 0}
    <span class="text-xs text-indigo-600">{$reviewingIssues.size} reviewing…</span>
  {/if}
</section>

<FilterBar filters={{ repo, severity, mode }} {repos} variant="issue" onChange={applyFilters} />

{#if err}
  <p class="text-sm text-red-600">Could not load issues: {err}</p>
{:else if loading && issues.length === 0}
  <p class="text-sm text-gray-500">Loading…</p>
{:else if filtered.length === 0}
  <p class="text-sm text-gray-500">No issues match the current filters.</p>
{:else}
  <div class="overflow-hidden rounded-md border border-gray-200 bg-white">
    {#each filtered as issue (issue.id)}
      <IssueTile {issue} />
    {/each}
  </div>
{/if}
