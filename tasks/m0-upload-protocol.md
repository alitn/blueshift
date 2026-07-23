# Task: m0-upload-protocol — server-initiated upload session (documented GCS pattern)

**Milestone:** M0 blocker (AC1) · **Type:** backend (blob + api) · **Slug:** `m0-upload-protocol`

## Problem (found live 2026-07-23, third AC1 attempt; researched, not guessed)

`/internal/blob` GCS backend returns a signed **resumable-initiation** URL
(`Method: POST`, `x-goog-resumable: start` signed). Per provider docs the initiation
POST **must have an empty body and `Content-Length: 0`**; the file bytes go in a
subsequent PUT to the session URI returned in the `Location` header. Our web client
(correctly, per its own contract) sends the file as the body of the single request the
server described — so the provider rejects with 400, which the browser surfaces as a
CORS failure (error responses carry no allow-origin header).

## Ruling — adopt the documented pattern, not a custom protocol

Per provider docs, signed URLs for resumable uploads are *generally unnecessary*: when a
backend exists, the backend initiates the session and hands the client the session URI,
which acts as a bearer auth token (valid ~1 week). Known cross-origin gotcha (documented
in provider issue trackers): the initiation request must carry the **browser's `Origin`
header** so subsequent browser PUTs to the session URI receive correct CORS headers.

Consequence: the web client's existing contract (single request, method+headers from
the DTO, file as body, XHR progress) is **already correct** and the local blob backend
already matches it. The client and local backend do not change.

## Scope

1. **`/internal/blob` interface:** `InitResumableUpload` (or successor) gains the
   requesting browser's `origin string` parameter.
2. **`gcs.go`:** initiate the session server-side — empty-body POST with
   `x-goog-resumable: start`, the object's `Content-Type`, `Content-Length: 0`, and
   `Origin: <browser origin>`; read the session URI from `Location` (case-insensitive);
   return `Upload{URL: <session URI>, Method: PUT, Headers: {Content-Type}}`.
   Implementation choice (implementer's): POST to a self-signed URL (reuses existing
   signing) or an authenticated XML-API call — either is fine inside `/internal/blob`.
   Map provider failures to neutral errors with internal error IDs, as elsewhere.
3. **`internal/api` episodes create:** pass the request's `Origin` header through to the
   blob layer (fall back to the configured public base URL when absent).
4. **`local.go`:** unchanged behavior; only the signature gains the ignored `origin`
   param. Client: unchanged. DTO: unchanged (URL/method/headers as today).

## Tests

- `gcs.go` unit (recorded/replayed HTTP, matching repo convention): empty init body and
  `Content-Length: 0` asserted; `Origin` forwarded; `Location` parsed case-insensitively;
  provider error → neutral error.
- API test: browser `Origin` reaches the blob layer; fallback applies when missing.
- Existing e2e/demo flows stay green (client contract unchanged — they already cover it).

## Acceptance

- make check green. Reviewer verifies: init request provably bodyless in a test; session
  URI never logged at INFO or below (it is a bearer credential — log only at DEBUG with
  redaction or not at all); DTO neutral; no client/web diff present.
- Architect (post-commit, operational): live prod check — browser upload completes; this
  is the AC1 gate retry.

## Evidence

Summary; diffs; test transcript; the recorded init request/response pair; open questions.
