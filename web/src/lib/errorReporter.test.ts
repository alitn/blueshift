import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createErrorReporter, registerErrorReporting } from './errorReporter';

describe('createErrorReporter dedupe', () => {
  it('sends once for the same message+line within the window', () => {
    let t = 1000;
    const fetch = vi.fn().mockResolvedValue({ ok: true } as Response);
    const { report } = createErrorReporter({ fetch, now: () => t, dedupeWindowMs: 30_000 });

    expect(report({ message: 'boom', url: '/x', line: 5 })).toBe(true);
    t += 10_000; // still inside the 30s window
    expect(report({ message: 'boom', url: '/x', line: 5 })).toBe(false);
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('sends again after the window elapses', () => {
    let t = 0;
    const fetch = vi.fn().mockResolvedValue({ ok: true } as Response);
    const { report } = createErrorReporter({ fetch, now: () => t, dedupeWindowMs: 30_000 });

    expect(report({ message: 'boom', url: '/x', line: 5 })).toBe(true);
    t += 30_001;
    expect(report({ message: 'boom', url: '/x', line: 5 })).toBe(true);
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it('treats a different message or line as distinct', () => {
    const t = 0;
    const fetch = vi.fn().mockResolvedValue({ ok: true } as Response);
    const { report } = createErrorReporter({ fetch, now: () => t });

    expect(report({ message: 'boom', url: '/x', line: 5 })).toBe(true);
    expect(report({ message: 'other', url: '/x', line: 5 })).toBe(true);
    expect(report({ message: 'boom', url: '/x', line: 6 })).toBe(true);
    expect(fetch).toHaveBeenCalledTimes(3);
  });

  it('suppresses empty/whitespace messages without calling fetch', () => {
    const fetch = vi.fn().mockResolvedValue({ ok: true } as Response);
    const { report } = createErrorReporter({ fetch });
    expect(report({ message: '   ', url: '/x' })).toBe(false);
    expect(report({ message: '', url: '/x' })).toBe(false);
    expect(fetch).not.toHaveBeenCalled();
  });

  it('posts to the neutral endpoint with keepalive and caps message/stack', () => {
    const fetch = vi.fn().mockResolvedValue({ ok: true } as Response);
    const { report } = createErrorReporter({ fetch });
    report({ message: 'x'.repeat(5000), stack: 'y'.repeat(20000), url: '/login', line: 1, col: 2 });

    expect(fetch).toHaveBeenCalledTimes(1);
    const [url, init] = fetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/client-errors');
    expect(init.method).toBe('POST');
    expect(init.keepalive).toBe(true);
    const body = JSON.parse(init.body as string);
    expect(body.message.length).toBe(2000);
    expect(body.stack.length).toBe(8000);
    expect(body.line).toBe(1);
    expect(body.col).toBe(2);
  });

  it('swallows a rejected forward without throwing', async () => {
    const fetch = vi.fn().mockRejectedValue(new Error('network down'));
    const { report } = createErrorReporter({ fetch });
    expect(() => report({ message: 'boom', url: '/x' })).not.toThrow();
    await Promise.resolve();
  });
});

describe('registerErrorReporting', () => {
  type Listener = (e: unknown) => void;

  function fakeTarget() {
    const listeners: Record<string, Listener[]> = {};
    return {
      addEventListener: vi.fn((type: string, fn: Listener) => {
        (listeners[type] ??= []).push(fn);
      }),
      removeEventListener: vi.fn((type: string, fn: Listener) => {
        listeners[type] = (listeners[type] ?? []).filter((l) => l !== fn);
      }),
      location: { href: '/login' },
      navigator: { userAgent: 'TestUA/1.0' },
      emit(type: string, e: unknown) {
        for (const l of listeners[type] ?? []) l(e);
      }
    };
  }

  let fetch: ReturnType<typeof vi.fn>;
  let teardown: (() => void) | undefined;

  beforeEach(() => {
    fetch = vi.fn().mockResolvedValue({ ok: true } as Response);
  });

  afterEach(() => {
    teardown?.();
    teardown = undefined;
    vi.restoreAllMocks();
  });

  it('registers error + unhandledrejection listeners exactly once', () => {
    const target = fakeTarget();
    teardown = registerErrorReporting(target as never, { fetch });
    // A second call before teardown must be a no-op (registered once).
    const noop = registerErrorReporting(target as never, { fetch });
    noop();

    expect(target.addEventListener).toHaveBeenCalledTimes(2);
    expect(target.addEventListener).toHaveBeenCalledWith('error', expect.any(Function));
    expect(target.addEventListener).toHaveBeenCalledWith(
      'unhandledrejection',
      expect.any(Function)
    );
  });

  it('forwards an uncaught error event through the reporter', () => {
    const target = fakeTarget();
    teardown = registerErrorReporting(target as never, { fetch });

    target.emit('error', {
      message: 'TypeError: boom',
      error: new Error('TypeError: boom'),
      filename: 'https://app/login',
      lineno: 12,
      colno: 3
    });

    expect(fetch).toHaveBeenCalledTimes(1);
    const body = JSON.parse((fetch.mock.calls[0][1] as RequestInit).body as string);
    expect(body.message).toBe('TypeError: boom');
    expect(body.url).toBe('https://app/login');
    expect(body.line).toBe(12);
    expect(body.col).toBe(3);
    expect(body.user_agent).toBe('TestUA/1.0');
  });

  it('forwards an unhandled promise rejection', () => {
    const target = fakeTarget();
    teardown = registerErrorReporting(target as never, { fetch });

    target.emit('unhandledrejection', { reason: new Error('rejected!') });

    expect(fetch).toHaveBeenCalledTimes(1);
    const body = JSON.parse((fetch.mock.calls[0][1] as RequestInit).body as string);
    expect(body.message).toBe('rejected!');
    expect(body.url).toBe('/login');
  });

  it('removes listeners on teardown and re-arms the guard', () => {
    const target = fakeTarget();
    const stop = registerErrorReporting(target as never, { fetch });
    stop();
    expect(target.removeEventListener).toHaveBeenCalledTimes(2);

    // After teardown, registration works again.
    teardown = registerErrorReporting(target as never, { fetch });
    expect(target.addEventListener).toHaveBeenCalledTimes(4);
  });
});
