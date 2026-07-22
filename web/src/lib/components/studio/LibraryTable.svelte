<script lang="ts">
  // Library table per prototype screen 03: fixed column header + 62px rows.
  // Persian titles render dir="rtl" + <bdi> + font-fa inside the LTR cell, with
  // ZWNJ preserved verbatim (Svelte text nodes are byte-exact). CLIPS/COST show
  // an em dash until M1. Ready rows are keyboard-openable; failed rows expose a
  // danger RETRY. All colors/spacing come from tokens.
  import type { Episode } from '$lib/episodes';
  import { formatDuration, formatSize, formatUploaded } from '$lib/pipeline';
  import PipelineSteps from './PipelineSteps.svelte';

  let {
    episodes,
    onOpen,
    onRetry
  }: {
    episodes: Episode[];
    onOpen: (ep: Episode) => void;
    onRetry: (ep: Episode) => void;
  } = $props();

  function openKey(ep: Episode, event: KeyboardEvent) {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      onOpen(ep);
    }
  }
</script>

<div class="flex h-full min-h-0 flex-col">
  <!-- Column header -->
  <div
    class="flex flex-none items-center border-b border-border-subtle px-6 py-2 font-semibold text-[10.5px] tracking-[0.14em] text-text-faint"
  >
    <div class="min-w-0 flex-1">EPISODE</div>
    <div class="w-[80px] flex-none">UPLOADED</div>
    <div class="w-[90px] flex-none">DURATION</div>
    <div class="w-[250px] flex-none">PIPELINE</div>
    <div class="w-[60px] flex-none text-right">CLIPS</div>
    <div class="w-[80px] flex-none text-right">COST</div>
    <div class="w-[100px] flex-none"></div>
  </div>

  <!-- Rows -->
  <div class="min-h-0 flex-1 overflow-auto">
    {#each episodes as ep (ep.id)}
      {#if ep.status === 'ready'}
        <!-- Ready rows are keyboard-openable (role=button + Enter/Space). -->
        <div
          class="flex h-[62px] cursor-pointer items-center border-b border-border-hairline px-6 outline-none transition-colors duration-hover ease-out hover:bg-hover-row focus-visible:bg-accent-wash-12"
          role="button"
          tabindex="0"
          aria-label={`Open ${ep.title}`}
          onclick={() => onOpen(ep)}
          onkeydown={(e) => openKey(ep, e)}
          data-testid="episode-row"
          data-status={ep.status}
        >
          {@render rowCells(ep)}
        </div>
      {:else}
        <div
          class="flex h-[62px] items-center border-b border-border-hairline px-6 transition-colors duration-hover ease-out hover:bg-hover-row"
          data-testid="episode-row"
          data-status={ep.status}
        >
          {@render rowCells(ep)}
        </div>
      {/if}
    {/each}
  </div>
</div>

{#snippet rowCells(ep: Episode)}
  <div class="min-w-0 flex-1 pr-5">
    <div
      dir="rtl"
      class="truncate text-left font-fa text-[12.5px] text-text-primary"
      data-testid="episode-title"
    >
      <bdi>{ep.title}</bdi>
    </div>
    <div class="mt-[2px] font-mono text-[10.5px] text-text-faint">{ep.sourceFilename}</div>
  </div>
  <div class="w-[80px] flex-none font-mono text-[11px] text-text-muted">
    {formatUploaded(ep.uploadedAt)}
  </div>
  <div
    class="w-[90px] flex-none font-mono text-[11px] tabular-nums text-text-muted"
    title={ep.sizeBytes ? formatSize(ep.sizeBytes) : undefined}
  >
    {formatDuration(ep.durationMs)}
  </div>
  <div class="w-[250px] flex-none">
    <PipelineSteps status={ep.status} />
  </div>
  <div class="w-[60px] flex-none text-right font-mono text-[11px] text-text-primary">—</div>
  <div class="w-[80px] flex-none text-right font-mono text-[11px] tabular-nums text-text-muted">—</div>
  <div class="flex w-[100px] flex-none justify-end">
    {#if ep.status === 'ready'}
      <button
        type="button"
        onclick={(e) => {
          e.stopPropagation();
          onOpen(ep);
        }}
        class="rounded-3 border border-border-control px-3.5 py-1 text-[10.5px] font-semibold tracking-[0.12em] text-text-primary outline-none transition-colors duration-hover ease-out hover:border-border-hover-control focus-visible:border-accent-border"
      >
        OPEN
      </button>
    {:else if ep.status === 'failed'}
      <button
        type="button"
        onclick={() => onRetry(ep)}
        class="rounded-3 border border-danger-border px-3.5 py-1 text-[10.5px] font-semibold tracking-[0.12em] text-danger outline-none transition-colors duration-hover ease-out hover:border-danger-border-hover focus-visible:border-danger-border-hover"
      >
        RETRY
      </button>
    {/if}
  </div>
{/snippet}
