/**
 * Client-side helpers for the neutral auth endpoints. The browser only ever
 * talks to /api/auth/*; it never sees the identity provider. All error copy
 * here is deliberately generic — nothing hints at the underlying stack.
 */

export type Me = {
  user: { email: string; name: string };
  org: { name: string };
  role: string;
};

export type LoginError = 'auth_failed' | 'rate_limited' | 'unavailable' | 'invalid' | 'network';

export type LoginResult = { ok: true; me: Me } | { ok: false; error: LoginError };

/** postLogin submits credentials and maps the response to a typed result. */
export async function postLogin(email: string, password: string): Promise<LoginResult> {
  let res: Response;
  try {
    res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ email, password })
    });
  } catch {
    return { ok: false, error: 'network' };
  }

  if (res.ok) {
    return { ok: true, me: (await res.json()) as Me };
  }
  switch (res.status) {
    case 401:
      return { ok: false, error: 'auth_failed' };
    case 429:
      return { ok: false, error: 'rate_limited' };
    case 400:
      return { ok: false, error: 'invalid' };
    default:
      return { ok: false, error: 'unavailable' };
  }
}

/** fetchMe returns the current session's identity, or null if unauthenticated. */
export async function fetchMe(): Promise<Me | null> {
  try {
    const res = await fetch('/api/auth/me', { credentials: 'same-origin' });
    if (res.ok) return (await res.json()) as Me;
    return null;
  } catch {
    return null;
  }
}

/** postLogout clears the session cookie server-side (best effort). */
export async function postLogout(): Promise<void> {
  try {
    await fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin' });
  } catch {
    /* best effort: the client redirects to /login regardless */
  }
}

/**
 * ensureSession is the shell's auth guard: if there is no valid session it
 * redirects to /login and reports false; otherwise true. redirect is injected
 * so it is testable without the router.
 */
export async function ensureSession(
  redirect: (path: string) => void | Promise<void>
): Promise<boolean> {
  const me = await fetchMe();
  if (!me) {
    await redirect('/login');
    return false;
  }
  return true;
}

/** loginErrorMessage maps a LoginError to a neutral, user-facing sentence. */
export function loginErrorMessage(error: LoginError): string {
  switch (error) {
    case 'auth_failed':
      return 'Incorrect email or password.';
    case 'rate_limited':
      return 'Too many attempts. Wait a minute and try again.';
    case 'unavailable':
      return 'Sign-in is temporarily unavailable. Try again shortly.';
    case 'invalid':
      return 'Enter your email and password.';
    case 'network':
      return 'Network error. Check your connection and try again.';
  }
}
