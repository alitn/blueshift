// Package segmenteval holds the offline pause-based resegmentation golden —
// the `make eval` entry pinning how asr.Resegment splits a provider
// "mega-segment" into readable timed turns.
//
// The property under test: given a fixed mega-segment transcript (the
// 2026-07-24 prod wire shape — a whole 4-minute take returned as ONE segment
// of 641 word-timed words; the committed fixture is a deterministic synthetic
// equivalent of that receipt, since the real prod transcript lives only in the
// prod database), the DEFAULT thresholds produce the SAME segmentation,
// byte-for-byte, deterministically. The suite also hard-asserts the invariants
// that must hold regardless of the golden: the flattened word sequence —
// text bytes (including U+200C ZWNJ), timings, confidence, order — crosses
// unchanged (verbatim: segmentation only regroups), every produced boundary
// uses only ASR-measured word times, output segments respect the word/duration
// bounds, the result is Validate-green, and resegmenting the output is a
// no-op (idempotence). No provider is called and no database is touched.
//
// It is data-driven and language-agnostic: it discovers every language
// registered with internal/lang that declares an asr engine slot and, for
// each, runs its testdata/<code>/mega.json through asr.Resegment and compares
// to testdata/<code>/golden.json. A registered asr-capable language without a
// mega fixture fails the suite — new such languages must bring one.
//
// Goldens fail closed on any drift; regenerating them is an explicit,
// deliberate act (the -update flag), mirroring eval/lang, eval/diarize, and
// the visual-baseline discipline. See eval/README.md for the regeneration flow.
package segmenteval
