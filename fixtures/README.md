# fixtures/

Gold test data — the offline ground truth for `make demo`, `make eval`, and recorded-replay
tests. Expected contents (human/pipeline supplies the media; agents never fabricate gold
data):

- `clips/` — 3 short gold Persian clips (source video snippets).
- `transcripts/` — hand-verified gold transcripts (segments + word timings) for each clip.
- `episode-sample/` — the 1-hour sample episode used by first-run and the M1 demo.
- `recorded/` — recorded API JSON for ASR/LLM replay in tests (recorded once from live,
  committed, replayed forever; a nightly live smoke on one fixture detects drift).

Everything here must be redistributable and free of secrets.
