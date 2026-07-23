// Package lang is the runtime registry through which everything language-specific
// is resolved from data, never hardcoded. An episode carries a BCP-47 language
// tag (episodes.language); downstream stages — RTL layout, ZWNJ handling,
// caption shaping, engine selection — ask the registry for that tag's Language
// and act on what it declares. Adding a content language is a new lang/<code>
// package plus config rows and fixtures; no code here changes, and no
// language-specific behaviour ("fa assumptions") lives outside its lang/<code>
// package. Unknown tags fail with an explicit error, never a silent default.
package lang

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Direction is the primary writing direction a language declares. The caption
// and transcript UIs set base direction from this; there is no per-string
// direction guessing.
type Direction string

const (
	// LTR is left-to-right (e.g. English).
	LTR Direction = "ltr"
	// RTL is right-to-left (e.g. Persian, Arabic).
	RTL Direction = "rtl"
)

// EngineKey names an engine-selection slot a language needs. The registry only
// DECLARES which slots a language uses; the concrete engine bound to a slot is
// data (a config row), never code — keeping vendor/model choices out of the
// codebase (CLAUDE.md, "Vendor neutrality"). Key names are neutral abstract
// slots; they never carry provider or model names.
type EngineKey string

const (
	// EngineASR selects the speech-recognition engine for a language.
	EngineASR EngineKey = "asr"
	// EngineLLM selects the language model used for that language's copy tasks.
	EngineLLM EngineKey = "llm"
	// EngineCaptionShaper selects the caption text-shaping engine (relevant for
	// complex/cursive scripts that need contextual shaping and line breaking).
	EngineCaptionShaper EngineKey = "caption_shaper"
)

// Language is everything downstream resolves at runtime for one content
// language. Implementations are pure and live in lang/<code>.
type Language interface {
	// Code returns the canonical BCP-47 primary tag the language registers under
	// (e.g. "fa"). It is the key clients use via Get.
	Code() string
	// Direction reports the language's base writing direction.
	Direction() Direction
	// Normalize canonicalises text for storage and comparison. It MUST be
	// idempotent: Normalize(Normalize(x)) == Normalize(x). It performs only
	// character-level canonicalisation of equivalent representations; it never
	// inserts, deletes, or rewrites words (the verbatim invariant, CLAUDE.md).
	Normalize(string) string
	// PreserveJoinControls reports whether the caption pipeline must preserve
	// join-control characters (U+200C ZWNJ / U+200D ZWJ) when shaping and
	// breaking lines, because they are morphologically significant for this
	// language.
	PreserveJoinControls() bool
	// EngineKeys lists the engine-selection slots this language declares. Values
	// for these keys come from config rows, not code.
	EngineKeys() []EngineKey
}

// ErrUnknownLanguage is returned by Get for a tag with no registered match.
var ErrUnknownLanguage = errors.New("lang: unknown language")

var (
	mu       sync.RWMutex
	registry = map[string]Language{}
)

// Register adds a Language to the registry, keyed by the canonical form of its
// Code. It is meant to be called from a lang/<code> package init. It panics on
// an empty code or a duplicate registration — both are programming errors that
// must fail loudly at startup rather than resolve ambiguously later.
func Register(l Language) {
	code := canonTag(l.Code())
	if code == "" {
		panic("lang: Register: empty language code")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[code]; dup {
		panic("lang: Register: duplicate language code " + code)
	}
	registry[code] = l
}

// Get resolves a BCP-47 tag to its Language. It matches the canonical tag
// exactly, then falls back to the tag's primary language subtag (so "fa-IR"
// resolves to a registered "fa"). This fallback is a defined resolution, not a
// silent default: a tag with no registered primary subtag returns
// ErrUnknownLanguage.
func Get(code string) (Language, error) {
	c := canonTag(code)
	mu.RLock()
	defer mu.RUnlock()
	if l, ok := registry[c]; ok {
		return l, nil
	}
	if i := strings.IndexByte(c, '-'); i > 0 {
		if l, ok := registry[c[:i]]; ok {
			return l, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownLanguage, code)
}

// MustGet is Get for call sites where the tag is known to be registered (e.g.
// after validation at ingest). It panics on an unknown tag.
func MustGet(code string) Language {
	l, err := Get(code)
	if err != nil {
		panic(err)
	}
	return l
}

// Registered returns the sorted canonical codes currently registered. Intended
// for diagnostics and tests, not hot-path use.
func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// canonTag reduces a BCP-47 tag to the registry's lookup key: trimmed,
// underscores unified to hyphens, lowercased. Casing/separator variants of the
// same tag ("FA", "fa_IR") therefore resolve identically. This is a lookup
// canonicalisation only — it is not full BCP-47 canonicalisation.
func canonTag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "_", "-")
	return strings.ToLower(s)
}
