import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { toast, toasts } from './toast-store';

describe('toast store', () => {
  beforeEach(() => {
    toast.clear();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
    toast.clear();
  });

  it('pushes a toast onto the queue and returns its id', () => {
    const id = toast.push({ title: 'Saved' });
    const list = get(toasts);
    expect(list).toHaveLength(1);
    expect(list[0].id).toBe(id);
    expect(list[0].title).toBe('Saved');
    expect(list[0].variant).toBe('default');
  });

  it('preserves FIFO order across multiple pushes', () => {
    toast.push({ title: 'first' });
    toast.push({ title: 'second' });
    toast.push({ title: 'third' });
    expect(get(toasts).map((t) => t.title)).toEqual(['first', 'second', 'third']);
  });

  it('dismisses a specific toast by id without touching others', () => {
    const a = toast.push({ title: 'a', duration: 0 });
    toast.push({ title: 'b', duration: 0 });
    toast.dismiss(a);
    expect(get(toasts).map((t) => t.title)).toEqual(['b']);
  });

  it('auto-dismisses after the configured duration', () => {
    toast.push({ title: 'ephemeral', duration: 3000 });
    expect(get(toasts)).toHaveLength(1);
    vi.advanceTimersByTime(2999);
    expect(get(toasts)).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(get(toasts)).toHaveLength(0);
  });

  it('keeps sticky toasts (duration <= 0) until dismissed', () => {
    toast.push({ title: 'sticky', duration: 0 });
    vi.advanceTimersByTime(60_000);
    expect(get(toasts)).toHaveLength(1);
  });

  it('exposes variant helpers that tag the toast', () => {
    toast.ok({ title: 'ready' });
    toast.warn({ title: 'check' });
    toast.danger({ title: 'failed' });
    expect(get(toasts).map((t) => t.variant)).toEqual(['ok', 'warn', 'danger']);
  });

  it('clear() empties the queue and cancels pending timers', () => {
    toast.push({ title: 'x', duration: 1000 });
    toast.clear();
    expect(get(toasts)).toHaveLength(0);
    // Advancing past the original duration must not resurrect or error.
    vi.advanceTimersByTime(2000);
    expect(get(toasts)).toHaveLength(0);
  });
});
