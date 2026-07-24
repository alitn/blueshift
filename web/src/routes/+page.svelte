<script lang="ts">
  // Library (prototype screen 03 / first-run screen 05). Lists org episodes with
  // live pipeline status (polled — see $lib/pollStore), client-side status +
  // search filtering, an upload dialog, and proxy playback for Ready rows. All
  // color/type/spacing come from tokens.
  import { onDestroy, onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { createEpisodesStore } from '$lib/pollStore';
  import { retryEpisode, type Episode } from '$lib/episodes';
  import { applyFilter, counts, type EpisodeFilter } from '$lib/components/studio/filter';
  import LibraryTable from '$lib/components/studio/LibraryTable.svelte';
  import FilterChips from '$lib/components/studio/FilterChips.svelte';
  import EmptyState from '$lib/components/studio/EmptyState.svelte';
  import UploadDialog from '$lib/components/studio/UploadDialog.svelte';
  import RemoveEpisodeDialog from '$lib/components/studio/RemoveEpisodeDialog.svelte';

  const episodes = createEpisodesStore();

  let filter = $state<EpisodeFilter>('all');
  let query = $state('');
  let uploadOpen = $state(false);
  let removeOpen = $state(false);
  let removeTarget = $state<Episode | null>(null);

  const chipCounts = $derived(counts($episodes.episodes));
  const visible = $derived(applyFilter($episodes.episodes, filter, query));
  const isEmpty = $derived($episodes.loaded && $episodes.episodes.length === 0);

  onMount(() => episodes.start());
  onDestroy(() => episodes.stop());

  function openEpisode(ep: Episode) {
    // Open the Episode view. The public id is URL/API material (never shown).
    void goto(`/episode/${encodeURIComponent(ep.id)}`);
  }

  async function onRetry(ep: Episode) {
    const ok = await retryEpisode(ep.id);
    if (ok) {
      // Optimistically reflect the reset and resume polling so the row advances.
      await episodes.refresh();
      episodes.start();
    }
  }

  function onUploaded() {
    // A new 'uploaded' episode exists; refresh and ensure polling is running.
    void episodes.refresh();
    episodes.start();
  }

  function onRemove(ep: Episode) {
    // The row's × only asks; the danger dialog owns the destructive step.
    removeTarget = ep;
    removeOpen = true;
  }

  function onRemoved(id: string) {
    // Confirmed 204: drop the row optimistically. Deleted episodes never come
    // back from the server, so no refetch is needed.
    episodes.remove(id);
  }

  // `U` opens the upload dialog, unless the user is typing or a dialog is open.
  function onWindowKey(event: KeyboardEvent) {
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return;
    if (uploadOpen || removeOpen) return;
    const el = event.target as HTMLElement | null;
    const tag = el?.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || el?.isContentEditable) return;
    if (event.key === 'u' || event.key === 'U') {
      event.preventDefault();
      uploadOpen = true;
    }
  }
</script>

<svelte:head>
  <title>Library · Blueshift Studio</title>
</svelte:head>

<svelte:window onkeydown={onWindowKey} />

<div class="flex h-full min-h-0 flex-col">
  <!-- Toolbar: search · filter chips · keyboard hint · upload -->
  <div class="flex flex-none items-center gap-3.5 border-b border-border-subtle px-6 py-3.5">
    <div
      class="flex w-[300px] items-center gap-2 rounded-3 border border-border-strong px-2.5 py-2 transition-colors duration-hover ease-out focus-within:border-accent-border"
    >
      <svg width="11" height="11" viewBox="0 0 12 12" aria-hidden="true" class="flex-none">
        <circle cx="5" cy="5" r="3.6" fill="none" stroke="currentColor" stroke-width="1.2" class="text-text-faint" />
        <path d="M8 8l3 3" stroke="currentColor" stroke-width="1.2" class="text-text-faint" />
      </svg>
      <input
        type="search"
        bind:value={query}
        placeholder="Search episodes, guests, topics…"
        aria-label="Search episodes"
        class="w-full bg-transparent font-mono text-[11px] text-text-primary outline-none placeholder:text-text-faint"
      />
    </div>

    <FilterChips active={filter} counts={chipCounts} onSelect={(f) => (filter = f)} />

    <div class="flex-1"></div>

    <div
      class="hidden items-center gap-1.5 font-mono text-[10.5px] tracking-[0.1em] text-text-muted md:flex"
      aria-hidden="true"
    >
      <kbd class="rounded-2 border border-border-strong px-1.5 py-[2px]">U</kbd>
      <span class="text-text-faint">UPLOAD</span>
      <kbd class="ml-1.5 rounded-2 border border-border-strong px-1.5 py-[2px]">↵</kbd>
      <span class="text-text-faint">OPEN</span>
    </div>

    <button
      type="button"
      onclick={() => (uploadOpen = true)}
      class="rounded-3 bg-accent px-4.5 py-2 text-[11.5px] font-semibold tracking-[0.1em] text-text-on-accent outline-none transition-colors duration-hover ease-out hover:bg-accent-bright focus-visible:bg-accent-bright"
    >
      UPLOAD MASTER
    </button>
  </div>

  <!-- Body: table, empty state, or (first load) nothing -->
  <div class="min-h-0 flex-1">
    {#if isEmpty}
      <EmptyState onUpload={() => (uploadOpen = true)} />
    {:else if $episodes.loaded}
      <LibraryTable episodes={visible} onOpen={openEpisode} {onRetry} {onRemove} />
    {/if}
  </div>
</div>

<UploadDialog bind:open={uploadOpen} onUploaded={onUploaded} />
<RemoveEpisodeDialog bind:open={removeOpen} episode={removeTarget} onRemoved={onRemoved} />
