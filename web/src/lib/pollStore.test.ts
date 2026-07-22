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
});
