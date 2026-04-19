<script lang="ts">
  import { severityClass } from '$lib/severity.js';

  let { severity }: { severity: string | null | undefined } = $props();

  const label = $derived((severity ?? 'none').toUpperCase());
  const cls = $derived(severity ? severityClass(severity) : 'bg-gray-100 text-gray-500');
</script>

<span class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium {cls}">
  {label}
</span>
