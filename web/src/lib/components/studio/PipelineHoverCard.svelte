<script lang="ts">
  // Hover/focus popover for the Library's pipeline cell: wraps the cell content
  // (the five bars) in a tooltip trigger and lazily fetches the per-stage
  // provenance on first open (cached per episode until its status/stage
  // changes — the Library poll payload is untouched). Dismissal (unhover,
  // blur, Escape) and focus-open come from the vendored tooltip primitive.
  //
  // Rest-invisible by contract: at rest this renders ONLY the unstyled trigger
  // around the existing bars (block, full-width, left-aligned, zero padding),
  // so the at-rest row is pixel-identical to the committed baselines.
  //
  // Row-interaction contract: a MOUSE click on the cell keeps bubbling to the
  // row (open-on-click behaves exactly as before), while KEYBOARD activation is
  // contained — keydown never bubbles to the row (whose Enter/Space handler
  // would open the episode; same containment as the row's remove button) and a
  // keyboard-synthesized click (detail === 0) is stopped for the same reason.
  // Focusing the cell shows details; it must not open the episode underneath.
  import * as Tooltip from '$lib/components/ui/tooltip';
  import { fetchPipelineDetails, type PipelineDetails } from '$lib/pipelineDetails';
  import type { Episode } from '$lib/episodes';
  import type { Snippet } from 'svelte';
  import PipelineCard from './PipelineCard.svelte';

  let { episode, children }: { episode: Episode; children: Snippet } = $props();

  let details = $state<PipelineDetails | undefined>(undefined);
  let loading = $state(false);
  let error = $state(false);

  async function onOpenChange(open: boolean) {
    if (!open) return;
    loading = details === undefined;
    error = false;
    try {
      // Cached per episode+state in $lib/pipelineDetails; a status/stage
      // change since the last open refetches, otherwise this resolves locally.
      details = await fetchPipelineDetails(episode);
    } catch {
      details = undefined;
      error = true;
    } finally {
      loading = false;
    }
  }
</script>

<Tooltip.Provider delayDuration={150} skipDelayDuration={300}>
  <Tooltip.Root {onOpenChange}>
    <Tooltip.Trigger
      type="button"
      aria-label={`Pipeline details for ${episode.title}`}
      data-testid="pipeline-cell-trigger"
      class="block w-full cursor-pointer p-0 text-left outline-none"
      onclick={(e: MouseEvent) => {
        if (e.detail === 0) e.stopPropagation();
      }}
      onkeydown={(e: KeyboardEvent) => e.stopPropagation()}
    >
      {@render children()}
    </Tooltip.Trigger>
    <Tooltip.Content variant="card" side="bottom" align="start" data-testid="pipeline-popover">
      <PipelineCard {details} {loading} {error} />
    </Tooltip.Content>
  </Tooltip.Root>
</Tooltip.Provider>
