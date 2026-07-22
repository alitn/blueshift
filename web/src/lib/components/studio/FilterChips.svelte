<script lang="ts">
  // Client-side status filter chips per DESIGN.md: Archivo-600 10.5px uppercase,
  // radius-3, 1px border; the active chip gets accent-wash-18 fill + accent-border.
  import type { EpisodeFilter } from './filter';

  let {
    active,
    counts,
    onSelect
  }: {
    active: EpisodeFilter;
    counts: Record<EpisodeFilter, number>;
    onSelect: (f: EpisodeFilter) => void;
  } = $props();

  const chips: { key: EpisodeFilter; label: string }[] = [
    { key: 'all', label: 'ALL' },
    { key: 'processing', label: 'PROCESSING' },
    { key: 'ready', label: 'READY' },
    { key: 'failed', label: 'FAILED' }
  ];
</script>

<div class="flex gap-1.5" role="group" aria-label="Filter episodes by status">
  {#each chips as chip (chip.key)}
    {@const isActive = active === chip.key}
    <button
      type="button"
      aria-pressed={isActive}
      onclick={() => onSelect(chip.key)}
      class="rounded-3 border px-2.5 py-1 font-semibold text-[10.5px] tracking-[0.1em] outline-none transition-colors duration-hover ease-out {isActive
        ? 'border-accent-border bg-accent-wash-18 text-text-primary'
        : 'border-border-strong text-text-muted hover:border-border-hover focus-visible:border-accent-border focus-visible:bg-accent-wash-12'}"
    >
      {chip.label}
      {counts[chip.key]}
    </button>
  {/each}
</div>
