/**
 * Encapsulated polling store for the Library's live status. In M0 "live" means a
 * 3-second GET /api/episodes poll that runs only while some episode is still in
 * a non-terminal state, and pauses entirely while the tab is hidden (Occam — no
 * SSE yet, per the Architect ruling). The store hides all of that behind
 * subscribe/refresh/start/stop, so a future SSE implementation can replace the
 * internals without the Library page changing.
 */
import { writable, type Readable } from 'svelte/store';
import { listEpisodes as defaultFetcher, isTerminal, type Episode } from './episodes';

export type EpisodesState = {
  episodes: Episode[];
  /** true once the first fetch has resolved (so the UI can distinguish empty
   *  from not-yet-loaded). */
  loaded: boolean;
  /** true when the most recent fetch failed. */
  error: boolean;
};

export type EpisodesStore = Readable<EpisodesState> & {
  /** Fetch once, immediately, updating the store. */
  refresh: () => Promise<void>;
  /** Begin polling (idempotent). Fetches immediately, then every intervalMs
   *  while any episode is non-terminal and the tab is visible. */
  start: () => void;
  /** Stop polling and detach listeners. */
  stop: () => void;
};

/** A minimal document surface so tests can drive visibility without a real DOM. */
export type VisibilityDoc = Pick<
  Document,
  'hidden' | 'addEventListener' | 'removeEventListener'
>;

export type PollOptions = {
  fetcher?: () => Promise<Episode[]>;
  intervalMs?: number;
  doc?: VisibilityDoc | null;
};

export function createEpisodesStore(opts: PollOptions = {}): EpisodesStore {
  const fetcher = opts.fetcher ?? defaultFetcher;
  const intervalMs = opts.intervalMs ?? 3000;
  const doc: VisibilityDoc | null =
    opts.doc !== undefined ? opts.doc : typeof document !== 'undefined' ? document : null;

  const store = writable<EpisodesState>({ episodes: [], loaded: false, error: false });

  let handle: ReturnType<typeof setTimeout> | null = null;
  let running = false;
  // Whether the last known state still has work to watch. Kept true on error so
  // a transient failure retries rather than silently freezing the view.
  let pending = false;
  // True while a fetch is awaiting. It is the concurrency guard that keeps the
  // loop a single logical timeline: a redundant start()/refresh() or a
  // visibility resume that lands while a fetch is in flight collapses into the
  // running fetch instead of firing a second, overlapping request. Combined with
  // the single `handle` (always cleared before it is reassigned), the store holds
  // at most one in-flight fetch and one scheduled tick no matter how many times
  // polling is (re)started.
  let inFlight = false;

  function clearHandle() {
    if (handle !== null) {
      clearTimeout(handle);
      handle = null;
    }
  }

  function schedule() {
    clearHandle();
    if (!running || !pending) return;
    if (doc?.hidden) return; // paused while hidden; onVisibility resumes it
    handle = setTimeout(() => void poll(), intervalMs);
  }

  async function poll() {
    if (inFlight) return; // a fetch is already running; don't stack another
    clearHandle();
    inFlight = true;
    try {
      const episodes = await fetcher();
      pending = episodes.some((e) => !isTerminal(e.status));
      store.set({ episodes, loaded: true, error: false });
    } catch {
      pending = true; // retry on the next tick
      store.update((s) => ({ ...s, loaded: true, error: true }));
    } finally {
      inFlight = false;
    }
    schedule();
  }

  function onVisibility() {
    if (!running) return;
    if (doc?.hidden) {
      clearHandle();
    } else if (pending) {
      void poll();
    }
  }

  function start() {
    if (!running) {
      running = true;
      doc?.addEventListener('visibilitychange', onVisibility);
      void poll();
      return;
    }
    // Already polling. Re-ignite only when the loop has gone idle — no tick
    // scheduled and no fetch in flight — which is exactly the case a new
    // upload/retry needs restarted after every episode reached a terminal state.
    // A start() while the loop is live is a no-op, so repeated (re)starts can
    // never accelerate the cadence past the single intervalMs timer.
    if (handle === null && !inFlight) void poll();
  }

  function stop() {
    running = false;
    clearHandle();
    doc?.removeEventListener('visibilitychange', onVisibility);
  }

  return { subscribe: store.subscribe, refresh: poll, start, stop };
}
