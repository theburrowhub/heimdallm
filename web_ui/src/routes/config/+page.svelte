<script lang="ts">
  import { browser } from '$app/environment';
  import { fetchConfig, reloadConfig, updateConfig } from '$lib/api.js';
  import type { Config } from '$lib/types.js';

  // Known primitive fields + the nested issue_tracking block. The daemon's
  // Config is free-form (Record<string, unknown>), so we load the raw
  // payload, only edit the fields we know about, and write the whole
  // object back on save — unknown fields round-trip unchanged.
  type IssueTracking = {
    enabled?: boolean;
    filter_mode?: string;
    default_action?: string;
    organizations?: string[];
    assignees?: string[];
    develop_labels?: string[];
    review_only_labels?: string[];
    skip_labels?: string[];
  };

  let raw: Config = $state({});
  let pollInterval = $state('5m');
  let aiPrimary = $state('');
  let aiFallback = $state('');
  let reviewMode = $state('single');
  let retentionDays = $state(90);
  let repositoriesText = $state('');
  let it = $state<IssueTracking>({});

  let loading = $state(true);
  let saving = $state(false);
  let err: string | null = $state(null);
  let savedFlash: string | null = $state(null);

  const pollOptions = ['1m', '5m', '30m', '1h'];
  const cliOptions = ['claude', 'gemini', 'codex', 'opencode'];
  const reviewModes = ['single', 'multi'];
  const filterModes = ['exclusive', 'inclusive'];
  const defaultActions = ['ignore', 'review_only'];

  function asString(v: unknown, fallback = ''): string {
    return typeof v === 'string' ? v : fallback;
  }
  function asNumber(v: unknown, fallback = 0): number {
    return typeof v === 'number' ? v : fallback;
  }
  function asStringArray(v: unknown): string[] {
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  }
  function asIssueTracking(v: unknown): IssueTracking {
    if (!v || typeof v !== 'object') return {};
    const o = v as Record<string, unknown>;
    return {
      enabled: typeof o.enabled === 'boolean' ? o.enabled : undefined,
      filter_mode: asString(o.filter_mode) || undefined,
      default_action: asString(o.default_action) || undefined,
      organizations: asStringArray(o.organizations),
      assignees: asStringArray(o.assignees),
      develop_labels: asStringArray(o.develop_labels),
      review_only_labels: asStringArray(o.review_only_labels),
      skip_labels: asStringArray(o.skip_labels)
    };
  }

  function load(): void {
    if (!browser) return;
    loading = true;
    err = null;
    fetchConfig()
      .then((c) => {
        raw = c;
        pollInterval = asString(c.poll_interval, '5m');
        aiPrimary = asString(c.ai_primary);
        aiFallback = asString(c.ai_fallback);
        reviewMode = asString(c.review_mode, 'single');
        retentionDays = asNumber(c.retention_days, 90);
        repositoriesText = asStringArray(c.repositories).join('\n');
        it = asIssueTracking(c.issue_tracking);
      })
      .catch((e) => (err = e instanceof Error ? e.message : String(e)))
      .finally(() => (loading = false));
  }

  $effect(() => {
    load();
  });

  function parseLines(text: string): string[] {
    return text
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean);
  }

  function parseCsv(text: string): string[] {
    return text
      .split(',')
      .map((l) => l.trim())
      .filter(Boolean);
  }

  async function save(): Promise<void> {
    if (saving) return;
    saving = true;
    err = null;
    savedFlash = null;
    try {
      // Compose payload: raw keeps unknown fields, we override what we own.
      const payload: Config = { ...raw };
      payload.poll_interval = pollInterval;
      payload.ai_primary = aiPrimary;
      payload.ai_fallback = aiFallback || undefined;
      payload.review_mode = reviewMode;
      payload.retention_days = retentionDays;
      payload.repositories = parseLines(repositoriesText);
      payload.issue_tracking = {
        ...((raw.issue_tracking as Record<string, unknown> | undefined) ?? {}),
        enabled: Boolean(it.enabled),
        filter_mode: it.filter_mode || 'exclusive',
        default_action: it.default_action || 'ignore',
        organizations: it.organizations ?? [],
        assignees: it.assignees ?? [],
        develop_labels: it.develop_labels ?? [],
        review_only_labels: it.review_only_labels ?? [],
        skip_labels: it.skip_labels ?? []
      };

      await updateConfig(payload);
      // Hot-apply so the user doesn't need to restart the daemon.
      await reloadConfig();
      savedFlash = 'Saved and reloaded.';
      // Refresh view with persisted state (daemon may have normalised fields).
      load();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }
</script>

<section class="space-y-8">
  <header>
    <h1 class="text-2xl font-semibold">Configuration</h1>
    <p class="text-sm text-gray-500">
      Live daemon configuration. Saving applies the change immediately via <code>POST /reload</code
      >.
    </p>
  </header>

  {#if loading}
    <p class="text-gray-500">Loading…</p>
  {:else if err}
    <div
      class="rounded border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700"
      data-testid="config-error"
    >
      {err}
    </div>
  {/if}

  {#if !loading}
    <form
      class="space-y-6"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <fieldset class="space-y-4 rounded border border-gray-200 p-4">
        <legend class="text-sm font-semibold">Polling & AI</legend>

        <label class="flex flex-col gap-1 text-sm">
          Poll interval
          <select bind:value={pollInterval} class="w-40 rounded border border-gray-300 px-2 py-1">
            {#each pollOptions as opt (opt)}
              <option value={opt}>{opt}</option>
            {/each}
          </select>
        </label>

        <div class="flex gap-4">
          <label class="flex flex-1 flex-col gap-1 text-sm">
            AI primary
            <select bind:value={aiPrimary} class="rounded border border-gray-300 px-2 py-1">
              <option value="">—</option>
              {#each cliOptions as cli (cli)}
                <option value={cli}>{cli}</option>
              {/each}
            </select>
          </label>
          <label class="flex flex-1 flex-col gap-1 text-sm">
            AI fallback
            <select bind:value={aiFallback} class="rounded border border-gray-300 px-2 py-1">
              <option value="">— (none)</option>
              {#each cliOptions as cli (cli)}
                <option value={cli}>{cli}</option>
              {/each}
            </select>
          </label>
        </div>

        <label class="flex flex-col gap-1 text-sm">
          Review mode
          <select bind:value={reviewMode} class="w-40 rounded border border-gray-300 px-2 py-1">
            {#each reviewModes as m (m)}
              <option value={m}>{m}</option>
            {/each}
          </select>
        </label>

        <label class="flex flex-col gap-1 text-sm">
          Retention (days)
          <input
            type="number"
            min="0"
            bind:value={retentionDays}
            class="w-40 rounded border border-gray-300 px-2 py-1"
          />
        </label>
      </fieldset>

      <fieldset class="space-y-4 rounded border border-gray-200 p-4">
        <legend class="text-sm font-semibold">Repositories</legend>
        <label class="flex flex-col gap-1 text-sm">
          One <code>org/repo</code> per line. Blank lines are ignored.
          <textarea
            bind:value={repositoriesText}
            rows="5"
            class="rounded border border-gray-300 px-2 py-1 font-mono text-sm"
            placeholder="freepik-company/ai-platform"
          ></textarea>
        </label>
      </fieldset>

      <fieldset class="space-y-4 rounded border border-gray-200 p-4">
        <legend class="text-sm font-semibold">Issue tracking</legend>

        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={it.enabled} />
          Enable issue-tracking pipeline
        </label>

        <div class="flex gap-4">
          <label class="flex flex-1 flex-col gap-1 text-sm">
            Filter mode
            <select bind:value={it.filter_mode} class="rounded border border-gray-300 px-2 py-1">
              {#each filterModes as f (f)}
                <option value={f}>{f}</option>
              {/each}
            </select>
          </label>
          <label class="flex flex-1 flex-col gap-1 text-sm">
            Default action
            <select bind:value={it.default_action} class="rounded border border-gray-300 px-2 py-1">
              {#each defaultActions as a (a)}
                <option value={a}>{a}</option>
              {/each}
            </select>
          </label>
        </div>

        <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
          {@render labelList(
            'Organizations',
            it.organizations ?? [],
            (v) => (it.organizations = v)
          )}
          {@render labelList('Assignees', it.assignees ?? [], (v) => (it.assignees = v))}
          {@render labelList(
            'Develop labels',
            it.develop_labels ?? [],
            (v) => (it.develop_labels = v)
          )}
          {@render labelList(
            'Review-only labels',
            it.review_only_labels ?? [],
            (v) => (it.review_only_labels = v)
          )}
          {@render labelList('Skip labels', it.skip_labels ?? [], (v) => (it.skip_labels = v))}
        </div>
      </fieldset>

      <div class="flex items-center gap-4">
        <button
          type="submit"
          disabled={saving}
          class="rounded bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-500 disabled:opacity-50"
        >
          {saving ? 'Saving…' : 'Save & reload'}
        </button>
        {#if savedFlash}
          <span class="text-sm text-green-700" data-testid="config-saved">{savedFlash}</span>
        {/if}
      </div>
    </form>
  {/if}
</section>

{#snippet labelList(title: string, values: string[], onUpdate: (v: string[]) => void)}
  <label class="flex flex-col gap-1 text-sm">
    {title}
    <input
      type="text"
      value={values.join(', ')}
      oninput={(e) => onUpdate(parseCsv((e.currentTarget as HTMLInputElement).value))}
      placeholder="bug, feature, needs-triage"
      class="rounded border border-gray-300 px-2 py-1 font-mono text-xs"
    />
    <span class="text-xs text-gray-500"
      >Comma-separated. No quotes or spaces needed around values.</span
    >
  </label>
{/snippet}
