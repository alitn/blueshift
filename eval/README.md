# eval/ — offline golden suite (Go)

`make eval` runs the Go golden evaluations under `./eval/...`. These are the
committed-baseline checks CLAUDE.md calls the golden tests: today, the language
registry's text-normalization / ZWNJ-idempotency goldens (`eval/lang`). More
land here as the pipeline arrives (diarization anchor stability, caption
fidelity, `.ass` byte-exactness); the Python pipeline suite in `tools/eval/`
runs alongside once present.

CI runs `make eval` in the `check` job on every PR (`.github/workflows/pr.yml`).

## How the goldens work

`eval/lang` is language-agnostic: it discovers every language registered with
`internal/lang` and, for each `<code>`, reads `eval/lang/testdata/<code>/corpus.json`
(authored inputs), runs each input through that language's `Normalize`, and
compares the result **byte-for-byte** to `eval/lang/testdata/<code>/golden.json`.
It also asserts idempotency (`Normalize(Normalize(x)) == Normalize(x)`).

Adding a registered language without a `corpus.json` fails the suite — new
languages must bring eval fixtures.

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
```

Review the resulting diff to `testdata/<code>/golden.json` before committing.
