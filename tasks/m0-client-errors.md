# Task: m0-client-errors — frontend errors into Cloud Logging via our API

**Milestone:** M0 observability (human-requested) · **Type:** full-stack small · **Slug:** `m0-client-errors`

## Ruling

No third-party error SDKs (Sentry is ADR-gated; client never talks to provider endpoints).
Frontend errors flow through our own API into structured server logs → Cloud Logging →
Error Reporting, where the Architect reads them autonomously.

## Scope

1. **API:** `POST /api/client-errors` (auth NOT required — errors on /login matter too, so
   mount it as a public exception like login; rate-limit hard: reuse the token-bucket at
   e.g. 10/min/IP; body {message, stack?, url, line?, col?, user_agent? } with tight length
   caps (message 2k, stack 8k — truncate, never reject for length); log at ERROR severity
   with fields prefixed client_* plus a random event id; respond 204 always (even on
   malformed body — this endpoint must never generate its own error loops).
2. **Web:** `window.onerror` + `unhandledrejection` handlers registered once in the root
   layout; forward via fetch keepalive; swallow forwarding failures silently; simple
   dedupe (same message+line within 30s sends once); no PII beyond what the browser
   provides in the error itself.
3. **Tests:** handler (rate limit, truncation, malformed body → 204, log fields neutral);
   web unit test for dedupe + handler registration.

## Acceptance

- make check green. Verify locally against the dev stack: throw a test error in the
  console → appears in the app's stdout log as structured ERROR with client_ fields.
- No vendor strings anywhere; endpoint DTO neutral.

## Evidence

Summary; diffs; local verification transcript; open questions.
