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
  // COMPOSE (m1-prompt-moments): a free-prompt input at the top of the rail.
  // Submit runs one engine call over the episode's own transcript and renders
  // the EPHEMERAL results as a "PROMPT RESULTS" group of the same cards —
  // nothing persists until KEEP (approve-to-persist: the kept moment joins
  // the ranked list as an approved moment at its next free rank). DISCARD
  // drops a result from view; an empty result set is the valid, neutral
  // "no matches" answer. Focused result cards: K keeps, D discards,
  // Enter/Space seeks. Styling is tokens-only; the compose affordance is
  // flagged for DESIGN.md codification.
  //
  // `load`/`save`/`compose`/`keep` are injected (defaults = the real client)
  // so states and the optimistic flow are testable.
  import {
    composeMoments,
    fetchMoments,
    keepComposedMoment,
    setMomentStatus,
    type ComposedMoment,
    type EpisodeMoments,
    type Moment,
    type MomentStatus
  } from '$lib/moments';
  import { formatTimecode } from '$lib/timecode';

  let {
    episodeId,
    load = fetchMoments,
    save = setMomentStatus,
    compose = composeMoments,
    keep = keepComposedMoment,
    onSeek
  }: {
    episodeId: string;
    load?: (id: string) => Promise<EpisodeMoments>;
    save?: (id: string, rank: number, status: MomentStatus) => Promise<Moment>;
    compose?: (id: string, prompt: string) => Promise<ComposedMoment[]>;
    keep?: (id: string, m: ComposedMoment) => Promise<Moment>;
    /** A card was activated — seek the player to this offset (ms). */
    onSeek?: (ms: number) => void;
  } = $props();

  type Status = 'loading' | 'loaded' | 'error';
  let status = $state<Status>('loading');
  let moments = $state<Moment[]>([]);

  // Compose flow state: idle (nothing asked) → running → results | empty |
  // error; keep failures keep the results on screen and surface their own
  // neutral line.
  type ComposeState = 'idle' | 'running' | 'results' | 'empty' | 'error';
  let composeState = $state<ComposeState>('idle');
  let prompt = $state('');
  let composed = $state<ComposedMoment[]>([]);
  let keepFailed = $state(false);
  let composeSeq = 0; // stale-response guard for fast re-submits/navigation

  // Load on mount and whenever the episode changes; a stale request is ignored
  // so a fast re-navigation never lands an earlier episode's moments. The
  // compose flow resets with the episode — results never leak across.
  $effect(() => {
    const id = episodeId;
    let cancelled = false;
    status = 'loading';
    moments = [];
    composeSeq++;
    prompt = '';
    composed = [];
    composeState = 'idle';
    keepFailed = false;
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

  /** submitCompose runs the prompt; a blank prompt or an in-flight call is a
   *  no-op. Stale responses (a re-submit or an episode switch raced ahead)
   *  are dropped by the sequence guard. */
  function submitCompose(event: SubmitEvent): void {
    event.preventDefault();
    const p = prompt.trim();
    if (p === '' || composeState === 'running') return;
    const seq = ++composeSeq;
    composeState = 'running';
    composed = [];
    keepFailed = false;
    compose(episodeId, p)
      .then((results) => {
        if (seq !== composeSeq) return;
        composed = results;
        composeState = results.length > 0 ? 'results' : 'empty';
      })
      .catch(() => {
        if (seq !== composeSeq) return;
        composeState = 'error';
      });
  }

  /**
   * keepComposed persists one result (approve-to-persist). On success the
   * card leaves the results group and the returned moment — approved, at its
   * next free rank — joins the ranked list, from where it behaves like any
   * other moment. A refusal leaves the card in place with a neutral line.
   */
  function keepComposed(c: ComposedMoment): void {
    keepFailed = false;
    keep(episodeId, c)
      .then((m) => {
        composed = composed.filter((x) => x !== c);
        moments = [...moments, m].sort((a, b) => a.rank - b.rank);
        if (composed.length === 0) composeState = 'idle';
      })
      .catch(() => {
        keepFailed = true;
      });
  }

  /** discardComposed drops one result from view — ephemeral, nothing to undo. */
  function discardComposed(c: ComposedMoment): void {
    keepFailed = false;
    composed = composed.filter((x) => x !== c);
    if (composed.length === 0) composeState = 'idle';
  }

  /**
   * composedKey: single-key handling on a focused result card — K keeps,
   * D discards; Enter/Space (on the card itself) seeks, mirroring the ranked
   * cards. Modified keys pass through.
   */
  function composedKey(c: ComposedMoment, event: KeyboardEvent): void {
    if (event.ctrlKey || event.metaKey || event.altKey) return;
    const key = event.key.toLowerCase();
    if (key === 'k') {
      event.preventDefault();
      keepComposed(c);
    } else if (key === 'd') {
      event.preventDefault();
      discardComposed(c);
    } else if ((event.key === 'Enter' || event.key === ' ') && event.target === event.currentTarget) {
      event.preventDefault();
      onSeek?.(c.startMs);
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

  <!-- COMPOSE: the free-prompt affordance (tokens only; flagged for DESIGN.md
       codification). Submit → one engine call → the PROMPT RESULTS group
       below; the inline lines cover running / no-matches / failure, all
       neutral. -->
  <form
    class="flex-none border-b border-border-subtle p-3"
    onsubmit={submitCompose}
    data-testid="compose-form"
  >
    <div
      class="flex items-center gap-2 rounded-3 border border-border-strong px-2.5 py-2 transition-colors duration-hover ease-out focus-within:border-accent-border"
    >
      <input
        type="text"
        dir="auto"
        bind:value={prompt}
        placeholder="Describe a moment to find…"
        aria-label="Compose moments from a prompt"
        maxlength="500"
        class="w-full bg-transparent text-[11.5px] text-text-primary outline-none placeholder:text-text-faint"
        data-testid="compose-input"
      />
      <button
        type="submit"
        disabled={composeState === 'running' || prompt.trim() === ''}
        class="flex-none cursor-pointer rounded-2 bg-accent px-2 py-[4px] font-semibold text-[10.5px] leading-none tracking-[0.1em] text-text-on-accent transition-colors duration-hover ease-out hover:bg-accent-bright disabled:cursor-default disabled:opacity-[0.35]"
        data-testid="compose-submit"
      >
        COMPOSE
      </button>
    </div>
    {#if composeState === 'running'}
      <div
        class="mt-2 font-semibold text-[10.5px] tracking-[0.16em] text-text-faint"
        role="status"
        data-testid="compose-loading"
      >
        COMPOSING…
      </div>
    {:else if composeState === 'empty'}
      <div
        class="mt-2 text-[11.5px] leading-relaxed text-text-muted"
        role="status"
        data-testid="compose-empty"
      >
        No matches — nothing in this episode fits that prompt.
      </div>
    {:else if composeState === 'error'}
      <div
        class="mt-2 text-[11.5px] leading-relaxed text-danger"
        role="status"
        data-testid="compose-error"
      >
        Compose failed. Try again shortly.
      </div>
    {/if}
  </form>

  <!-- Body: ranked cards, or a neutral loading / awaiting / error state. -->
  <div class="flex min-h-0 flex-1 flex-col overflow-auto">
    <!-- PROMPT RESULTS: the ephemeral composed cards. Same card visuals as the
         ranked list; KEEP persists (approve-to-persist), DISCARD drops. -->
    {#if composed.length > 0}
      <div
        class="flex flex-none flex-col gap-2.5 border-b border-border-subtle p-3"
        data-testid="compose-results"
      >
        <div class="flex items-center justify-between">
          <span class="font-semibold text-[10.5px] tracking-[0.16em] text-text-muted">
            PROMPT RESULTS
          </span>
          <span
            class="font-semibold text-[10.5px] tracking-[0.08em] text-text-faint"
            data-testid="compose-summary"
          >
            {composed.length} {composed.length === 1 ? 'MATCH' : 'MATCHES'}
          </span>
        </div>
        {#if keepFailed}
          <div
            class="text-[11.5px] leading-relaxed text-danger"
            role="status"
            data-testid="compose-keep-error"
          >
            Couldn’t keep that moment. Try again shortly.
          </div>
        {/if}
        {#each composed as c (c.rank)}
          <!-- One ephemeral result card: same treatment as a ranked card;
               click / Enter / Space seeks, K keeps, D discards. -->
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
          <div
            role="group"
            aria-label={`Prompt result ${c.rank}`}
            tabindex="0"
            class="cursor-pointer rounded-4 border border-border-default bg-bg-4 p-3 outline-none transition-colors duration-hover ease-out focus-visible:bg-accent-wash-12 focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-accent-border"
            onclick={() => onSeek?.(c.startMs)}
            onkeydown={(e) => composedKey(c, e)}
            data-testid="composed-card"
            data-rank={c.rank}
          >
            <div dir="ltr" class="flex items-center gap-2 font-mono">
              <span class="font-semibold text-[12px] text-text-primary" data-testid="composed-rank">
                #{c.rank}
              </span>
              <span class="text-[10.5px] text-text-faint" data-testid="composed-range">
                {formatTimecode(c.startMs)}–{formatTimecode(c.endMs)}
              </span>
            </div>

            <div
              dir="ltr"
              class="mb-1.5 mt-2 text-left text-[13px] font-medium leading-[1.45] text-text-primary"
              data-testid="composed-rationale"
            >
              {c.rationaleEn}
            </div>
            <!-- Verbatim Persian quote: RTL, font-fa, ZWNJ preserved byte-exact. -->
            <div
              dir="rtl"
              class="mb-[11px] border-s-2 border-border-strong ps-2.5 text-right font-fa text-[12px] leading-[1.9] text-text-muted"
              data-testid="composed-quote"
            >
              <bdi>{c.quoteFa}</bdi>
            </div>

            <div dir="ltr" class="flex items-center gap-2">
              <button
                type="button"
                class="flex-1 cursor-pointer rounded-3 bg-accent py-[7px] text-center font-semibold text-[11px] tracking-[0.1em] text-text-on-accent transition-colors duration-hover ease-out hover:bg-accent-bright"
                onclick={(e) => {
                  e.stopPropagation();
                  keepComposed(c);
                }}
                data-testid="composed-keep"
              >
                KEEP
              </button>
              <button
                type="button"
                class="cursor-pointer rounded-3 px-1.5 py-[7px] font-medium text-[11px] tracking-[0.08em] text-text-muted transition-colors duration-hover ease-out hover:text-text-primary"
                onclick={(e) => {
                  e.stopPropagation();
                  discardComposed(c);
                }}
                data-testid="composed-discard"
              >
                DISCARD
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
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
