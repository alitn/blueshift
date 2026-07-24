<script lang="ts">
  // Proxy playback well (prototype screen 01, left column): a native <video>
  // playing the signed proxy URL for a Ready episode. The signed URL is fetched
  // per episode and is opaque to the client — nothing here names the underlying
  // stack. Extracted from the M0 PlayerDialog so the Episode view and any future
  // host share one playback surface. All colour/spacing come from tokens.
  import { fetchProxyUrl } from '$lib/episodes';

  let { episodeId }: { episodeId: string } = $props();

  let url = $state<string | null>(null);
  let error = $state(false);
  let loading = $state(false);

  // Fetch a fresh signed URL per episode; a stale response is ignored.
  $effect(() => {
    const id = episodeId;
    if (!id) {
      url = null;
      error = false;
      loading = false;
      return;
    }
    let cancelled = false;
    loading = true;
    error = false;
    url = null;
    fetchProxyUrl(id)
      .then((u) => {
        if (cancelled) return;
        url = u;
        error = u === null;
      })
      .catch(() => {
        if (!cancelled) error = true;
      })
      .finally(() => {
        if (!cancelled) loading = false;
      });
    return () => {
      cancelled = true;
    };
  });
</script>

<div class="aspect-video w-full flex-none overflow-hidden border-b border-border-subtle bg-bg-1">
  {#if url}
    <!-- svelte-ignore a11y_media_has_caption -->
    <video src={url} controls class="h-full w-full" data-testid="proxy-video"></video>
  {:else}
    <div
      class="flex h-full w-full items-center justify-center font-semibold text-[11px] tracking-[0.2em] text-text-faint"
      data-testid="proxy-placeholder"
    >
      {#if loading}
        LOADING…
      {:else if error}
        PROXY UNAVAILABLE
      {:else}
        PROXY PLAYBACK
      {/if}
    </div>
  {/if}
</div>
