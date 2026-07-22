<script lang="ts">
  // Library first-run placeholder (screen 05). Static only in M0 — real Library
  // data lands in m0-library and the upload flow in m0-upload, so the primary
  // CTA is disabled here (opacity 0.35 + not-allowed per DESIGN.md button rules).
</script>

<svelte:head>
  <title>Library · Blueshift Studio</title>
</svelte:head>

<section class="flex h-full items-center justify-center p-6" aria-labelledby="first-run-title">
  <div
    class="w-[620px] max-w-full rounded-4 border border-dashed border-border-control p-[28px] text-center"
  >
    <h1 id="first-run-title" class="text-[14px] font-semibold tracking-[0.01em] text-text-primary">
      Upload your first master
    </h1>
    <p class="mx-auto mb-4 mt-[7px] text-[11.5px] leading-[1.6] text-text-muted">
      Processing takes about 25 minutes — transcription, analysis and fidelity checks run
      unattended.<br />Explore the sample episode meanwhile.
    </p>
    <button
      type="button"
      disabled
      class="inline-block cursor-not-allowed rounded-3 bg-accent px-5 py-2 text-[10.5px] font-semibold tracking-[0.1em] text-text-on-accent opacity-[0.35]"
    >
      UPLOAD MASTER
    </button>
    <p class="mt-3 font-mono text-[8.5px] tracking-[0.06em] text-text-faint">
      MP4 · MOV · MXF — UP TO 40 GB
    </p>
  </div>
</section>
