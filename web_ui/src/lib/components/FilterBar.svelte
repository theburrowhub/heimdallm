<script lang="ts">
  interface Filters {
    repo: string;
    severity: string;
    // One of state (PRs) or mode (issues).
    state?: string;
    mode?: string;
  }

  interface Props {
    filters: Filters;
    repos: string[]; // unique repos available in current dataset
    variant: 'pr' | 'issue';
    onChange: (next: Filters) => void;
  }

  let { filters, repos, variant, onChange }: Props = $props();

  const severities = ['any', 'critical', 'high', 'medium', 'low'];
  const prStates = ['open', 'closed', 'all'];
  const issueModes = ['all', 'auto_implement', 'review_only'];

  function update(field: keyof Filters, value: string): void {
    onChange({ ...filters, [field]: value });
  }
</script>

<div
  class="mb-4 flex flex-wrap items-center gap-3 rounded-md border border-gray-200 bg-gray-50 p-3"
>
  <label class="flex items-center gap-1 text-xs text-gray-600">
    <span>Repo:</span>
    <select
      class="rounded border border-gray-300 bg-white px-2 py-1 text-xs"
      value={filters.repo}
      onchange={(e) => update('repo', (e.currentTarget as HTMLSelectElement).value)}
    >
      <option value="">any</option>
      {#each repos as repo (repo)}
        <option value={repo}>{repo}</option>
      {/each}
    </select>
  </label>

  <label class="flex items-center gap-1 text-xs text-gray-600">
    <span>Severity:</span>
    <select
      class="rounded border border-gray-300 bg-white px-2 py-1 text-xs"
      value={filters.severity || 'any'}
      onchange={(e) => update('severity', (e.currentTarget as HTMLSelectElement).value)}
    >
      {#each severities as s (s)}
        <option value={s}>{s}</option>
      {/each}
    </select>
  </label>

  {#if variant === 'pr'}
    <label class="flex items-center gap-1 text-xs text-gray-600">
      <span>State:</span>
      <select
        class="rounded border border-gray-300 bg-white px-2 py-1 text-xs"
        value={filters.state ?? 'open'}
        onchange={(e) => update('state', (e.currentTarget as HTMLSelectElement).value)}
      >
        {#each prStates as s (s)}
          <option value={s}>{s}</option>
        {/each}
      </select>
    </label>
  {:else}
    <label class="flex items-center gap-1 text-xs text-gray-600">
      <span>Mode:</span>
      <select
        class="rounded border border-gray-300 bg-white px-2 py-1 text-xs"
        value={filters.mode ?? 'all'}
        onchange={(e) => update('mode', (e.currentTarget as HTMLSelectElement).value)}
      >
        {#each issueModes as m (m)}
          <option value={m}>{m}</option>
        {/each}
      </select>
    </label>
  {/if}
</div>
