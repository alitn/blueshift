// Package langeval holds the offline golden evaluation for the language
// registry's text normalization. It is the first entry in the `make eval`
// golden suite (CLAUDE.md): the ZWNJ/normalization idempotency goldens.
//
// The suite is data-driven and language-agnostic: it discovers every language
// registered with internal/lang and, for each, runs the committed input corpus
// through Normalize and compares the result to a committed golden byte-for-byte.
// Goldens fail closed on any drift; regenerating them is an explicit, deliberate
// act (the -update flag), mirroring the visual-baseline discipline. See
// eval/README.md for the regeneration flow.
package langeval
