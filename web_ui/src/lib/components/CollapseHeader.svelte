<script lang="ts">
  interface Props {
    title: string;
    count: number;
    expanded: boolean;
    onToggle: () => void;
  }

  let { title, count, expanded, onToggle }: Props = $props();
</script>

<button
  type="button"
  onclick={onToggle}
  class="flex w-full items-center gap-2 border-b border-gray-200 bg-gray-50 px-4 py-2 text-left hover:bg-gray-100"
  aria-expanded={expanded}
>
  <svg
    class="h-4 w-4 shrink-0 text-gray-500 transition-transform {expanded ? 'rotate-90' : ''}"
    viewBox="0 0 20 20"
    fill="currentColor"
    aria-hidden="true"
  >
    <path
      fill-rule="evenodd"
      d="M7.293 14.707a1 1 0 010-1.414L10.586 10 7.293 6.707a1 1 0 011.414-1.414l4 4a1 1 0 010 1.414l-4 4a1 1 0 01-1.414 0z"
      clip-rule="evenodd"
    />
  </svg>
  <span class="text-sm font-semibold text-gray-700">{title}</span>
  <span class="rounded-full bg-gray-200 px-2 py-0.5 text-xs text-gray-600">{count}</span>
</button>
