<script lang="ts">
  // Transcript pane (prototype screen 01, middle column). Given an episode public
  // id it loads the neutral transcript and renders speaker turns: an LTR metadata
  // row (mm:ss timecode left, raw diarization chip right) above the RTL Persian
  // body. The body text is copied verbatim from the transcript API — ZWNJ (U+200C)
  // and every other byte survive untouched (the verbatim invariant); nothing here
  // normalizes. Neutral throughout: the header summary is language + word count,
  // and no state names the underlying stack. All colour/type/spacing come from
  // tokens. `load` is injected (default = the real client) so states are testable.
  //
  // Player sync (m1-transcript-sync): the host passes `activeIdx` (the segment
  // current at the playhead, -1 = none) and receives `onSelect(idx)` when a
  // segment is clicked or keyboard-activated (Enter/Space — segments are
  // focusable role=button). The active segment carries the design highlight
  // (accent-wash-14 + a 2px accent edge on the reading-start side — inline-start,
  // so it lands on the right for this RTL block) and is auto-scrolled into view
  // only when outside the pane. Auto-follow never fights the user: manual
  // scroll intent (wheel/touch/pointer/scroll keys) suspends it for ~4s; it
  // resumes on the next segment change after that idle, or immediately when a
  // segment is activated (policy in $lib/transcriptSync).
  import { fetchTranscript, type Transcript } from '$lib/transcript';
  import { createFollowGate } from '$lib/transcriptSync';

  let {
    episodeId,
    load = fetchTranscript,
    activeIdx = -1,
    onSelect,
    onLoaded
  }: {
    episodeId: string;
    load?: (id: string) => Promise<Transcript>;
    /** idx of the segment current at the playhead; -1 (or absent) = none. */
    activeIdx?: number;
    /** A segment was clicked or keyboard-activated. */
    onSelect?: (idx: number) => void;
    /** The transcript resolved — hands the host the segment timings to map. */
    onLoaded?: (t: Transcript) => void;
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
          onLoaded?.(t);
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

  // ---- Transcript → video: segment activation --------------------------------

  function select(idx: number): void {
    // An explicit jump resumes auto-follow immediately (policy).
    followGate.noteSelect();
    onSelect?.(idx);
  }

  function selectKey(idx: number, event: KeyboardEvent): void {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault(); // Space must not scroll the pane
      select(idx);
    }
  }

  // ---- Video → transcript: auto-follow with manual-scroll suspension ---------

  const followGate = createFollowGate();
  let scrollBox = $state<HTMLDivElement | null>(null);

  /** Manual scroll intent on the pane: suspend auto-follow (~4s window). */
  function noteUserScroll(): void {
    followGate.noteUserScroll(Date.now());
  }

  /** Scroll keys pressed on the scroll region itself (not on a segment). */
  function scrollKey(event: KeyboardEvent): void {
    if (event.target !== scrollBox) return;
    const keys = ['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '];
    if (keys.includes(event.key)) noteUserScroll();
  }

  /**
   * scrollIntent (action): passive manual-scroll-intent detection for the
   * auto-follow gate — wheel, touch, scrollbar/pointer grabs, and scroll keys
   * on the region itself. These listeners carry no interaction semantics (the
   * interactive elements are the segment buttons inside), so they are attached
   * imperatively rather than as template handlers.
   */
  function scrollIntent(node: HTMLElement) {
    const key = (e: Event) => scrollKey(e as KeyboardEvent);
    node.addEventListener('wheel', noteUserScroll, { passive: true });
    node.addEventListener('touchmove', noteUserScroll, { passive: true });
    node.addEventListener('pointerdown', noteUserScroll, { passive: true });
    node.addEventListener('keydown', key);
    return {
      destroy() {
        node.removeEventListener('wheel', noteUserScroll);
        node.removeEventListener('touchmove', noteUserScroll);
        node.removeEventListener('pointerdown', noteUserScroll);
        node.removeEventListener('keydown', key);
      }
    };
  }

  /** True when the segment sits fully inside the pane's vertical viewport. */
  function fullyInView(el: Element, box: Element): boolean {
    const er = el.getBoundingClientRect();
    const br = box.getBoundingClientRect();
    return er.top >= br.top && er.bottom <= br.bottom;
  }

  // Follow the playhead: when the active segment changes and auto-follow is not
  // suspended, smooth-scroll it into view — only if it is outside the viewport
  // (block:'nearest' keeps the movement minimal; suspension is re-checked on
  // each segment change, which is exactly the "resume after idle" policy).
  $effect(() => {
    const idx = activeIdx;
    const box = scrollBox;
    if (!box || idx < 0 || status !== 'loaded') return;
    if (!followGate.shouldFollow(Date.now())) return;
    const el = box.querySelector(`[data-seg-idx="${idx}"]`);
    if (el && !fullyInView(el, box)) {
      el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }
  });
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
    bind:this={scrollBox}
    use:scrollIntent
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
          <!-- One speaker turn: clickable/keyboard-activatable (role=button,
               Enter/Space) to seek the player. The active (playhead) segment
               gets the accent-wash-14 highlight + a 2px accent inline-start
               edge — the reading-start (right) side of this RTL block. The
               negative margins cancel the padding+edge so at-rest geometry
               matches the prototype exactly; hover is the subtle row wash. -->
          <div
            dir="rtl"
            class={`cursor-pointer rounded-2 border-s-2 py-1.5 pe-2.5 ps-2.5 text-right outline-none transition-colors duration-hover ease-out -my-1.5 -me-2.5 -ms-3 focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-accent-border ${
              segment.idx === activeIdx
                ? 'border-s-accent bg-accent-wash-14'
                : 'border-s-transparent hover:bg-hover-row focus-visible:bg-accent-wash-12'
            }`}
            role="button"
            tabindex="0"
            aria-current={segment.idx === activeIdx ? 'true' : undefined}
            onclick={() => select(segment.idx)}
            onkeydown={(e) => selectKey(segment.idx, e)}
            data-testid="transcript-segment"
            data-seg-idx={segment.idx}
          >
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
