# Task: m1-moments-stage — LLM-proposed ranked moments (fast-tracked, human wants it today)

**Milestone:** M1 · **Type:** backend (stage + migration) · **Slug:** `m1-moments-stage`
**Fast-track ruling (Architect, 2026-07-24):** speaker-NAMING, shots/reframe, and
embeddings are NOT prerequisites — naming decorates cards later, shots matter only at
render, embeddings serve the out-of-scope search box. Moments needs only segments (live)
+ /internal/llm (live).

## Scope

1. **Additive migration (next free number):** `moments` table per CLAUDE.md hierarchy:
   `id identity, episode_id FK, rank int` (1 = best), segment span `start_idx int,
   end_idx int` (inclusive segment idx range) + `start_ms int, end_ms int` (derived from
   the span's first/last segment — ASR times only), `rationale_en text`, `quote_fa text`,
   `status text CHECK IN ('proposed','approved','dismissed') DEFAULT 'proposed'`,
   `status_changed_at timestamptz NULL`, `created_at`. UNIQUE(episode_id, rank).
   sqlc: idempotent replace-per-episode insert (tx, like segments); list by episode
   rank-ordered; a status-update (org-scoped via episode join, proposed↔approved/
   dismissed transitions).
2. **`internal/moments` engine** (mirror internal/diarize's shape): builds the LLM
   request from idx-ordered segments `{idx, text, start_ms, end_ms, speaker_key?}`
   (times MAY be sent here — the LLM cites spans it selects, it never invents times;
   output references segment idxs ONLY). Schema: `{moments:[{rank, start_idx, end_idx,
   rationale_en, quote_fa}]}` — 3..8 moments, ranks 1..n contiguous, idx ranges valid +
   non-overlapping, **quote_fa MUST be a verbatim contiguous substring of the span's
   joined segment text** (VALIDATE this — the verbatim invariant; invalid → the
   /internal/llm one-retry-then-fail path). English rationale, Persian quote (per SPEC).
   **AMENDED 2026-07-24 (human caught spec drift — SPEC-M1 requires word offsets):**
   `start_ms/end_ms` are WORD-ACCURATE: locate the validated quote in the span's word
   sequence (same join rule as resegmentation; first-in-span if ambiguous) → start =
   quote's first word's start_ms, end = quote's last word's end_ms — ASR times looked up
   by the STAGE, never emitted by the LLM. Alignment failure → invalid-output path.
   Moment precision is thereby independent of segment length.
3. **Stage `moments`** registered after diarize in the REGISTRY; terminal when active.
   Cost-safety EXACTLY like diarize: skip-if-moments-exist, BeginBillableAttempt before
   the call, reprocess-only rebill. Fake engine fixture (deterministic, matches the demo
   transcript — pin like diarize's) for demo/CI.
4. **Chain:** demo lib.sh + deploy.yml → `PIPELINE_STAGES=ingest,transcribe,diarize,moments`;
   demo seeds through moments (fake). Prod uses the existing bs-lm-1 engine (same env).
5. **Tests:** DB-backed replace/idempotency/rank-order/status transitions (org-scoped);
   stage test w/ fake (exact rows, verbatim quote enforced — a fixture with a
   non-substring quote must fail validation); 4-stage re-drive bills ZERO; eval golden
   for moment stability (mirror eval/diarize; -update only). make check + make eval +
   make e2e functional green. Baselines: library.png ×2 will drift AGAIN (4th bar) —
   report, don't touch; episode view untouched by this task (rail is the NEXT task).

## Acceptance

- Reviewer verifies: verbatim-quote validation is load-bearing (mutation), idx-span
  validation exact, cost-safety parity with diarize, chain order, no provider leaks.
- Architect post-deploy: fresh upload of the 3-speaker excerpt → 4 stages → moments rows
  exist via API/DB (the RAIL renders them in the next task).

## Evidence

Summary; diffs; gate transcripts; fixture list; baseline impact; open questions.
