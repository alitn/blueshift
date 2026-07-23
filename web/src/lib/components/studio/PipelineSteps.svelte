<script lang="ts">
  // Five-bar pipeline indicator per DESIGN.md: five 22×4px radius-2 bars plus a
  // mono 10.5px stage label below, colored to state. The (state, stage)->bars
  // mapping is the ruling in $lib/pipeline; `stage` is the server's neutral
  // current_stage that says which bar is current.
  import { pipelineView, STEP_BG, TONE_TEXT } from '$lib/pipeline';
  import type { DisplayState } from '$lib/episodes';

  let { state, stage }: { state: DisplayState; stage?: string } = $props();
  const view = $derived(pipelineView(state, stage));
</script>

<div>
  <div class="flex gap-[3px]" aria-hidden="true">
    {#each view.steps as step, i (i)}
      <div
        class="h-1 w-[22px] rounded-1 {STEP_BG[step]}"
        data-testid="pipeline-bar"
        data-step={step}
      ></div>
    {/each}
  </div>
  <div class="mt-1 font-mono text-[10.5px] tracking-[0.06em] {TONE_TEXT[view.tone]}">
    {view.label}
  </div>
</div>
