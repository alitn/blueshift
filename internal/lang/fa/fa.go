// Package fa implements the Persian (BCP-47 "fa") content language for the
// lang registry. It is pure: normalization is a set of deterministic functions
// with no I/O and no shared mutable state after init. Everything Persian-specific
// lives here — no other package encodes "fa" assumptions (CLAUDE.md). Importing
// this package (including for its side effect) registers it with internal/lang.
package fa

import "blueshift/internal/lang"

// code is the canonical BCP-47 tag this language registers under.
const code = "fa"

// persian is the lang.Language implementation for Persian.
type persian struct{}

// Code returns the canonical BCP-47 primary tag.
func (persian) Code() string { return code }

// Direction reports Persian's base writing direction: right-to-left.
func (persian) Direction() lang.Direction { return lang.RTL }

// Normalize applies the Persian normalization rule set (see normalize.go).
func (persian) Normalize(s string) string { return Normalize(s) }

// PreserveJoinControls is true: Persian uses U+200C (ZWNJ) as a morphological
// non-joiner (verb prefixes, plural/comparative suffixes), so the caption
// pipeline must preserve join controls when shaping and breaking lines.
func (persian) PreserveJoinControls() bool { return true }

// EngineKeys declares the engine-selection slots Persian needs. The concrete
// engines are config data, never code.
func (persian) EngineKeys() []lang.EngineKey {
	return []lang.EngineKey{lang.EngineASR, lang.EngineLLM, lang.EngineCaptionShaper}
}

func init() { lang.Register(persian{}) }
