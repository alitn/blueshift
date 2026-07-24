// Package diarizeeval holds the offline anchor-merge stability golden for LLM
// diarization — the diarization entry in the `make eval` golden suite CLAUDE.md
// names ("diarization text-anchor merge stability on fixtures").
//
// The property under test: given a fixed transcript and a fixed (recorded) model
// response, the diarizer produces the SAME speaker grouping, deterministically.
// The suite feeds committed segments through internal/diarize with a fake-backed
// llm.Client replaying a committed response, then compares the produced idx ->
// speaker_key grouping byte-for-byte to a committed golden. No provider is called
// and no database is touched.
//
// It is data-driven and language-agnostic: it discovers every language registered
// with internal/lang that declares an llm engine slot and, for each, runs its
// testdata/<code>/{segments.json, response.json} through the diarizer and compares
// to testdata/<code>/golden.json. A registered llm-capable language without eval
// fixtures fails the suite — new such languages must bring diarization fixtures.
//
// Goldens fail closed on any drift; regenerating them is an explicit, deliberate
// act (the -update flag), mirroring eval/lang and the visual-baseline discipline.
// See eval/README.md for the regeneration flow.
package diarizeeval
