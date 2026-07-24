<script lang="ts">
  // Transcript pane (prototype screen 01, middle column). Given an episode public
  // id it loads the neutral transcript and renders speaker turns: an LTR metadata
  // row (mm:ss timecode left, raw diarization chip right) above the RTL Persian
  // body. The body text is copied verbatim from the transcript API — ZWNJ (U+200C)
  // and every other byte survive untouched (the verbatim invariant); nothing here
  // normalizes. Neutral throughout: the header summary is language + word count,
  // and no state names the underlying stack. All colour/type/spacing come from
  // tokens. `load` is injected (default = the real client) so states are testable.
  import { fetchTranscript, type Transcript } from '$lib/transcript';

  let {
    episodeId,
    load = fetchTranscript
  }: {
    episodeId: string;
    load?: (id: string) => Promise<Transcript>;
  } = $props();

  type Status = 'loading' | 'loaded' | 'error';
  let status = $state<Status>('loading');
  let transcript = $state<Transcript | null>(null);

  // Load on mount and whenever the episode changes; a stale request is ignored so
  // a fast re-navigation never lands an earlier episode's transcript.
  $effect(() => {
    const id = episodeId;
    let cancelled = false;
    status = 'loading';
    transcript = null;
    load(id)
      .then((t) => {
        if (!cancelled) {
          transcript = t;
          status = 'loaded';
        }
      })
      .catch(() => {
        if (!cancelled) status = 'error';
      });
    return () => {
      cancelled = true;
    };
  });

  const segments = $derived(transcript?.segments ?? []);
  // A populated transcript scrolls inside the body; make that region keyboard-
  // focusable so it is arrow-scrollable without a pointer (a11y 2.1.1).
  const scrollable = $derived(status === 'loaded' && segments.length > 0);
  // N = total ASR words across segments (word timings are the verbatim source of
  // truth for the count), rendered with a thousands separator like the prototype.
  const wordCount = $derived(segments.reduce((n, s) => n + s.words.length, 0));
  const summary = $derived(
    transcript
      ? `${transcript.language.toUpperCase()} · ${wordCount.toLocaleString('en-US')} WORDS`
      : ''
  );

  /** formatTimecode renders a start offset in ms as mm:ss (minutes are not capped
   *  at 60 — a 74-minute mark reads "74:12"). Tabular via the mono class. */
  function formatTimecode(ms: number): string {
    const totalSeconds = Number.isFinite(ms) && ms > 0 ? Math.floor(ms / 1000) : 0;
    const m = Math.floor(totalSeconds / 60);
    const s = totalSeconds % 60;
    const pad = (n: number) => n.toString().padStart(2, '0');
    return `${pad(m)}:${pad(s)}`;
  }
</script>

<section
  class="flex h-full min-h-0 flex-col bg-bg-2"
  aria-label="Transcript"
  data-testid="transcript-pane"
>
  <!-- Panel header (screen 01: 40px, "TRANSCRIPT" label + neutral summary) -->
  <div
    class="flex h-[40px] flex-none items-center justify-between border-b border-border-subtle px-4.5"
  >
    <span class="font-semibold text-[11px] tracking-[0.16em] text-text-muted">TRANSCRIPT</span>
    {#if status === 'loaded'}
      <span
        class="font-semibold text-[10.5px] tracking-[0.08em] text-text-faint"
        data-testid="transcript-summary"
      >
        {summary}
      </span>
    {/if}
  </div>

  <!-- Body: segments, or a neutral loading / awaiting / error state. When
       populated it is the scroll region, so it takes keyboard focus. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    class="flex min-h-0 flex-1 flex-col overflow-auto focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-accent-border"
    tabindex={scrollable ? 0 : undefined}>
    {#if status === 'loading'}
      <div
        class="flex flex-1 flex-col items-center justify-center px-6 text-center"
        data-testid="transcript-loading"
      >
        <span class="font-semibold text-[11px] tracking-[0.16em] text-text-faint">
          LOADING TRANSCRIPT…
        </span>
      </div>
    {:else if status === 'error'}
      <div
        class="flex flex-1 flex-col items-center justify-center gap-1.5 px-6 text-center"
        data-testid="transcript-error"
      >
        <span class="font-semibold text-[11px] tracking-[0.16em] text-danger">
          TRANSCRIPT UNAVAILABLE
        </span>
        <span class="text-[11.5px] leading-relaxed text-text-muted">
          The transcript couldn’t be loaded. Try again shortly.
        </span>
      </div>
    {:else if segments.length === 0}
      <div
        class="flex flex-1 flex-col items-center justify-center gap-1.5 px-6 text-center"
        data-testid="transcript-empty"
      >
        <span class="font-semibold text-[11px] tracking-[0.16em] text-text-faint">
          AWAITING TRANSCRIPT
        </span>
        <span class="text-[11.5px] leading-relaxed text-text-muted">
          No segments yet — transcription hasn’t produced this episode’s transcript.
        </span>
      </div>
    {:else}
      <div class="flex flex-col gap-4 px-5 pb-5 pt-3.5">
        {#each segments as segment (segment.idx)}
          <div dir="rtl" class="text-right" data-testid="transcript-segment">
            <!-- Metadata row stays LTR: timecode left, speaker chip right. -->
            <div dir="ltr" class="mb-[5px] flex items-center justify-between">
              <span class="font-mono text-[11px] text-text-faint" data-testid="segment-timecode">
                {formatTimecode(segment.startMs)}
              </span>
              {#if segment.speakerKey}
                <span
                  class="rounded-3 border border-border-strong px-2 py-[3px] font-mono text-[11px] leading-none text-text-muted"
                  data-testid="speaker-chip"
                >
                  {segment.speakerKey}
                </span>
              {/if}
            </div>
            <!-- Verbatim Persian body: RTL, font-fa, ZWNJ preserved byte-exact. -->
            <div
              class="font-fa text-[14.5px] leading-[2] text-text-body"
              data-testid="segment-text"
            >
              <bdi>{segment.text}</bdi>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</section>
