# SPEC-M1 — The demo that sells (weeks 3–9)

**Goal:** the golden path + trust machinery ONLY, on the full schema. One hour of Persian TV
interview in; ranked, evidence-backed moments out; three clicks to an approved, captioned,
platform-ready clip. Everything that makes an editor trust the system is in; everything else
is deliberately out and additive later.

**Demo at gate:** `docs/DEMO.md` runs end-to-end on staging in under 15 minutes with zero
live-processing waits (sample episode pre-processed).

## In scope

### Pipeline (worker stages, per episode)

1. **Transcribe** — ASR via `/internal/asr` (Chirp 2 impl): word-level timestamps, glossary
   biasing from `glossary_terms` (by `episodes.language`). Segments + words persisted per the
   schema in `CLAUDE.md`; embeddings computed per segment (pgvector).
2. **Diarize + name** — LLM diarization via `/internal/llm`, **text-anchored** (anchors into
   ASR text; timestamps still come only from ASR). Speaker naming with recorded **evidence**:
   intro quote + lower-third OCR crop image. Merged against `speaker_directory`. Anchor-merge
   stability covered by golden tests (`make eval`).
3. **Moments** — LLM proposes candidate moments referencing segment spans + word offsets;
   ranked; each card carries an **English rationale** and a **Persian evidence quote**
   (quote text copied verbatim from ASR — the verbatim invariant, enforced by test).
4. **Render** — ffmpeg cut at approved bounds; per-shot 9:16 crop driven by LLM-provided
   bboxes (shots from scdet); libass caption burn, ZWNJ-safe, shaped via `/internal/lang`;
   outputs to `clips/`, fidelity-checked before being marked ready.

### Product surface

- **Moment cards** — ranked list per episode; actions: **Approve-as-is / Adjust / Dismiss**;
  single-key approve; every action audited.
- **In-place clip editor** — sentence-selection trim bound to segment/word data; filmstrip
  ±3s around each cut point; flash-frame warning (cut lands mid-shot near a shot boundary);
  live Persian caption preview (RTL, ZWNJ-preserving, styled from tokens); J/K/L transport.
- **Per-shot 9:16 reframe preview** from stored bboxes; editable crop offset per shot.
- **Caption fidelity checker** — rendered caption text must equal ASR words for the span,
  byte-for-byte after normalization; any mismatch **blocks approval** (UI surface + server
  enforcement). Catches 100% of seeded mismatches in `make eval`.
- **Render drawer** — presets **Reels + Telegram only** (presets are config rows, not code);
  progress; download via scoped signed URL.
- **First-run** — sample pre-processed episode present on first login (from fixtures).
- **Trust rails** — self-approval on (`allow_self_approval=true`); full audit trail
  (`llm_calls`, `correction_log`, approval events).

### Corrections

Editing a caption/transcript segment rewrites that segment's `words` and inserts into
`correction_log` (PG18 OLD/NEW RETURNING). Corrections feed glossary suggestions later (M3+),
but are recorded now.

## Out of scope (ALL additive later — no schema or structural changes required)

Publishing APIs; scheduling; batch-approve; prompt-search box; cover-frame picker (auto-pick
first clean frame instead); Shorts/X presets; approvals inbox UI; org/team admin UI;
comments/assignments; L2+ intelligence.

## Acceptance criteria

1. The 1-hour fixture yields **≥5 ranked moments** with rationale + verbatim evidence quote.
2. **Three clicks** from opening an episode to an approved clip (approve → render → done).
3. A **seeded caption mismatch blocks approval** — demonstrated in `make eval` and in the UI.
4. `docs/DEMO.md` runs end-to-end on staging in **<15 min** with zero live-processing waits.
5. All M1 UI satisfies the UI Definition of Done (visual baselines, RTL/ZWNJ assertions,
   token conformance, axe smoke, keyboard paths).
6. `make eval` green: anchor-merge stability, fidelity checker 100% on seeded mismatches,
   ZWNJ normalization idempotent, rendered `.ass` byte-identical to goldens.
7. Vendor-leak gate green; no provider hint on any client-visible surface (engine labels are
   `bs-asr-1`, `bs-lm-2.1`, `ENGINE … MS`).

## Task decomposition

The Architect will split M1 into ~15–25 single-day tasks in `tasks/queue.md` after M0 ships,
roughly: lang registry + `lang/fa` (ZWNJ/normalization, pure funcs) → asr interface + impl →
transcribe stage → diarize+name stage (+evidence) → moments stage → segments/moments API →
transcript UI → moment rail → editor (trim → filmstrip → captions preview → reframe) →
fidelity checker → render stage → render drawer → first-run seed → demo script hardening.
