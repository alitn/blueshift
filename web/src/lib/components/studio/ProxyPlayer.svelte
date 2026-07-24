<script lang="ts">
  // Proxy playback well (prototype screen 01, left column): a native <video>
  // playing the signed proxy URL for a Ready episode. The signed URL is fetched
  // per episode and is opaque to the client — nothing here names the underlying
  // stack. Extracted from the M0 PlayerDialog so the Episode view and any future
  // host share one playback surface. All colour/spacing come from tokens.
  //
  // Transcript sync (m1-transcript-sync): the player reports the playhead via
  // `ontime` (native timeupdate, ~4Hz per the media spec's 15–250ms cadence,
  // plus seeking/seeked so scrubbing tracks instantly) and exposes `seekTo`, a
  // play-state-preserving seek: it only assigns currentTime, which the media
  // element contract guarantees never starts or stops playback — playing stays
  // playing, paused stays paused.
  // `loadUrl` is injected (default = the real client) so states are testable.
  import { fetchProxyUrl } from '$lib/episodes';

  let {
    episodeId,
    ontime,
    loadUrl = fetchProxyUrl
  }: {
    episodeId: string;
    /** Playhead position in ms, on every timeupdate (~4Hz) and every seek. */
    ontime?: (ms: number) => void;
    loadUrl?: (id: string) => Promise<string | null>;
  } = $props();

  let url = $state<string | null>(null);
  let error = $state(false);
  let loading = $state(false);
  let videoEl = $state<HTMLVideoElement | null>(null);

  function emitTime(): void {
    if (videoEl) ontime?.(videoEl.currentTime * 1000);
  }

  /**
   * seekTo moves the playhead to `ms`, preserving play state exactly: setting
   * HTMLMediaElement.currentTime neither plays a paused video nor pauses a
   * playing one, and nothing else here touches play()/pause(). No-op until the
   * proxy <video> exists (e.g. proxy unavailable).
   */
  export function seekTo(ms: number): void {
    if (!videoEl) return;
    videoEl.currentTime = Math.max(0, ms) / 1000;
  }

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
    loadUrl(id)
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
    <video
      bind:this={videoEl}
      src={url}
      controls
      class="h-full w-full"
      data-testid="proxy-video"
      ontimeupdate={emitTime}
      onseeking={emitTime}
      onseeked={emitTime}
    ></video>
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
