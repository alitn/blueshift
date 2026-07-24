# Task: m1-segments-api — read an episode's transcript over the API

**Milestone:** M1 · **Type:** backend (api) · **Slug:** `m1-segments-api`
**Part of the transcript vertical slice** (segments-api → transcript-ui → real-Chirp activation).
No billable calls — pure read of already-persisted segments.

## Scope

1. **Endpoint:** `GET /api/episodes/{id}/transcript` — auth required, org-scoped (the
   episode must belong to the principal's org; else 404, never cross-org read). Returns
   the episode's segments ordered by `idx`.
2. **Neutral DTO** (no provider names, no internal bigint ids, no storage keys — vendor
   gate + Reviewer enforce): each segment → `{ idx, start_ms, end_ms, text, speaker_key,
   words: [[w, start_ms, end_ms, conf], …] }`. Include the words array (the transcript UI
   needs word-level timing); `speaker_key` is nullable (null until diarized). Wrap in
   `{ episode_id: <prefixed public id>, language, segments: [...] }`.
3. **Empty/not-ready cases:** an episode with no segments yet → `{ …, segments: [] }`
   (200, not an error) so the UI can render an "awaiting transcript" state; unknown/foreign
   episode → 404.
4. **Payload note:** a ~1 h interview may be hundreds–low-thousands of segments with word
   arrays. For M1 return the full ordered set in one response (document the size); add
   cursor pagination only if a real payload proves too large (out of scope now — note it).
5. **Tests:** DB-backed (seed segments incl. one with speaker_key + a ZWNJ in text →
   assert DTO shape, ordering, verbatim text incl. U+200C, words positional array intact);
   org-scoping (foreign episode → 404); empty-segments → 200 empty; auth required
   (unauth → 401); vendor-leak assertion on the response. Web client: a typed
   `fetchTranscript(id)` in web/src/lib with a unit test (mirrors episodes.ts patterns) —
   the transcript UI task consumes it.

## Out of scope

The transcript UI rendering (next task); producing segments (that's transcribe, real-Chirp
activation); pagination; corrections/editing.

## Acceptance

- make check green; DB-backed API tests + web client unit test run.
- Reviewer verifies: org-scoping (no cross-tenant read), neutral DTO (no provider/id/key
  leak), verbatim words/text, empty + 404 + 401 cases, ordering by idx.

## Evidence

Summary; diffs; test transcript; a sample DTO payload; open questions.
