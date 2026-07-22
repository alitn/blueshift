# Task: m0-auth — sign-in, sessions, deny-by-default authz

**Milestone:** M0 (docs/SPEC-M0.md §5) · **Type:** backend + minimal UI · **Slug:** `m0-auth`

## Goal

Every request is authenticated and org-scoped, deny-by-default. Identity Platform is the
production identity provider, invisible to the client; `make demo` works fully offline via a
dev auth mode.

## Architecture rulings (Architect — follow these)

- **The provider never touches the browser.** The web client talks only to our neutral
  endpoints (`/api/auth/login`, `/api/auth/logout`, `/api/auth/me`). The Go server calls
  Identity Platform server-side. Provider hostnames/names may appear ONLY in
  `internal/auth/` (server-side, boundary-mapped errors) — never in `web/`, `internal/api`
  DTOs, or client-visible errors (the gate enforces the first two; you enforce the rest).
- **Sessions are stateless signed cookies** (stdlib `crypto/hmac` SHA-256): payload = user
  public_id + org public_id + role + expiry (7d), key from `SESSION_SECRET` env (required in
  staging/prod; dev default with a WARN log). HttpOnly, `Secure` outside dev, SameSite=Lax,
  path=/. No session table, no revocation in M0.
- **Auth modes** via config `AUTH_MODE=identity|dev` (default `dev` when `ENV=dev`, required
  `identity` when `ENV` is staging/prod):
  - `identity`: `/api/auth/login` {email,password} → Identity Platform REST
    sign-in server-side → on success, look up `users` by email + membership role → set
    session cookie. Provider errors mapped to neutral messages + internal error id
    (raw error to server logs only).
  - `dev`: same endpoint; verifies password equals `DEV_PASSWORD` env (default
    `blueshift-dev`) and the email exists in seeded `users`; no network. This is what
    `make demo` uses.
- **Allowed dependency:** Identity Platform via **server-side REST using stdlib
  `net/http`** (no SDK — it's one endpoint; the admin SDK would drag a large tree without
  need). If during implementation you find REST insufficient (it isn't for password
  sign-in), stop and report rather than adding an SDK.

## Scope

1. **`internal/auth`:** session codec (mint/verify/expiry/tamper detection), login flows for
   both modes behind one interface, neutral error mapping (`auth_failed`, `auth_unavailable`
   + internal error id), cookie helpers.
2. **Middleware (`internal/server`):** authn middleware resolving the session cookie to a
   `Principal{UserPublicID, OrgPublicID, Role}` in context; **deny-by-default**: every
   `/api/*` route requires a valid session except `POST /api/auth/login`; `/healthz`,
   `/readyz`, and static/SPA paths stay public. 401 JSON for API, no redirects (SPA handles).
   Role gate helper `RequireRole("approver")` for later use.
3. **Endpoints (`internal/api`):** `POST /api/auth/login` (rate-limit lightly: 5/min/IP via
   in-memory token bucket, stdlib), `POST /api/auth/logout` (clears cookie),
   `GET /api/auth/me` → `{user, org, role}` using public ids only (`/internal/ids` for any
   id rendering; never internal bigints). DTOs neutral (gate greps `internal/api`).
4. **Login page (`web/src/routes/login`):** no design screen exists — keep it minimal and
   token-pure: centered `bg-4` card on `bg-2`, wordmark per DESIGN.md, email + password
   inputs, primary sign-in button, neutral error line (`danger` text). On success redirect
   to `/`. Add a tiny client auth guard: shell layout calls `/api/auth/me`; 401 → redirect
   to `/login`. Logout in the avatar dropdown (use the vendored dropdown-menu primitive).
5. **Config:** `AUTH_MODE`, `SESSION_SECRET`, `DEV_PASSWORD`, `IDP_API_KEY` (identity mode;
   from Secret Manager via env at deploy) added to `internal/config` with validation rules
   above.
6. **Tests:** session codec (round-trip, expiry, tamper, wrong key); middleware
   (no cookie → 401 on /api/*, public paths open, principal in context); login dev-mode
   (wrong password, unknown email, success sets cookie); identity-mode against a **fake
   local HTTP server fixture** (success + provider-error mapping — assert neutral message,
   no provider strings in response body); rate limiter; `/api/auth/me` DTO shape; web:
   component test for the login form logic + guard redirect. All race-clean.

## Out of scope

Admin/user management UI, password reset, MFA, refresh/revocation, per-route role
enforcement beyond the helper, `allow_self_approval` logic (M2), any provider SDK.

## Acceptance

- `make check` fully green. Vendor gate green (no provider strings in `web/` or
  `internal/api`; fake-fixture tests assert neutral client errors).
- With `AUTH_MODE=dev`: login with seeded `dev-approver@blueshift.local` + dev password → cookie set;
  `/api/auth/me` returns user+org+role; wrong password → 401 neutral error; all other
  `/api/*` 401 without cookie.
- App boots with no DB (login returns 503 `auth_unavailable` cleanly) — readyz already
  reflects DB state.
- Screenshot evidence of the login page (1440×900) to `.artifacts/screens/m0-auth/` (same
  capture method as m0-web-skeleton).

## Evidence to return

Summary + deviations; diffstat + status; tail of `make check`; curl transcript of the dev-mode
acceptance flow; screenshot path; open questions.
