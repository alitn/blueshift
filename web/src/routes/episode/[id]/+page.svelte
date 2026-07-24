<script lang="ts">
  // Episode view (prototype screen 01): the proxy player, the transcript, and
  // the Moments rail — the three-column review surface. The clip editor is a
  // later slice and is deliberately absent. Reached from the Library by opening
  // a Ready episode; the route param is the episode public id (URL/API material —
  // never shown in the UI). Rendered inside the shared AppShell (top bar + status
  // bar); the top bar shows the LIBRARY ▸ EPISODE breadcrumb via the layout.
  //
  // Two-way player ↔ transcript sync (m1-transcript-sync) is wired here:
  // - Video → transcript: the player reports the playhead (~4Hz + seeks); the
  //   pure mapper in $lib/transcriptSync resolves the current segment (gap
  //   policy: keep the previous one through silences; none before the first —
  //   at t=0 a segment starting at 0 IS current, so the at-rest view highlights
  //   segment 0), and the pane highlights/follows it.
  // - Transcript → video: activating a segment seeks the player to the segment
  //   start via a play-state-preserving seek (playing keeps playing, paused
  //   stays paused) and highlights it immediately — no wait for the next tick.
  // - Moments → video (m1-moments-rail): activating a card seeks to the
  //   moment's quote-aligned start the same way; the transcript highlight
  //   follows because activeIdx is derived from the playhead — no extra wiring.
  import { page } from '$app/stores';
  import ProxyPlayer from '$lib/components/studio/ProxyPlayer.svelte';
  import TranscriptPane from '$lib/components/studio/TranscriptPane.svelte';
  import MomentsRail from '$lib/components/studio/MomentsRail.svelte';
  import { segmentIndexAt } from '$lib/transcriptSync';
  import type { Transcript } from '$lib/transcript';

  const id = $derived($page.params.id);

  let player = $state<{ seekTo: (ms: number) => void } | null>(null);
  let currentMs = $state(0);
  let timings = $state<{ startMs: number }[]>([]);
  const activeIdx = $derived(segmentIndexAt(timings, currentMs));

  // A new episode's playhead starts at rest; drop the previous episode's state.
  $effect(() => {
    void id;
    currentMs = 0;
    timings = [];
  });

  function handleLoaded(t: Transcript): void {
    timings = t.segments;
  }

  function handleTime(ms: number): void {
    currentMs = ms;
  }

  /** Seek to an absolute offset: highlight instantly (even with no video
   *  present), then move the playhead play-state-preservingly. */
  function handleSeek(ms: number): void {
    currentMs = ms;
    player?.seekTo(ms);
  }

  function handleSelect(idx: number): void {
    const seg = timings[idx];
    if (!seg) return;
    handleSeek(seg.startMs);
  }
</script>

<svelte:head>
  <title>Episode · Blueshift Studio</title>
</svelte:head>

<div class="flex h-full min-h-0">
  {#if id}
    <!-- Player column (screen 01: 472px, on the app canvas). Transport, waveform
         and source metadata are later slices — this slice reuses playback only. -->
    <div class="flex w-[472px] max-w-[45%] flex-none flex-col border-r border-border-subtle">
      <ProxyPlayer bind:this={player} episodeId={id} ontime={handleTime} />
    </div>

    <!-- Transcript panel -->
    <div class="flex min-w-0 flex-1 flex-col">
      <TranscriptPane
        episodeId={id}
        {activeIdx}
        onSelect={handleSelect}
        onLoaded={handleLoaded}
      />
    </div>

    <!-- Moments rail (screen 01: 356px bg-3 side panel). The max-width mirrors
         the player column's treatment so the three-column layout stays sensible
         at 1280 (rail ~320px) while matching the prototype at 1440. -->
    <div class="flex w-[356px] max-w-[25%] flex-none flex-col">
      <MomentsRail episodeId={id} onSeek={handleSeek} />
    </div>
  {/if}
</div>
