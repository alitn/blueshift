<script lang="ts">
  // Moments rail (prototype screens 01/02, right column): the bg-3 side panel
  // listing the episode's ranked moment proposals as bg-4 cards — rank chip,
  // mm:ss–mm:ss quote window, English rationale (LTR), and the verbatim
  // Persian quote (RTL, font-fa, <bdi>, ZWNJ byte-exact — the verbatim
  // invariant; nothing here normalizes). Neutral throughout: no state names
  // the engine that proposed anything. All colour/type/spacing from tokens.
  //
  // Review actions (SPEC-M1 single-key approve): each card carries APPROVE /
  // DISMISS (proposed) or UNDO (approved/dismissed — the reverse transition
  // back to proposed). Cards are focusable and keyboard-operable: A approves,
  // D dismisses the focused card; Enter/Space (on the card itself) seeks.
  // Status flips are optimistic — the card updates instantly and reverts if
  // the API refuses — and only legal transitions are ever attempted
  // (proposed -> approved/dismissed, approved/dismissed -> proposed).
  //
  // Clicking a card seeks the player to the moment's start (the host wires
  // onSeek to the play-state-preserving seek; the transcript highlight follows
  // the playhead on its own). Button clicks do not seek — stopPropagation
  // keeps review actions and navigation separate.
  //
  // `load`/`save` are injected (defaults = the real client) so states and the
  // optimistic flow are testable.
  import {
    fetchMoments,
    setMomentStatus,
    type EpisodeMoments,
    type Moment,
    type MomentStatus
  } from '$lib/moments';
  import { formatTimecode } from '$lib/timecode';

  let {
    episodeId,
    load = fetchMoments,
    save = setMomentStatus,
    onSeek
  }: {
    episodeId: string;
    load?: (id: string) => Promise<EpisodeMoments>;
    save?: (id: string, rank: number, status: MomentStatus) => Promise<Moment>;
    /** A card was activated — seek the player to this offset (ms). */
    onSeek?: (ms: number) => void;
  } = $props();

  type Status = 'loading' | 'loaded' | 'error';
  let status = $state<Status>('loading');
  let moments = $state<Moment[]>([]);

  // Load on mount and whenever the episode changes; a stale request is ignored
  // so a fast re-navigation never lands an earlier episode's moments.
  $effect(() => {
    const id = episodeId;
    let cancelled = false;
    status = 'loading';
    moments = [];
    load(id)
      .then((m) => {
        if (!cancelled) {
          moments = m.moments;
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

  const summary = $derived(`${moments.length} CANDIDATES · RANKED`);

  /** legalTransition mirrors the API's review state machine. */
  function legalTransition(from: MomentStatus, to: MomentStatus): boolean {
    if (from === 'proposed') return to === 'approved' || to === 'dismissed';
    return to === 'proposed'; // approved/dismissed -> proposed (the undo)
  }

  /**
   * setStatus flips a card optimistically: the UI updates instantly, the API
   * call follows, and a refusal (illegal transition, lost row, network) puts
   * the previous status back. Illegal requests are never sent.
   */
  function setStatus(m: Moment, to: MomentStatus): void {
    if (!legalTransition(m.status, to)) return;
    const prev = m.status;
    m.status = to;
    save(episodeId, m.rank, to)
      .then((updated) => {
        m.status = updated.status;
      })
      .catch(() => {
        m.status = prev;
      });
  }

  function seek(m: Moment): void {
    onSeek?.(m.startMs);
  }

  /**
   * cardKey: single-key review on the focused card — A approves, D dismisses
   * (proposed cards only; the guard in setStatus refuses the rest). Enter and
   * Space seek, but only when the card itself is focused, so activating an
   * inner button never double-fires a seek. Modified keys pass through.
   */
  function cardKey(m: Moment, event: KeyboardEvent): void {
    if (event.ctrlKey || event.metaKey || event.altKey) return;
    const key = event.key.toLowerCase();
    if (key === 'a') {
      event.preventDefault();
      setStatus(m, 'approved');
    } else if (key === 'd') {
      event.preventDefault();
      setStatus(m, 'dismissed');
    } else if ((event.key === 'Enter' || event.key === ' ') && event.target === event.currentTarget) {
      event.preventDefault(); // Space must not scroll the rail
      seek(m);
    }
  }
</script>

<section
  class="flex h-full min-h-0 flex-col bg-bg-3"
  aria-label="Moments"
  data-testid="moments-rail"
>
  <!-- Panel header (screens 01/02: 40px, "MOMENTS" label + ranked summary) -->
  <div
    class="flex h-[40px] flex-none items-center justify-between border-b border-border-subtle px-3.5"
  >
    <span class="font-semibold text-[11px] tracking-[0.16em] text-text-muted">MOMENTS</span>
    {#if status === 'loaded' && moments.length > 0}
      <span
        class="font-semibold text-[10.5px] tracking-[0.08em] text-text-faint"
        data-testid="moments-summary"
      >
        {summary}
      </span>
    {/if}
  </div>

  <!-- Body: ranked cards, or a neutral loading / awaiting / error state. -->
  <div class="flex min-h-0 flex-1 flex-col overflow-auto">
    {#if status === 'loading'}
      <div
        class="flex flex-1 flex-col items-center justify-center px-6 text-center"
        data-testid="moments-loading"
      >
        <span class="font-semibold text-[11px] tracking-[0.16em] text-text-faint">
          LOADING MOMENTS…
        </span>
      </div>
    {:else if status === 'error'}
      <div
        class="flex flex-1 flex-col items-center justify-center gap-1.5 px-6 text-center"
        data-testid="moments-error"
      >
        <span class="font-semibold text-[11px] tracking-[0.16em] text-danger">
          MOMENTS UNAVAILABLE
        </span>
        <span class="text-[11.5px] leading-relaxed text-text-muted">
          The moments couldn’t be loaded. Try again shortly.
        </span>
      </div>
    {:else if moments.length === 0}
      <div
        class="flex flex-1 flex-col items-center justify-center gap-1.5 px-6 text-center"
        data-testid="moments-empty"
      >
        <span class="font-semibold text-[11px] tracking-[0.16em] text-text-faint">
          AWAITING MOMENTS
        </span>
        <span class="text-[11.5px] leading-relaxed text-text-muted">
          No proposals yet — analysis hasn’t ranked this episode’s moments.
        </span>
      </div>
    {:else}
      <div class="flex flex-col gap-2.5 p-3">
        {#each moments as m (m.rank)}
          <!-- One ranked card. A focusable group (not role=button — it holds
               real buttons): click / Enter / Space seeks, A / D review it.
               Approved carries the accent border + chip; dismissed sinks to
               the disabled emphasis (opacity 0.35, the DESIGN.md disabled
               convention) but stays in rank position and operable. -->
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
          <div
            role="group"
            aria-label={`Moment ${m.rank}`}
            tabindex="0"
            class={`cursor-pointer rounded-4 border bg-bg-4 p-3 outline-none transition-colors duration-hover ease-out focus-visible:bg-accent-wash-12 focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-accent-border ${
              m.status === 'approved' ? 'border-accent-border' : 'border-border-default'
            }`}
            onclick={() => seek(m)}
            onkeydown={(e) => cardKey(m, e)}
            data-testid="moment-card"
            data-rank={m.rank}
            data-status={m.status}
          >
            <!-- Metadata row stays LTR: rank + quote window left, status right. -->
            <div dir="ltr" class="flex items-center gap-2 font-mono">
              <span class="font-semibold text-[12px] text-text-primary" data-testid="moment-rank">
                #{m.rank}
              </span>
              <span class="text-[10.5px] text-text-faint" data-testid="moment-range">
                {formatTimecode(m.startMs)}–{formatTimecode(m.endMs)}
              </span>
              <div class="flex-1"></div>
              {#if m.status === 'approved'}
                <span
                  class="rounded-2 border border-accent-border bg-accent-wash-18 px-2 py-[3px] text-[10.5px] leading-none tracking-[0.12em] text-accent-bright"
                  data-testid="moment-status"
                >
                  APPROVED
                </span>
              {:else if m.status === 'dismissed'}
                <span
                  class="rounded-2 border border-border-strong px-2 py-[3px] text-[10.5px] leading-none tracking-[0.12em] text-text-faint"
                  data-testid="moment-status"
                >
                  DISMISSED
                </span>
              {/if}
            </div>

            <div class={m.status === 'dismissed' ? 'opacity-[0.35]' : ''}>
              <!-- English rationale: LTR editorial line. -->
              <div
                dir="ltr"
                class="mb-1.5 mt-2 text-left text-[13px] font-medium leading-[1.45] text-text-primary"
                data-testid="moment-rationale"
              >
                {m.rationaleEn}
              </div>
              <!-- Verbatim Persian quote: RTL, font-fa, inline-start rule,
                   ZWNJ preserved byte-exact. -->
              <div
                dir="rtl"
                class="mb-[11px] border-s-2 border-border-strong ps-2.5 text-right font-fa text-[12px] leading-[1.9] text-text-muted"
                data-testid="moment-quote"
              >
                <bdi>{m.quoteFa}</bdi>
              </div>
            </div>

            <!-- Review actions. Button clicks never seek (stopPropagation). -->
            <div dir="ltr" class="flex items-center gap-2">
              {#if m.status === 'proposed'}
                <button
                  type="button"
                  class="flex-1 cursor-pointer rounded-3 bg-accent py-[7px] text-center font-semibold text-[11px] tracking-[0.1em] text-text-on-accent transition-colors duration-hover ease-out hover:bg-accent-bright"
                  onclick={(e) => {
                    e.stopPropagation();
                    setStatus(m, 'approved');
                  }}
                  data-testid="moment-approve"
                >
                  APPROVE
                </button>
                <button
                  type="button"
                  class="cursor-pointer rounded-3 px-1.5 py-[7px] font-medium text-[11px] tracking-[0.08em] text-text-muted transition-colors duration-hover ease-out hover:text-text-primary"
                  onclick={(e) => {
                    e.stopPropagation();
                    setStatus(m, 'dismissed');
                  }}
                  data-testid="moment-dismiss"
                >
                  DISMISS
                </button>
              {:else}
                <div class="flex-1"></div>
                <button
                  type="button"
                  class="cursor-pointer rounded-3 px-1.5 py-[7px] font-medium text-[11px] tracking-[0.08em] text-text-muted transition-colors duration-hover ease-out hover:text-text-primary"
                  onclick={(e) => {
                    e.stopPropagation();
                    setStatus(m, 'proposed');
                  }}
                  data-testid="moment-undo"
                >
                  UNDO
                </button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</section>
