<script lang="ts">
  // Five-bar pipeline indicator per DESIGN.md: five 22×4px radius-2 bars plus a
  // mono 8.5px stage label below, colored to state. The status->bars mapping is
  // the M0 ruling in $lib/pipeline.
  import { pipelineView, STEP_BG, TONE_TEXT } from '$lib/pipeline';
  import type { EpisodeStatus } from '$lib/episodes';

  let { status }: { status: EpisodeStatus } = $props();
  const view = $derived(pipelineView(status));
</script>

<div>
  <div class="flex gap-[3px]" aria-hidden="true">
    {#each view.steps as step, i (i)}
      <div class="h-1 w-[22px] rounded-1 {STEP_BG[step]}"></div>
    {/each}
  </div>
  <div class="mt-1 font-mono text-[8.5px] tracking-[0.06em] {TONE_TEXT[view.tone]}">
    {view.label}
  </div>
</div>
