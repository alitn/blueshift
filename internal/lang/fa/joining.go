package fa

import (
	_ "embed"
	"strconv"
	"strings"
	"unicode"
)

// The cursive-joining behaviour used by ZWNJ canonicalisation is taken verbatim
// from the Unicode Character Database, not invented here. We embed the
// authoritative derived file and parse it at startup, so the classification is
// exactly Unicode's and is auditable in-repo.
//
// Source: Unicode UCD 17.0.0, extracted/DerivedJoiningType.txt (Joining_Type
// property; property definition in ArabicShaping.txt and The Unicode Standard,
// §9.2 "Arabic", "Cursive Joining"). Code points not listed there have
// Joining_Type = Non_Joining (U), per that file's @missing line.
//
//go:embed ucd/DerivedJoiningType.txt
var derivedJoiningType string

// Joining_Type range tables, one per non-U type, parsed from the embedded UCD
// file. Non-listed runes are Non_Joining (U) and appear in none of these.
var (
	jtDual  *unicode.RangeTable // D  Dual_Joining   — joins on both sides
	jtRight *unicode.RangeTable // R  Right_Joining  — joins the previous rune only
	jtLeft  *unicode.RangeTable // L  Left_Joining   — joins the following rune only
	jtCause *unicode.RangeTable // C  Join_Causing   — forces joining (ZWJ, tatweel)
	jtTrans *unicode.RangeTable // T  Transparent    — combining/format marks
)

func init() {
	tables := parseDerivedJoiningType(derivedJoiningType)
	jtDual = tables['D']
	jtRight = tables['R']
	jtLeft = tables['L']
	jtCause = tables['C']
	jtTrans = tables['T']
}

// joinsToNext reports whether r can cursively connect to the logically
// following rune. In right-to-left Arabic script the following rune sits to the
// visual left, so this is true for Dual_Joining, Left_Joining, and Join_Causing.
func joinsToNext(r rune) bool {
	return unicode.Is(jtDual, r) || unicode.Is(jtLeft, r) || unicode.Is(jtCause, r)
}

// joinsToPrev reports whether r can cursively connect to the logically
// preceding rune (visually to its right), i.e. Dual_Joining, Right_Joining, or
// Join_Causing.
func joinsToPrev(r rune) bool {
	return unicode.Is(jtDual, r) || unicode.Is(jtRight, r) || unicode.Is(jtCause, r)
}

// isTransparent reports whether r is transparent to cursive joining (combining
// marks such as the Arabic harakat, and format marks). Such runes are skipped
// when locating a ZWNJ's effective neighbours.
func isTransparent(r rune) bool { return unicode.Is(jtTrans, r) }

// parseDerivedJoiningType parses the UCD DerivedJoiningType format:
//
//	CODE          ; T # comment
//	CODE..CODE    ; T # comment
//
// building one RangeTable per Joining_Type letter present. It panics on a
// malformed line; the input is committed, embedded data validated by tests, so a
// panic here means the data file was corrupted.
func parseDerivedJoiningType(data string) map[byte]*unicode.RangeTable {
	type rng struct{ lo, hi rune }
	byType := map[byte][]rng{}

	for _, line := range strings.Split(data, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		semi := strings.IndexByte(line, ';')
		if semi < 0 {
			panic("lang/fa: malformed DerivedJoiningType line: " + line)
		}
		codes := strings.TrimSpace(line[:semi])
		typ := strings.TrimSpace(line[semi+1:])
		if len(typ) != 1 {
			panic("lang/fa: unexpected Joining_Type value: " + typ)
		}
		lo, hi := parseCodeRange(codes)
		byType[typ[0]] = append(byType[typ[0]], rng{lo, hi})
	}

	out := map[byte]*unicode.RangeTable{}
	for t := range byType {
		rt := &unicode.RangeTable{}
		for _, r := range byType[t] {
			appendRange(rt, r.lo, r.hi)
		}
		setLatinOffset(rt)
		out[t] = rt
	}
	// Guarantee every table is non-nil so unicode.Is never dereferences nil, even
	// if a future data file drops a whole Joining_Type.
	for _, t := range []byte{'D', 'R', 'L', 'C', 'T'} {
		if out[t] == nil {
			out[t] = &unicode.RangeTable{}
		}
	}
	return out
}

// parseCodeRange parses "0640" or "0622..0625" into an inclusive rune range.
func parseCodeRange(s string) (rune, rune) {
	if lo, hi, ok := strings.Cut(s, ".."); ok {
		return parseHexRune(lo), parseHexRune(hi)
	}
	r := parseHexRune(s)
	return r, r
}

func parseHexRune(s string) rune {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 16, 32)
	if err != nil {
		panic("lang/fa: bad code point in DerivedJoiningType: " + s)
	}
	return rune(v)
}

// appendRange adds an inclusive [lo,hi] range to rt, splitting at the 16-bit
// boundary between R16 and R32 as unicode.RangeTable requires. The UCD file is
// sorted ascending, so the resulting slices stay sorted (a precondition of
// unicode.Is).
func appendRange(rt *unicode.RangeTable, lo, hi rune) {
	if lo <= 0xFFFF {
		h := hi
		if h > 0xFFFF {
			h = 0xFFFF
		}
		rt.R16 = append(rt.R16, unicode.Range16{Lo: uint16(lo), Hi: uint16(h), Stride: 1})
	}
	if hi > 0xFFFF {
		l := lo
		if l < 0x10000 {
			l = 0x10000
		}
		rt.R32 = append(rt.R32, unicode.Range32{Lo: uint32(l), Hi: uint32(hi), Stride: 1})
	}
}

// setLatinOffset records how many leading R16 entries fall entirely within
// Latin-1, the small optimisation unicode.Is uses.
func setLatinOffset(rt *unicode.RangeTable) {
	for _, r16 := range rt.R16 {
		if r16.Hi > unicode.MaxLatin1 {
			break
		}
		rt.LatinOffset++
	}
}
