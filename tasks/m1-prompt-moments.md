# Task: m1-prompt-moments — free-prompt custom moment composition (fast-tracked MVP)

**Milestone:** M1+ (human fast-track 2026-07-24; MVP-first bias) · **Type:** full-stack · **Slug:** `m1-prompt-moments`

## Ruling

Free-prompt FIRST, no signal precompute (human challenged the value; deferred to M2 when
cross-episode search matters). One LLM call per prompt over the episode's own transcript
(~1–5¢); reuses ALL of the committed moments machinery (schema shape, verbatim-quote
validation, quote-anchored word-accurate bounds via asr.LocateQuote, /internal/llm
one-retry, llm_calls audit).

## Scope

1. **API:** `POST /api/episodes/{id}/moments/compose` body `{prompt}` (auth, org-scoped
   404, prompt length cap ~500 chars, rate-limit reuse of the token bucket e.g.
   6/min/org — user-initiated so no pipeline attempt counter, but every call audited in
   llm_calls as prompt_version-tagged compose). Requires segments to exist (else 409
   "not transcribed yet" neutral message). Response: EPHEMERAL moments-shaped list
   (rank, span, start/end ms word-accurate, rationale_en, quote_fa) — NOT persisted.
   The user prompt is passed as data with an instruction frame; validation identical to
   the stage (verbatim quotes etc.); 0 results is a VALID response ("no matches") — the
   window clamp does not apply to compose.
2. **internal/moments:** a Compose variant on the engine (same request builder + a
   user-prompt section; same validator minus the min-count clamp). The user prompt must
   be treated as content, never as system authority (prompt-injection posture: the
   instruction frame pins the JSON contract + verbatim rule; validation enforces
   regardless).
3. **Approve-to-keep:** approving a composed result persists it as a moments row at
   `rank = max(rank)+1` for the episode via a new store method (needs an additive
   `source text NOT NULL DEFAULT 'auto'` column, migration next free number; composed
   rows get source='prompt'). Persisted-then-approved rows behave like any moment
   (status transitions, future render). The UNIQUE(episode_id,rank) holds (next-free
   rank). Pipeline replace (reprocess) deletes ONLY source='auto' rows — composed keeps.
4. **UI:** a prompt input at the top of the MomentsRail ("COMPOSE" affordance per design
   conventions — tokens only, flag for DESIGN.md codification): submit → loading →
   results render as cards in a "PROMPT RESULTS" group (same card component; seek works;
   KEEP button = approve-to-persist, discard = drop from view). Empty result → neutral
   "no matches" message. Errors neutral. Keyboard reachable; axe.
5. **Tests:** engine compose validation (verbatim/injection-frame/0-results); API
   (org-scope, rate limit, length cap, 409 untranscribed, audit row); store
   (source column, next-free-rank persist, reprocess deletes auto-only); vitest + e2e
   (compose with the fake engine returning a fixture; keep→persists across reload).
   Fake engine gets a compose recording. Baselines: episode ×2 will drift (prompt input
   visible) — report, don't touch.

## Acceptance

- make check + make eval + e2e functional green. Reviewer verifies: verbatim + word
  accuracy on composed results, injection posture, rate limit, source-column semantics
  (reprocess spares composed), neutral errors.
- Architect post-deploy: compose "find the most controversial exchange" on the 3-speaker
  episode → real results render → keep one → survives reload. Human verifies.

## Evidence

Summary; diffs; gate transcripts; screenshots; baseline impact; open questions.
