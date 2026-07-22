# tools/eval/

Offline Python evaluation scripts — **never deployed**, never imported by the product.
Entry point (arrives with the M1 pipeline): `run.py --fixtures fixtures/`, invoked by
`make eval`. Planned checks:

- WER against gold transcripts (per language, via the lang registry's normalization).
- Diarization text-anchor merge stability on fixtures.
- Caption fidelity checker: must catch 100% of seeded mismatches.
- ZWNJ normalization idempotence (fa).
- Rendered `.ass` files byte-identical to committed goldens.

CI runs `make eval` on every PR; any change to prompts or `/internal/llm` requires it.
