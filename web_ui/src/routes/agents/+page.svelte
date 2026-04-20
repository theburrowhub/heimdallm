<script lang="ts">
  import { browser } from '$app/environment';
  import { deleteAgent, fetchAgents, upsertAgent } from '$lib/api.js';
  import type { Agent } from '$lib/types.js';

  const cliOptions = ['claude', 'gemini', 'codex', 'opencode'];

  let agents: Agent[] = $state([]);
  let loading = $state(true);
  let err: string | null = $state(null);
  let savedFlash: string | null = $state(null);

  // Editor state — `id` empty string when creating a new agent.
  let editor: Agent | null = $state(null);
  let previewOpen = $state(false);
  let saving = $state(false);

  function blankAgent(): Agent {
    return {
      id: '',
      name: '',
      cli: 'claude',
      prompt: '',
      instructions: '',
      cli_flags: '',
      is_default: false,
      created_at: ''
    };
  }

  function load(): void {
    if (!browser) return;
    loading = true;
    err = null;
    fetchAgents()
      .then((list) => {
        agents = list ?? [];
      })
      .catch((e) => (err = e instanceof Error ? e.message : String(e)))
      .finally(() => (loading = false));
  }

  $effect(() => {
    load();
  });

  function startCreate(): void {
    editor = blankAgent();
    previewOpen = false;
  }
  function startEdit(a: Agent): void {
    // Deep-ish copy so the form can be cancelled without mutating the list.
    editor = { ...a };
    previewOpen = false;
  }
  function cancel(): void {
    editor = null;
  }

  async function save(): Promise<void> {
    if (!editor || saving) return;
    if (!editor.id.trim() || !editor.name.trim()) {
      err = 'id and name are required';
      return;
    }
    saving = true;
    err = null;
    savedFlash = null;
    try {
      await upsertAgent(editor);
      savedFlash = `Saved ${editor.id}.`;
      editor = null;
      load();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  async function remove(a: Agent): Promise<void> {
    if (!confirm(`Delete agent "${a.id}"? This cannot be undone.`)) return;
    err = null;
    savedFlash = null;
    try {
      await deleteAgent(a.id);
      savedFlash = `Deleted ${a.id}.`;
      load();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    }
  }

  // A very simple preview: replace the placeholders Heimdallm documents
  // ({diff}, {title}, {author}) with obvious markers. Helps the author
  // see the shape of the prompt without running a real review.
  function renderPreview(template: string): string {
    if (!template.trim()) return '(empty template)';
    return template
      .replace(/\{diff\}/g, '«DIFF»')
      .replace(/\{title\}/g, '«TITLE»')
      .replace(/\{author\}/g, '«AUTHOR»')
      .replace(/\{comments\}/g, '«COMMENTS»');
  }
</script>

<section class="space-y-6">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold">Agents</h1>
      <p class="text-sm text-gray-500">
        Custom review prompts and CLI flags. One entry per reviewer persona.
      </p>
    </div>
    <button
      class="rounded bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-500"
      onclick={startCreate}
      data-testid="new-agent"
    >
      + New agent
    </button>
  </header>

  {#if err}
    <div
      class="rounded border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700"
      data-testid="agents-error"
    >
      {err}
    </div>
  {/if}
  {#if savedFlash}
    <div
      class="rounded border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700"
      data-testid="agents-saved"
    >
      {savedFlash}
    </div>
  {/if}

  {#if loading}
    <p class="text-gray-500">Loading…</p>
  {:else if agents.length === 0 && !editor}
    <p class="text-gray-500">
      No agents yet. The daemon falls back to the built-in template until one is defined.
    </p>
  {:else}
    <ul class="space-y-2" data-testid="agents-list">
      {#each agents as a (a.id)}
        <li
          class="flex items-center justify-between rounded border border-gray-200 bg-white px-4 py-3"
        >
          <div>
            <div class="flex items-center gap-2">
              <span class="font-medium">{a.name}</span>
              <code class="text-xs text-gray-500">({a.id})</code>
              {#if a.is_default}
                <span class="rounded bg-indigo-100 px-2 py-0.5 text-xs text-indigo-800"
                  >default</span
                >
              {/if}
            </div>
            <div class="text-xs text-gray-500">CLI: {a.cli}</div>
          </div>
          <div class="flex gap-2">
            <button
              class="rounded border border-gray-300 px-2 py-1 text-xs hover:bg-gray-50"
              onclick={() => startEdit(a)}
            >
              Edit
            </button>
            <button
              class="rounded border border-red-300 px-2 py-1 text-xs text-red-700 hover:bg-red-50"
              onclick={() => remove(a)}
            >
              Delete
            </button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}

  {#if editor}
    <form
      class="space-y-4 rounded border border-gray-200 bg-white p-4"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      data-testid="agent-editor"
    >
      <header class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">{editor.created_at ? 'Edit agent' : 'New agent'}</h2>
        <button type="button" class="text-sm text-gray-500 hover:text-gray-700" onclick={cancel}>
          Cancel
        </button>
      </header>

      <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
        <label class="flex flex-col gap-1 text-sm">
          ID (stable, used in config)
          <input
            type="text"
            bind:value={editor.id}
            placeholder="security-audit"
            required
            disabled={!!editor.created_at}
            class="rounded border border-gray-300 px-2 py-1 font-mono text-xs disabled:cursor-not-allowed disabled:bg-gray-100 disabled:text-gray-500"
          />
          {#if editor.created_at}
            <span class="text-xs text-gray-500">
              ID is immutable after creation — changing it would orphan the existing agent.
            </span>
          {/if}
        </label>
        <label class="flex flex-col gap-1 text-sm">
          Name (human-readable)
          <input
            type="text"
            bind:value={editor.name}
            placeholder="Security audit"
            required
            class="rounded border border-gray-300 px-2 py-1"
          />
        </label>
        <label class="flex flex-col gap-1 text-sm">
          CLI
          <select bind:value={editor.cli} class="rounded border border-gray-300 px-2 py-1">
            {#each cliOptions as cli (cli)}
              <option value={cli}>{cli}</option>
            {/each}
          </select>
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={editor.is_default} />
          Mark as default
        </label>
      </div>

      <label class="flex flex-col gap-1 text-sm">
        Prompt template (supports <code>{'{diff}'}</code>, <code>{'{title}'}</code>,
        <code>{'{author}'}</code>, <code>{'{comments}'}</code>)
        <textarea
          bind:value={editor.prompt}
          rows="8"
          class="rounded border border-gray-300 px-2 py-1 font-mono text-xs"
        ></textarea>
      </label>

      <label class="flex flex-col gap-1 text-sm">
        Extra instructions (appended to the default template when Prompt is empty)
        <textarea
          bind:value={editor.instructions}
          rows="4"
          class="rounded border border-gray-300 px-2 py-1 text-sm"
        ></textarea>
      </label>

      <label class="flex flex-col gap-1 text-sm">
        CLI flags (free-form, validated server-side)
        <input
          type="text"
          bind:value={editor.cli_flags}
          placeholder="--model claude-opus-4-6"
          class="rounded border border-gray-300 px-2 py-1 font-mono text-xs"
        />
      </label>

      <div class="flex items-center justify-between">
        <button
          type="button"
          class="text-sm text-indigo-600 hover:underline"
          onclick={() => (previewOpen = !previewOpen)}
        >
          {previewOpen ? 'Hide preview' : 'Preview prompt'}
        </button>
        <button
          type="submit"
          disabled={saving}
          class="rounded bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-500 disabled:opacity-50"
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>

      {#if previewOpen}
        <pre
          class="max-h-80 overflow-auto whitespace-pre-wrap rounded border border-gray-200 bg-gray-50 p-3 font-mono text-xs"
          data-testid="agent-preview">{renderPreview(editor.prompt || editor.instructions)}</pre>
      {/if}
    </form>
  {/if}
</section>
