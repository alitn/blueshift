import { afterEach, describe, expect, it, vi } from 'vitest';
import { postLogin, fetchMe, ensureSession, loginErrorMessage } from './auth';

function mockFetch(status: number, body: unknown) {
  const res = {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body
  } as Response;
  return vi.fn().mockResolvedValue(res);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('postLogin', () => {
  it('returns the identity on success', async () => {
    const me = { user: { email: 'dev-approver@blueshift.local', name: 'Dev Approver' }, org: { name: 'Pilot' }, role: 'approver' };
    vi.stubGlobal('fetch', mockFetch(200, me));
    const result = await postLogin('dev-approver@blueshift.local', 'pw');
    expect(result).toEqual({ ok: true, me });
  });

  it('maps 401 to auth_failed', async () => {
    vi.stubGlobal('fetch', mockFetch(401, { error: 'auth_failed' }));
    const result = await postLogin('dev-approver@blueshift.local', 'bad');
    expect(result).toEqual({ ok: false, error: 'auth_failed' });
  });

  it('maps 429 to rate_limited and 503 to unavailable', async () => {
    vi.stubGlobal('fetch', mockFetch(429, {}));
    expect(await postLogin('a', 'b')).toEqual({ ok: false, error: 'rate_limited' });
    vi.stubGlobal('fetch', mockFetch(503, {}));
    expect(await postLogin('a', 'b')).toEqual({ ok: false, error: 'unavailable' });
  });

  it('maps a network failure to network', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockRejectedValue(new Error('boom'))
    );
    expect(await postLogin('a', 'b')).toEqual({ ok: false, error: 'network' });
  });
});

describe('fetchMe', () => {
  it('returns identity when authenticated', async () => {
    const me = { user: { email: 'a', name: 'A' }, org: { name: 'O' }, role: 'editor' };
    vi.stubGlobal('fetch', mockFetch(200, me));
    expect(await fetchMe()).toEqual(me);
  });

  it('returns null on 401', async () => {
    vi.stubGlobal('fetch', mockFetch(401, {}));
    expect(await fetchMe()).toBeNull();
  });
});

describe('ensureSession', () => {
  it('redirects to /login and reports false when unauthenticated', async () => {
    vi.stubGlobal('fetch', mockFetch(401, {}));
    const redirect = vi.fn();
    const ok = await ensureSession(redirect);
    expect(ok).toBe(false);
    expect(redirect).toHaveBeenCalledWith('/login');
  });

  it('does not redirect when authenticated', async () => {
    vi.stubGlobal('fetch', mockFetch(200, { user: {}, org: {}, role: 'editor' }));
    const redirect = vi.fn();
    const ok = await ensureSession(redirect);
    expect(ok).toBe(true);
    expect(redirect).not.toHaveBeenCalled();
  });
});

describe('loginErrorMessage', () => {
  // Copy is asserted verbatim: fixed, generic sentences that describe no
  // backend. The vendor-leak gate independently forbids provider names in web/.
  it('maps every error to fixed neutral copy', () => {
    expect(loginErrorMessage('auth_failed')).toBe('Incorrect email or password.');
    expect(loginErrorMessage('rate_limited')).toBe('Too many attempts. Wait a minute and try again.');
    expect(loginErrorMessage('unavailable')).toBe(
      'Sign-in is temporarily unavailable. Try again shortly.'
    );
    expect(loginErrorMessage('invalid')).toBe('Enter your email and password.');
    expect(loginErrorMessage('network')).toBe(
      'Network error. Check your connection and try again.'
    );
  });
});
