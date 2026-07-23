# Task: m1-asr-interface — /internal/asr engine interface + fixtures

**Milestone:** M1 · **Type:** backend (interface only) · **Slug:** `m1-asr-interface`

## Context

ASR sits behind `/internal/asr` (CLAUDE.md); first impl (next task) is Chirp 2, but the
interface is engine-neutral and the neutral label (`bs-asr-1`) is the only thing clients
ever see. Timestamps come only from ASR (verbatim invariant); word-level timing is the
foundation for segments, captions, and the editor.

## Scope

1. **Interface:** `Engine.Transcribe(ctx, TranscribeRequest) (Transcript, error)`.
   Request: audio object reference (bucket key — engines fetch via storage, callers never
   stream bytes through the interface), BCP-47 language, optional glossary bias terms
   (from `glossary_terms` by language), engine options (neutral keys declared via
   /internal/lang registry). Transcript: ordered segments of speaker-agnostic utterances
   with `Words []Word{Text, StartMs, EndMs, Conf}` — millisecond ints (schema convention),
   never floats-of-seconds at the boundary; plus engine label + raw metadata blob for
   audit. Document invariants: words non-overlapping, monotonic, within segment bounds —
   and provide a `Validate()` used by tests and callers.
2. **Registry:** neutral label → engine impl lookup (mirror /internal/lang and the
   /internal/llm pattern); unknown label → explicit error. Config decides the label per
   language via /internal/lang engine-selection keys.
3. **Fixture harness:** a `fake` engine for tests/demo (deterministic, loads recorded
   transcript fixtures from testdata; used by make demo later). At least one realistic
   Persian fixture with word timings (hand-checkable, short) committed under testdata.
4. **Tests:** Validate() property tests (overlaps, non-monotonic, out-of-bounds all
   rejected); registry unknown-label error; fake engine determinism; vendor gate —
   nothing in exported types/errors names a provider.

## Out of scope

Chirp/provider impl (m1-asr-impl); worker stage; persistence; embeddings.

## Acceptance

- make check green. Reviewer verifies: interface leaks no provider concepts; ms-int
  convention throughout; Validate() rejects each malformed-shape class; fake engine is
  deterministic and offline.

## Evidence

Summary; diffs; test transcript; open questions.
