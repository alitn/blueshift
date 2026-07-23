# Task: m1-lang-registry — /internal/lang registry + lang/fa + eval scaffold

**Milestone:** M1 · **Type:** backend (pure Go) · **Slug:** `m1-lang-registry`

## Context

Language is data, never identity (CLAUDE.md). Everything downstream — RTL, ZWNJ handling,
caption shaping, engine choice — resolves through `/internal/lang` at runtime from
`episodes.language` (BCP-47). Persian (`fa`) is the first content language. Adding a
language later = new `lang/<code>` package + config rows + fixtures; zero schema/UI change.

## Research requirement (standing rule)

Persian text normalization is a solved domain. Before implementing, research and cite in
code comments + the evidence report: Unicode Arabic joining/ZWNJ semantics (UAX + Unicode
chapter on Arabic script), and the normalization conventions established by mainstream
Persian NLP normalizers (e.g. the rule sets popularized by hazm/parsivar-class tools:
Arabic yeh U+064A → Farsi yeh U+06CC, Arabic kaf U+0643 → keheh U+06A9, digit folding,
ZWNJ canonicalization for affixes like می‌/ها). Do NOT invent normalization rules; adopt
the documented consensus subset needed for caption fidelity.

## Scope

1. **`/internal/lang` registry:** interface + registry keyed by BCP-47 code. A language
   declares at minimum: `Direction()` (ltr/rtl), `Normalize(string) string` (MUST be
   idempotent), `PreserveJoinControls` semantics for caption pipelines, and named
   engine-selection config keys (values live in config rows, not code — registry only
   declares the keys). Unknown language → explicit error, never a silent default.
   Registration via init or explicit table; no fa assumptions outside `lang/fa`.
2. **`lang/fa`:** pure functions implementing the researched normalization set:
   character folding (yeh/kaf variants → Persian forms), Arabic-Indic + Extended
   Arabic-Indic digit handling (pick ONE canonical form, cite the choice), ZWNJ
   canonicalization (collapse repeated ZWNJ, strip ZWNJ adjacent to non-joining context,
   preserve legitimate morphological ZWNJ — the verbatim invariant depends on this being
   deterministic), whitespace/invisible-char hygiene (BOM, LRM/RLM decisions — cite).
   Table-driven tests with real Persian fixtures; property test: `Normalize(Normalize(x))
   == Normalize(x)` over the fixture corpus + fuzz seed corpus.
3. **`make eval` scaffold:** new Makefile target running the eval suite (initially: the
   ZWNJ/normalization idempotency goldens under a build tag or dedicated ./eval/... test
   dir). Golden files under testdata/ with a documented regeneration flow that FAILS if
   goldens drift (regeneration must be an explicit flag, mirroring the visual-baseline
   discipline). Wire `make eval` into CI pr.yml `check` job (runs on every PR per
   CLAUDE.md). Keep runtime < 30s.

## Out of scope

ASR/LLM interfaces; caption shaping/rendering (M1 later tasks); any UI; any migration.

## Acceptance

- make check green; make eval green and wired into pr.yml.
- Reviewer verifies: normalization rules carry citations; idempotency property test
  exists and runs; registry rejects unknown languages; no `fa` string literals outside
  lang/fa and its tests/fixtures; eval goldens fail closed on drift.

## Evidence

Summary; diffs; test + eval transcript; the researched sources cited; open questions.
