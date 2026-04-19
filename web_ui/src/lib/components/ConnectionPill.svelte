<script lang="ts">
  import { getContext } from 'svelte';
  import type { Readable } from 'svelte/store';

  const connected = getContext<Readable<boolean>>('sse-connected');

  const cls = $derived($connected ? 'bg-green-100 text-green-700' : 'bg-amber-100 text-amber-700');
  const label = $derived($connected ? 'connected' : 'disconnected');
</script>

<span class="rounded-full px-2 py-0.5 text-xs font-medium {cls}" data-testid="sse-pill">
  {label}
</span>
