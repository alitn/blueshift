package fa

import (
	"testing"
	"unicode"
)

// TestJoiningClassification checks the parsed Joining_Type tables against the
// documented Unicode values for the runes that matter to Persian shaping. These
// expectations come straight from UCD 17.0.0 DerivedJoiningType.txt /
// ArabicShaping.txt (§9.2 "Arabic"); the test guards the embed + parser so a
// bad data file or parser change is caught, and documents "not invented".
func TestJoiningClassification(t *testing.T) {
	cases := []struct {
		r                  rune
		name               string
		wantPrev, wantNext bool // joinsToPrev / joinsToNext
		wantTransparent    bool
	}{
		// Dual_Joining letters: join on both sides.
		{0x0628, "BEH", true, true, false},
		{0x0633, "SEEN", true, true, false},
		{0x0647, "HEH", true, true, false},
		{0x0645, "MEEM", true, true, false},
		// Right_Joining letters: join only to the previous rune.
		{0x0627, "ALEF", true, false, false},
		{0x062F, "DAL", true, false, false},
		{0x0631, "REH", true, false, false},
		{0x0648, "WAW", true, false, false},
		{0x0629, "TEH MARBUTA", true, false, false},
		// Join_Causing: joins both sides.
		{0x0640, "TATWEEL", true, true, false},
		{0x200D, "ZWJ", true, true, false},
		// Non_Joining: joins neither; ZWNJ itself is U.
		{0x200C, "ZWNJ", false, false, false},
		{0x0020, "SPACE", false, false, false},
		{0x0041, "LATIN A", false, false, false},
		{0x06F0, "PERSIAN DIGIT ZERO", false, false, false},
		// Transparent marks (harakat): skipped when locating ZWNJ neighbours.
		{0x064B, "FATHATAN", false, false, true},
		{0x0650, "KASRA", false, false, true},
		{0x0670, "SUPERSCRIPT ALEF", false, false, true},
	}
	for _, c := range cases {
		if got := joinsToPrev(c.r); got != c.wantPrev {
			t.Errorf("joinsToPrev(U+%04X %s) = %v, want %v", c.r, c.name, got, c.wantPrev)
		}
		if got := joinsToNext(c.r); got != c.wantNext {
			t.Errorf("joinsToNext(U+%04X %s) = %v, want %v", c.r, c.name, got, c.wantNext)
		}
		if got := isTransparent(c.r); got != c.wantTransparent {
			t.Errorf("isTransparent(U+%04X %s) = %v, want %v", c.r, c.name, got, c.wantTransparent)
		}
	}
}

// TestFoldsPreserveJoiningType asserts every character fold maps a Dual_Joining
// letter to another Dual_Joining letter. This is the invariant that lets the
// fold pass run before the ZWNJ pass without changing any join decision, which
// underpins idempotency (normalize.go, foldRune).
func TestFoldsPreserveJoiningType(t *testing.T) {
	for _, r := range []rune{arabicKaf, keheh, arabicYeh, alefMaksura, farsiYeh} {
		if !unicode.Is(jtDual, r) {
			t.Errorf("U+%04X expected Dual_Joining but is not in jtDual", r)
		}
	}
}

// TestParserCoverage sanity-checks that the embedded file parsed into non-empty
// tables (guards against an empty/garbled embed silently disabling the rules).
func TestParserCoverage(t *testing.T) {
	tables := map[string]*unicode.RangeTable{
		"D": jtDual, "R": jtRight, "L": jtLeft, "C": jtCause, "T": jtTrans,
	}
	for name, rt := range tables {
		if rt == nil {
			t.Fatalf("joining table %s is nil", name)
		}
		if len(rt.R16)+len(rt.R32) == 0 {
			t.Errorf("joining table %s parsed empty", name)
		}
	}
}
