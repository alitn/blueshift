# eval/ — offline golden suite (Go)

`make eval` runs the Go golden evaluations under `./eval/...`. These are the
committed-baseline checks CLAUDE.md calls the golden tests: today, the language
registry's text-normalization / ZWNJ-idempotency goldens (`eval/lang`), the
diarization anchor-merge stability goldens (`eval/diarize`), the pause-based
resegmentation goldens (`eval/segment` — a provider mega-segment split into
readable timed turns, byte-pinned), and the moment-selection stability goldens
(`eval/moments` — a committed model response replayed through the real
selector, spans/ranks/verbatim quotes byte-pinned). More land here as the
pipeline arrives (caption fidelity, `.ass` byte-exactness); the Python pipeline
suite in `tools/eval/` runs alongside once present.

CI runs `make eval` in the `check` job on every PR (`.github/workflows/pr.yml`).

## How the goldens work

`eval/lang` is language-agnostic: it discovers every language registered with
`internal/lang` and, for each `<code>`, reads `eval/lang/testdata/<code>/corpus.json`
(authored inputs), runs each input through that language's `Normalize`, and
compares the result **byte-for-byte** to `eval/lang/testdata/<code>/golden.json`.
It also asserts idempotency (`Normalize(Normalize(x)) == Normalize(x)`).

Adding a registered language without a `corpus.json` fails the suite — new
languages must bring eval fixtures.

`eval/diarize`, `eval/segment`, and `eval/moments` follow the same discipline
over their own fixtures: `eval/diarize` replays a committed model response
through the real diarizer and byte-compares the produced speaker grouping;
`eval/segment` runs a committed mega-segment transcript (the prod "whole take
as ONE segment" shape) through `asr.Resegment` at the default thresholds and
byte-compares the produced turns, hard-asserting verbatim word preservation
(incl. U+200C), ASR-only boundary times, bounds, and idempotence alongside the
golden; `eval/moments` replays a committed model response through the real
moment selector — including its verbatim-quote and span validation — and
byte-compares the produced ranked proposals. All discover languages from the
registry (by declared engine slot) and fail on a capable language without
fixtures.

## Fail-closed on drift

Any change in normalization output (or a changed corpus) makes the committed
golden no longer match, and the test **fails**. This is intentional: it mirrors
the visual-baseline discipline — the golden never updates as a side effect of
making a test pass.

## Regenerating goldens (deliberate act)

Regeneration is an explicit flag, and — like screenshot baselines — is an
Architect-authorized change, never a quiet fix:

```
go test ./eval/lang -run TestNormalizationGolden -update
go test ./eval/diarize -run TestDiarizeAnchorMergeGolden -update
go test ./eval/segment -run TestResegmentGolden -update
go test ./eval/moments -run TestMomentSelectionGolden -update
```

Review the resulting diff to `testdata/<code>/golden.json` before committing.
