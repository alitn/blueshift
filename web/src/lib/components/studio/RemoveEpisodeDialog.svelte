<script lang="ts">
  // Danger confirm for removing an episode from the Library (vendored dialog
  // primitive, tokens only). The destructive step lives HERE, not on the row:
  // the row's × merely asks. On a confirmed 204 it reports the id so the
  // Library drops the row optimistically (no refetch); on failure it stays
  // open with a neutral error. Titles are Persian-first content: rendered
  // dir="rtl" in a <bdi> with the fa font stack, ZWNJ untouched.
  import { Dialog, DialogContent, DialogDescription, DialogOverlay, DialogTitle } from '$lib/components/ui/dialog';
  import { deleteEpisode, type Episode } from '$lib/episodes';

  let {
    open = $bindable(false),
    episode,
    onRemoved,
    remover = deleteEpisode
  }: {
    open?: boolean;
    episode: Episode | null;
    onRemoved: (id: string) => void;
    /** Injection seam for tests; defaults to the real API call. */
    remover?: (id: string) => Promise<boolean>;
  } = $props();

  let busy = $state(false);
  let error = $state(false);

  // Clear transient state whenever the dialog is dismissed.
  $effect(() => {
    if (!open) {
      busy = false;
      error = false;
    }
  });

  async function confirm() {
    if (busy || !episode) return;
    busy = true;
    error = false;
    let ok = false;
    try {
      ok = await remover(episode.id);
    } catch {
      ok = false;
    }
    if (ok) {
      onRemoved(episode.id);
      open = false;
      return;
    }
    error = true;
    busy = false;
  }
</script>

<Dialog bind:open>
  <DialogOverlay />
  <DialogContent class="w-[440px]">
    <DialogTitle>Remove episode</DialogTitle>
    <DialogDescription>
      This removes the episode from the library for your whole team. This cannot be undone here.
    </DialogDescription>

    {#if episode}
      <div class="mt-3 rounded-3 border border-border-subtle bg-bg-2 px-2.5 py-2">
        <div dir="rtl" class="truncate text-left font-fa text-[12.5px] text-text-primary">
          <bdi>{episode.title}</bdi>
        </div>
        <div class="mt-[2px] font-mono text-[10.5px] text-text-faint">{episode.sourceFilename}</div>
      </div>
    {/if}

    {#if error}
      <p role="alert" class="mt-3 text-[11px] leading-[1.5] text-danger">
        Remove failed. Check your connection and try again.
      </p>
    {/if}

    <div class="mt-4 flex items-center justify-end gap-2">
      <button
        type="button"
        onclick={() => (open = false)}
        disabled={busy}
        class="rounded-3 px-3.5 py-2 text-[10.5px] font-medium tracking-[0.08em] text-text-muted outline-none transition-colors duration-hover ease-out hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-[0.35]"
      >
        Cancel
      </button>
      <button
        type="button"
        onclick={confirm}
        disabled={busy || !episode}
        data-testid="remove-confirm"
        class="rounded-3 border border-danger-border px-4 py-2 text-[10.5px] font-semibold tracking-[0.1em] text-danger outline-none transition-colors duration-hover ease-out hover:border-danger-border-hover focus-visible:border-danger-border-hover disabled:cursor-not-allowed disabled:opacity-[0.35]"
      >
        {busy ? 'REMOVING…' : 'REMOVE'}
      </button>
    </div>
  </DialogContent>
</Dialog>
