<script lang="ts">
  // Upload dialog (vendored dialog primitive): file picker (mp4/mov/mxf, ≤40 GB)
  // + title field + a token-only progress bar during the direct-to-storage PUT.
  // On success it reports the new episode id so the Library refreshes and polls.
  import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '$lib/components/ui/dialog';
  import {
    uploadMaster,
    contentTypeFor,
    MAX_MASTER_BYTES,
    type UploadPhase
  } from '$lib/episodes';

  let {
    open = $bindable(false),
    onUploaded
  }: {
    open?: boolean;
    onUploaded: (id: string) => void;
  } = $props();

  let file = $state<File | null>(null);
  let title = $state('');
  let error = $state('');
  let busy = $state(false);
  let phase = $state<UploadPhase | null>(null);
  let progress = $state(0); // 0..1 during the PUT

  const accept = '.mp4,.mov,.mxf,video/mp4,video/quicktime,application/mxf';

  function reset() {
    file = null;
    title = '';
    error = '';
    busy = false;
    phase = null;
    progress = 0;
  }

  // Clear transient state whenever the dialog is dismissed.
  $effect(() => {
    if (!open) reset();
  });

  function onPick(event: Event) {
    error = '';
    const input = event.target as HTMLInputElement;
    const picked = input.files?.[0] ?? null;
    file = picked;
    if (!picked) return;
    if (!contentTypeFor(picked)) {
      error = 'Unsupported file. Use MP4, MOV or MXF.';
      file = null;
      return;
    }
    if (picked.size > MAX_MASTER_BYTES) {
      error = 'File exceeds the 40 GB limit.';
      file = null;
      return;
    }
    if (title.trim() === '') title = picked.name.replace(/\.[^.]+$/, '');
  }

  const phaseLabel = $derived(
    phase === 'creating'
      ? 'PREPARING'
      : phase === 'uploading'
        ? `UPLOADING ${Math.round(progress * 100)}%`
        : phase === 'finalizing'
          ? 'FINALIZING'
          : ''
  );

  async function submit() {
    if (busy || !file || title.trim() === '') return;
    error = '';
    busy = true;
    try {
      const id = await uploadMaster(file, title.trim(), (p, f) => {
        phase = p;
        progress = f;
      });
      onUploaded(id);
      open = false;
    } catch {
      error = 'Upload failed. Check your connection and try again.';
      busy = false;
      phase = null;
    }
  }
</script>

<Dialog bind:open>
  <DialogOverlay />
  <DialogContent class="w-[440px]">
    <DialogTitle>Upload master</DialogTitle>

    <div class="mt-3.5">
      <span
        id="upload-file-label"
        class="mb-1.5 block font-mono text-[8.5px] uppercase tracking-[0.16em] text-text-faint"
        >File</span
      >
      <label
        class="flex cursor-pointer items-center gap-2 rounded-3 border border-dashed border-border-control px-2.5 py-2.5 text-[11px] text-text-muted transition-colors duration-hover ease-out hover:border-border-hover-control"
      >
        <input
          type="file"
          {accept}
          onchange={onPick}
          disabled={busy}
          aria-labelledby="upload-file-label"
          class="sr-only"
        />
        <span class="truncate">{file ? file.name : 'Choose MP4, MOV or MXF…'}</span>
      </label>
      <p class="mt-1.5 font-mono text-[8px] tracking-[0.06em] text-text-faint">
        MP4 · MOV · MXF — UP TO 40 GB
      </p>
    </div>

    <div class="mt-3">
      <label
        for="upload-title"
        class="mb-1.5 block font-mono text-[8.5px] uppercase tracking-[0.16em] text-text-faint"
        >Title</label
      >
      <input
        id="upload-title"
        type="text"
        bind:value={title}
        disabled={busy}
        dir="auto"
        class="block w-full rounded-3 border border-border-strong bg-bg-2 px-2.5 py-2 text-[12px] text-text-primary outline-none transition-colors duration-hover ease-out placeholder:text-text-faint focus:border-accent-border focus:bg-accent-wash-12"
      />
    </div>

    {#if busy}
      <div class="mt-3.5" aria-live="polite">
        <div class="mb-1.5 flex justify-between font-mono text-[8.5px] tracking-[0.1em] text-text-muted">
          <span>{phaseLabel}</span>
        </div>
        <div class="h-[3px] overflow-hidden rounded-1 bg-bg-5">
          <div
            class="h-full bg-accent transition-[width] duration-hover ease-out"
            style="width: {phase === 'uploading' ? Math.round(progress * 100) : 100}%"
          ></div>
        </div>
      </div>
    {/if}

    {#if error}
      <p role="alert" class="mt-3 text-[11px] leading-[1.5] text-danger">{error}</p>
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
        onclick={submit}
        disabled={busy || !file || title.trim() === ''}
        class="rounded-3 bg-accent px-4 py-2 text-[10.5px] font-semibold tracking-[0.1em] text-text-on-accent outline-none transition-colors duration-hover ease-out hover:bg-accent-bright disabled:cursor-not-allowed disabled:opacity-[0.35]"
      >
        UPLOAD
      </button>
    </div>
  </DialogContent>
</Dialog>
