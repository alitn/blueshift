<script lang="ts">
  // Proxy playback in the vendored dialog: a native <video> playing the signed
  // proxy URL for a Ready episode. No episode route in M0. The signed URL is
  // fetched when the dialog opens and is opaque to the client.
  import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '$lib/components/ui/dialog';
  import { fetchProxyUrl, type Episode } from '$lib/episodes';

  let {
    open = $bindable(false),
    episode
  }: {
    open?: boolean;
    episode: Episode | null;
  } = $props();

  let url = $state<string | null>(null);
  let error = $state(false);
  let loading = $state(false);

  // Fetch a fresh signed URL each time the dialog opens for an episode.
  $effect(() => {
    if (open && episode) {
      const id = episode.id;
      loading = true;
      error = false;
      url = null;
      fetchProxyUrl(id)
        .then((u) => {
          url = u;
          error = u === null;
        })
        .catch(() => {
          error = true;
        })
        .finally(() => {
          loading = false;
        });
    } else if (!open) {
      url = null;
      error = false;
      loading = false;
    }
  });
</script>

<Dialog bind:open>
  <DialogOverlay />
  <DialogContent class="w-[720px] max-w-[calc(100vw-2rem)]">
    <DialogTitle class="mb-3">
      {#if episode}
        <span dir="rtl" class="font-fa"><bdi>{episode.title}</bdi></span>
      {:else}
        Proxy
      {/if}
    </DialogTitle>

    <div class="aspect-video w-full overflow-hidden rounded-3 border border-border-default bg-bg-1">
      {#if url}
        <!-- svelte-ignore a11y_media_has_caption -->
        <video src={url} controls autoplay class="h-full w-full" data-testid="proxy-video"></video>
      {:else}
        <div
          class="flex h-full w-full items-center justify-center font-semibold text-[11px] tracking-[0.2em] text-text-faint"
        >
          {#if loading}
            LOADING…
          {:else if error}
            PROXY UNAVAILABLE
          {:else}
            PROXY
          {/if}
        </div>
      {/if}
    </div>
  </DialogContent>
</Dialog>
