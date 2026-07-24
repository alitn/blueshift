<script lang="ts">
  // Confirm for re-entering a finished (READY) episode into the pipeline
  // (vendored dialog primitive, tokens only). Unlike Remove this is NON-
  // destructive: it only runs the steps whose output is missing — nothing
  // already produced is redone — so the confirm is the accent primary, not the
  // danger style. The row's REPROCESS action merely asks; the actual re-drive
  // lives here. On a confirmed success it reports the id so the Library resumes
  // polling (the row goes back to 'uploaded' and advances). Titles are
  // Persian-first content: rendered dir="rtl" in a <bdi> with the fa font stack,
  // ZWNJ untouched.
  import { Dialog, DialogContent, DialogDescription, DialogOverlay, DialogTitle } from '$lib/components/ui/dialog';
  import { reprocessEpisode, type Episode } from '$lib/episodes';

  let {
    open = $bindable(false),
    episode,
    onReprocessed,
    reprocessor = reprocessEpisode
  }: {
    open?: boolean;
    episode: Episode | null;
    onReprocessed: (id: string) => void;
    /** Injection seam for tests; defaults to the real API call. */
    reprocessor?: (id: string) => Promise<boolean>;
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
      ok = await reprocessor(episode.id);
    } catch {
      ok = false;
    }
    if (ok) {
      onReprocessed(episode.id);
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
    <DialogTitle>Reprocess episode</DialogTitle>
    <DialogDescription>
      This re-enters the episode into the pipeline. Only the steps that haven't run yet will run —
      nothing already finished is redone.
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
        Reprocess failed. Check your connection and try again.
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
        data-testid="reprocess-confirm"
        class="rounded-3 bg-accent px-4 py-2 text-[10.5px] font-semibold tracking-[0.1em] text-text-on-accent outline-none transition-colors duration-hover ease-out hover:bg-accent-bright focus-visible:bg-accent-bright disabled:cursor-not-allowed disabled:opacity-[0.35]"
      >
        {busy ? 'REPROCESSING…' : 'REPROCESS'}
      </button>
    </div>
  </DialogContent>
</Dialog>
