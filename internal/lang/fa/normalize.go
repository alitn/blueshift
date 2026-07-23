package fa

import (
	"strings"
	"unicode"
)

// This file implements the Persian text-normalization rule set. Every rule
// below is a documented convention, not an invention:
//
//   - Character folds and digit folding follow the consensus adopted by the
//     mainstream Persian NLP normalizers (hazm's `Normalizer`; parsivar's
//     `Normalizer`), which in turn reflect the letter/glyph distinctions in
//     The Unicode Standard §9.2 "Arabic" and the Arabic (U+0600..U+06FF) block
//     charts. Persian uses the Farsi forms of yeh (U+06CC) and keheh (U+06A9);
//     the Arabic yeh (U+064A) and kaf (U+0643) are common typing/ASR variants
//     that render with different medial/final shapes and must be canonicalised.
//   - Join-control handling follows Unicode cursive-joining semantics
//     (Joining_Type from ArabicShaping.txt / DerivedJoiningType.txt, consumed in
//     joining.go). ZWNJ (U+200C) is only kept where it actually suppresses a
//     cursive join, which is exactly the morphological use Persian relies on
//     (verb prefix mi-, plural -ha, comparative -tar, ...).
//   - Invisible/format-character hygiene targets: Bidi_Control marks (UAX #9);
//     the BOM/ZWNBSP (U+FEFF); and the zero-width/format artefacts U+200B (ZWSP)
//     and U+00AD (SHY). ZWSP and SHY are both General_Category=Cf (Format) in the
//     UCD (recorded as "# Cf" in DerivedJoiningType.txt) — line-break/hyphenation
//     controls that carry no textual content — and are stripped by the mainstream
//     Persian normalizers (hazm/parsivar remove zero-width/format characters).
//
// Deliberate NON-actions, to preserve the verbatim invariant (CLAUDE.md — text
// is copied from ASR, never generated) and to stay conservative:
//   - We never INSERT a ZWNJ or convert spaces to ZWNJ (that is morphological
//     generation, and hazm's optional affix-spacing rewrite is intentionally
//     omitted here).
//   - We do not fold ASCII/Latin digits to Persian digits (would corrupt URLs,
//     codes, model numbers); hazm makes that conversion optional and we opt out.
//   - We do not strip harakat/tashkil (U+064B..U+0652, U+0670): they are
//     pronunciation content, not noise, so they are preserved.
//   - We do not apply Unicode NFC here. Adding golang.org/x/text/unicode/norm as
//     a direct dependency is an ADR-level decision (Occam rule); input is
//     assumed to arrive in a consistent form from the ASR boundary. Noted as a
//     future candidate rule.

// Special code points, written as \u escapes so the invisible ones are
// unambiguous in review; the readable glyph/name is in the trailing comment.
const (
	zwnj = '\u200c' // U+200C ZERO WIDTH NON-JOINER, morphologically significant, canonicalised.
	// U+200D ZERO WIDTH JOINER is preserved untouched: it is Join_Causing and semantic.

	// Decorative elongation (kashida): Joining_Type=C, purely cosmetic, no
	// phonetic content; removed by every mainstream Persian normalizer.
	tatweel = '\u0640' // U+0640 ARABIC TATWEEL

	// Zero-width / format artefacts. All General_Category=Cf (Format) in the UCD;
	// no textual content, stripped by mainstream Persian normalizers (hazm/parsivar).
	bom  = '\ufeff' // U+FEFF BYTE ORDER MARK / ZERO WIDTH NO-BREAK SPACE (Cf)
	zwsp = '\u200b' // U+200B ZERO WIDTH SPACE (Cf; line-break control, not content)
	shy  = '\u00ad' // U+00AD SOFT HYPHEN (Cf; conditional-hyphenation control, not content)

	// Letter folds (all Dual_Joining -> Dual_Joining; see joining_test.go).
	arabicKaf   = '\u0643' // U+0643 ARABIC LETTER KAF          -> keheh
	keheh       = '\u06a9' // U+06A9 ARABIC LETTER KEHEH        (Persian kaf)
	arabicYeh   = '\u064a' // U+064A ARABIC LETTER YEH          -> farsi yeh
	alefMaksura = '\u0649' // U+0649 ARABIC LETTER ALEF MAKSURA (dotless yeh) -> farsi yeh
	farsiYeh    = '\u06cc' // U+06CC ARABIC LETTER FARSI YEH    (Persian yeh)

	// Digit block: Arabic-Indic (U+0660..U+0669) -> Extended Arabic-Indic /
	// Persian digits (U+06F0..U+06F9).
	arabicIndicZero   = '\u0660' // U+0660 ARABIC-INDIC DIGIT ZERO
	arabicIndicNine   = '\u0669' // U+0669 ARABIC-INDIC DIGIT NINE
	extArabicIndicOff = 0x06F0 - 0x0660
)

// Normalize canonicalises Persian text. It is idempotent by construction: the
// character-level passes (foldAndClean) are fixed-point after one application,
// and the join-control pass only removes cursively-ineffective ZWNJ, which
// cannot re-appear. See normalize_test.go for the corpus-wide idempotency
// property.
func Normalize(s string) string {
	if s == "" {
		return s
	}
	folded := foldAndClean(s)
	return canonicalizeZWNJ(folded)
}

// foldAndClean performs, in a single pass: invisible/format-character removal,
// tatweel removal, Arabic->Persian letter folding, and Arabic-Indic->Persian
// digit folding. ZWNJ is passed through here and handled afterwards.
func foldAndClean(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == tatweel, r == bom, r == zwsp, r == shy:
			// dropped: decorative / zero-width artefacts
			continue
		case r == zwnj:
			b.WriteRune(r) // decided later in canonicalizeZWNJ
		case unicode.Is(unicode.Bidi_Control, r):
			// LRM/RLM/ALM, embeddings, overrides, isolates (UAX #9): base
			// direction is set by the registry's Direction(), so inline bidi
			// controls are redundant artefacts in caption text.
			continue
		default:
			b.WriteRune(foldRune(r))
		}
	}
	return b.String()
}

// foldRune maps a single rune to its Persian canonical form. All folds map
// Dual_Joining -> Dual_Joining (verified against the UCD in joining_test.go), so
// they never change cursive-joining behaviour.
func foldRune(r rune) rune {
	switch r {
	case arabicKaf:
		return keheh
	case arabicYeh, alefMaksura:
		return farsiYeh
	}
	if r >= arabicIndicZero && r <= arabicIndicNine {
		return r + extArabicIndicOff
	}
	return r
}

// canonicalizeZWNJ collapses runs of consecutive ZWNJ to one and drops any ZWNJ
// that has no cursive-joining effect — i.e. one whose effective previous rune
// cannot join to the following rune, or whose effective following rune cannot
// join to the preceding one (boundaries, whitespace, and non-joining letters all
// fall here). A ZWNJ between two runes that WOULD otherwise join (e.g. the yeh
// of mi- before the verb, or beh before heh in ketab-ha) is preserved: that is
// the morphological non-joiner the verbatim caption depends on. Transparent
// marks (harakat) are skipped when locating a ZWNJ's neighbours, matching
// Unicode joining rules.
func canonicalizeZWNJ(s string) string {
	if !strings.ContainsRune(s, zwnj) {
		return s
	}
	runes := []rune(s)

	// Collapse consecutive ZWNJ to a single ZWNJ.
	collapsed := make([]rune, 0, len(runes))
	for _, r := range runes {
		if r == zwnj && len(collapsed) > 0 && collapsed[len(collapsed)-1] == zwnj {
			continue
		}
		collapsed = append(collapsed, r)
	}

	var b strings.Builder
	b.Grow(len(s))
	for i, r := range collapsed {
		if r != zwnj {
			b.WriteRune(r)
			continue
		}
		p := i - 1
		for p >= 0 && isTransparent(collapsed[p]) {
			p--
		}
		q := i + 1
		for q < len(collapsed) && isTransparent(collapsed[q]) {
			q++
		}
		prevJoins := p >= 0 && joinsToNext(collapsed[p])
		nextJoins := q < len(collapsed) && joinsToPrev(collapsed[q])
		if prevJoins && nextJoins {
			b.WriteRune(zwnj)
		}
		// else: cursively ineffective ZWNJ — dropped.
	}
	return b.String()
}
