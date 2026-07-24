<script lang="ts">
  // Episode view (prototype screen 01, scoped to the M1 transcript slice): the
  // proxy player beside the transcript. The Moments rail and the clip editor are
  // later slices and are deliberately absent. Reached from the Library by opening
  // a Ready episode; the route param is the episode public id (URL/API material —
  // never shown in the UI). Rendered inside the shared AppShell (top bar + status
  // bar); the top bar shows the LIBRARY ▸ EPISODE breadcrumb via the layout.
  import { page } from '$app/stores';
  import ProxyPlayer from '$lib/components/studio/ProxyPlayer.svelte';
  import TranscriptPane from '$lib/components/studio/TranscriptPane.svelte';

  const id = $derived($page.params.id);
</script>

<svelte:head>
  <title>Episode · Blueshift Studio</title>
</svelte:head>

<div class="flex h-full min-h-0">
  {#if id}
    <!-- Player column (screen 01: 472px, on the app canvas). Transport, waveform
         and source metadata are later slices — this slice reuses playback only. -->
    <div class="flex w-[472px] max-w-[45%] flex-none flex-col border-r border-border-subtle">
      <ProxyPlayer episodeId={id} />
    </div>

    <!-- Transcript panel -->
    <div class="flex min-w-0 flex-1 flex-col">
      <TranscriptPane episodeId={id} />
    </div>
  {/if}
</div>
