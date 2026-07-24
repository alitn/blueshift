import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { createEpisodesStore, type VisibilityDoc } from './pollStore';
import type { Episode } from './episodes';

function ep(id: string, status: Episode['status']): Episode {
  return {
    id,
    title: 'T',
    sourceFilename: 'f.mp4',
    language: 'fa',
    status,
    hasMaster: status !== 'uploaded',
    uploadedAt: '2026-07-01T00:00:00Z'
  };
}

// fakeDoc is a controllable visibility surface: toggle hidden and fire the
// visibilitychange listener the store registers.
function fakeDoc() {
  const listeners: Record<string, ((e: Event) => void)[]> = {};
  return {
    hidden: false,
    addEventListener(type: string, cb: EventListenerOrEventListenerObject) {
      (listeners[type] ||= []).push(cb as (e: Event) => void);
    },
    removeEventListener(type: string, cb: EventListenerOrEventListenerObject) {
      listeners[type] = (listeners[type] || []).filter((l) => l !== cb);
    },
    fire(type: string) {
      for (const l of listeners[type] || []) l(new Event(type));
    }
  };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
});

describe('createEpisodesStore', () => {
  it('polls immediately then every intervalMs while an episode is non-terminal', async () => {
    const fetcher = vi.fn(async () => [ep('ep_a', 'processing')]);
    const store = createEpisodesStore({ fetcher, intervalMs: 3000, doc: fakeDoc() });

    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(get(store).episodes[0].status).toBe('processing');

    await vi.advanceTimersByTimeAsync(3000);
    expect(fetcher).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(3000);
    expect(fetcher).toHaveBeenCalledTimes(3);

    store.stop();
  });

  it('repeated start() never stacks: one immediate fetch, one timer, 3s cadence', async () => {
    // Mirrors the Library page calling start() from onMount and again from every
    // onUploaded/onRetry. A redundant start() must not add an overlapping poll or
    // reset the interval — exactly one active timer, at intervalMs, throughout.
    const fetcher = vi.fn(async () => [ep('ep_a', 'processing')]);
    const store = createEpisodesStore({ fetcher, intervalMs: 3000, doc: fakeDoc() });

    store.start();
    store.start();
    store.start();
    await vi.advanceTimersByTimeAsync(0);
    // Three starts, still a single immediate fetch and a single scheduled tick.
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(1);

    // Starting again mid-loop stays a no-op — the cadence is unchanged.
    store.start();
    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(1);

    // Steady 3s cadence: one poll per interval, not faster.
    await vi.advanceTimersByTimeAsync(3000);
    expect(fetcher).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(3000);
    expect(fetcher).toHaveBeenCalledTimes(3);

    store.stop();
  });

  it('refresh()+start() together (the onUploaded path) fire a single fetch', async () => {
    // The Library's onUploaded does `void refresh(); start();`. The two must
    // collapse into one request, not two concurrent /api/episodes calls.
    const fetcher = vi.fn(async () => [ep('ep_a', 'processing')]);
    const store = createEpisodesStore({ fetcher, intervalMs: 3000, doc: fakeDoc() });

    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetcher).toHaveBeenCalledTimes(1);

    fetcher.mockClear();
    void store.refresh();
    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetcher).toHaveBeenCalledTimes(1); // collapsed, not doubled
    expect(vi.getTimerCount()).toBe(1);

    store.stop();
  });

  it('start() re-ignites a single poll after the loop idled (all terminal)', async () => {
    // Once every episode is terminal the loop parks (no scheduled tick). A later
    // upload/retry calls start() again, which must resume exactly one poll.
    const fetcher = vi
      .fn<() => Promise<Episode[]>>()
      .mockResolvedValueOnce([ep('ep_a', 'ready')]) // terminal -> loop idles
      .mockResolvedValue([ep('ep_a', 'processing')]);
    const store = createEpisodesStore({ fetcher, intervalMs: 3000, doc: fakeDoc() });

    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0); // idle: nothing scheduled

    store.start(); // a new upload/retry resumes polling
    await vi.advanceTimersByTimeAsync(0);
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(vi.getTimerCount()).toBe(1);

    store.stop();
  });

  it('stops polling once every episode is terminal', async () => {
    const fetcher = vi
      .fn<() => Promise<Episode[]>>()
      .mockResolvedValueOnce([ep('ep_a', 'processing')])
      .mockResolvedValue([ep('ep_a', 'ready')]);
    const store = createEpisodesStore({ fetcher, intervalMs: 3000, doc: fakeDoc() });

    store.start();
    await vi.advanceTimersByTimeAsync(0); // 1st: processing
    await vi.advanceTimersByTimeAsync(3000); // 2nd: ready -> terminal
    expect(fetcher).toHaveBeenCalledTimes(2);

    // No further polls: the state is terminal.
    await vi.advanceTimersByTimeAsync(9000);
    expect(fetcher).toHaveBeenCalledTimes(2);

    store.stop();
  });

  it('pauses while the tab is hidden and resumes on visibility', async () => {
    const doc = fakeDoc();
    const fetcher = vi.fn(async () => [ep('ep_a', 'processing')]);
    const store = createEpisodesStore({ fetcher, intervalMs: 3000, doc });

    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetcher).toHaveBeenCalledTimes(1);

    // Hide the tab: the scheduled poll is cancelled.
    doc.hidden = true;
    doc.fire('visibilitychange');
    await vi.advanceTimersByTimeAsync(9000);
    expect(fetcher).toHaveBeenCalledTimes(1);

    // Reveal: it polls immediately and resumes the interval.
    doc.hidden = false;
    doc.fire('visibilitychange');
    await vi.advanceTimersByTimeAsync(0);
    expect(fetcher).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(3000);
    expect(fetcher).toHaveBeenCalledTimes(3);

    store.stop();
  });

  it('marks error and retries on the next tick', async () => {
    const fetcher = vi
      .fn<() => Promise<Episode[]>>()
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValue([ep('ep_a', 'ready')]);
    const store = createEpisodesStore({ fetcher, intervalMs: 3000, doc: fakeDoc() });

    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(get(store).error).toBe(true);

    await vi.advanceTimersByTimeAsync(3000);
    expect(get(store).error).toBe(false);
    expect(get(store).episodes[0].status).toBe('ready');

    store.stop();
  });

  it('stop() halts polling and detaches the visibility listener', async () => {
    const doc = fakeDoc() as VisibilityDoc & { fire: (t: string) => void };
    const removeSpy = vi.spyOn(doc, 'removeEventListener');
    const fetcher = vi.fn(async () => [ep('ep_a', 'processing')]);
    const store = createEpisodesStore({ fetcher, intervalMs: 3000, doc });

    store.start();
    await vi.advanceTimersByTimeAsync(0);
    store.stop();
    expect(removeSpy).toHaveBeenCalledWith('visibilitychange', expect.any(Function));

    await vi.advanceTimersByTimeAsync(9000);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it('remove(id) drops exactly that episode locally, without a refetch', async () => {
    const fetcher = vi.fn(async () => [ep('ep_a', 'ready'), ep('ep_b', 'ready')]);
    const store = createEpisodesStore({ fetcher, intervalMs: 3000, doc: fakeDoc() });

    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(get(store).episodes.map((e) => e.id)).toEqual(['ep_a', 'ep_b']);

    store.remove('ep_a');
    expect(get(store).episodes.map((e) => e.id)).toEqual(['ep_b']);
    // Optimistic: purely local — no extra fetch fired.
    expect(fetcher).toHaveBeenCalledTimes(1);

    // Removing an id that is not present is a harmless no-op.
    store.remove('ep_missing');
    expect(get(store).episodes.map((e) => e.id)).toEqual(['ep_b']);

    store.stop();
  });
});
