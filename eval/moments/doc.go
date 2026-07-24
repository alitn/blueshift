// Package momentseval holds the offline moment-selection stability golden —
// the moments entry in the `make eval` golden suite, mirroring eval/diarize.
//
// The property under test: given a fixed transcript and a fixed (recorded)
// model response, the moment selector produces the SAME ranked proposal set,
// deterministically — spans, ranks, rationales, and verbatim quotes all
// byte-stable. The suite feeds committed speaker-aware segments through
// internal/moments with a fake-backed llm.Client replaying a committed
// response, then compares the produced rank-ordered proposals byte-for-byte to
// a committed golden. The run passes through the engine's REAL validation
// (count window, contiguous ranks, non-overlapping spans, verbatim-quote
// substring gate), so a fixture that violates any invariant fails the suite
// outright. No provider is called and no database is touched.
//
// It is data-driven and language-agnostic: it discovers every language
// registered with internal/lang that declares an llm engine slot and, for
// each, runs its testdata/<code>/{segments.json, response.json} through the
// selector and compares to testdata/<code>/golden.json. A registered
// llm-capable language without moment fixtures fails the suite — new such
// languages must bring moment fixtures.
//
// Goldens fail closed on any drift; regenerating them is an explicit,
// deliberate act (the -update flag), mirroring eval/diarize and the
// visual-baseline discipline. See eval/README.md for the regeneration flow.
package momentseval
