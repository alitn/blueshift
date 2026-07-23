/**
 * errorReporter forwards uncaught browser errors to our own API so they land in
 * structured server logs (Cloud Logging -> Error Reporting). There is no
 * third-party error SDK: the browser only ever talks to /api/client-errors.
 *
 * Rules baked in here:
 * - forwarding failures are swallowed silently — this must never create an
 *   error loop;
 * - a simple dedupe (same message + line within a 30s window) sends once;
 * - no PII is added beyond what the browser already put in the error itself.
 */

const ENDPOINT = '/api/client-errors';
const DEDUPE_WINDOW_MS = 30_000;
const MESSAGE_CAP = 2000;
const STACK_CAP = 8000;

/** ClientErrorReport is the neutral payload posted to the API. */
export type ClientErrorReport = {
  message: string;
  stack?: string;
  url: string;
  line?: number;
  col?: number;
  user_agent?: string;
};

/** Minimal window surface the reporter needs, so tests can pass a fake target. */
export type ErrorTarget = Pick<Window, 'addEventListener' | 'removeEventListener'> & {
  location?: { href?: string };
  navigator?: { userAgent?: string };
};

export type ReporterOptions = {
  /** Injectable fetch (defaults to the global). */
  fetch?: typeof fetch;
  /** Injectable clock in ms (defaults to Date.now), for deterministic dedupe tests. */
  now?: () => number;
  /** Dedupe window in ms (defaults to 30s). */
  dedupeWindowMs?: number;
};

function cap(s: string, max: number): string {
  return s.length > max ? s.slice(0, max) : s;
}

/**
 * createErrorReporter returns a reporter with its own dedupe state. Prefer this
 * over reaching for module-level state so each caller (and each test) is
 * isolated.
 */
export function createErrorReporter(opts: ReporterOptions = {}) {
  const doFetch = opts.fetch ?? globalThis.fetch?.bind(globalThis);
  const clock = opts.now ?? (() => Date.now());
  const dedupeWindowMs = opts.dedupeWindowMs ?? DEDUPE_WINDOW_MS;
  // key = message + '|' + line -> last time it was forwarded.
  const lastSent = new Map<string, number>();

  function send(report: ClientErrorReport): void {
    if (!doFetch) return;
    try {
      const body = JSON.stringify({
        message: cap(report.message, MESSAGE_CAP),
        stack: report.stack ? cap(report.stack, STACK_CAP) : undefined,
        url: report.url,
        line: report.line,
        col: report.col,
        user_agent: report.user_agent
      });
      // keepalive lets the POST survive a page unload/navigation.
      void doFetch(ENDPOINT, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        credentials: 'same-origin',
        keepalive: true,
        body
      }).catch(() => {
        /* swallow: never surface a forwarding failure */
      });
    } catch {
      /* stringify or synchronous fetch failure: swallow */
    }
  }

  /**
   * report forwards one error, applying dedupe. Returns true if it was
   * forwarded, false if suppressed (empty message, or a duplicate within the
   * window).
   */
  function report(input: ClientErrorReport): boolean {
    const message = input.message?.trim();
    if (!message) return false;
    const key = `${message}|${input.line ?? ''}`;
    const t = clock();
    const prev = lastSent.get(key);
    if (prev !== undefined && t - prev < dedupeWindowMs) return false;
    lastSent.set(key, t);
    send({ ...input, message });
    return true;
  }

  return { report };
}

// Module-level guard so the window handlers are registered exactly once, even if
// the root layout mounts more than once in a session.
let registered = false;

/**
 * registerErrorReporting attaches 'error' and 'unhandledrejection' listeners to
 * the target (defaults to window) and forwards them through a reporter. It is
 * idempotent — a second call before teardown is a no-op. The returned function
 * removes the listeners and re-arms the guard.
 */
export function registerErrorReporting(
  target: ErrorTarget = window,
  opts: ReporterOptions = {}
): () => void {
  if (registered) return () => {};
  registered = true;
  const reporter = createErrorReporter(opts);

  const onError = (event: ErrorEvent): void => {
    const err = event.error;
    reporter.report({
      message: event.message || (err instanceof Error ? err.message : String(err ?? 'error')),
      stack: err instanceof Error ? err.stack : undefined,
      url: event.filename || target.location?.href || '',
      line: event.lineno,
      col: event.colno,
      user_agent: target.navigator?.userAgent
    });
  };

  const onRejection = (event: PromiseRejectionEvent): void => {
    const reason = event.reason;
    reporter.report({
      message:
        reason instanceof Error ? reason.message : String(reason ?? 'unhandled rejection'),
      stack: reason instanceof Error ? reason.stack : undefined,
      url: target.location?.href || '',
      user_agent: target.navigator?.userAgent
    });
  };

  target.addEventListener('error', onError as EventListener);
  target.addEventListener('unhandledrejection', onRejection as EventListener);

  return () => {
    target.removeEventListener('error', onError as EventListener);
    target.removeEventListener('unhandledrejection', onRejection as EventListener);
    registered = false;
  };
}
