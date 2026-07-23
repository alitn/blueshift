package fa

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// normCase is one table-driven normalization expectation. `why` cites the rule.
// in/want are written as \u escapes so the exact code points are unambiguous.
type normCase struct {
	name string
	in   string
	want string
	why  string
}

// cases exercises every documented rule (see normalize.go). Each `why` names the
// Unicode / Persian-normalization basis for the expectation.
var cases = []normCase{
	{name: "kaf-arabic-to-keheh", in: "\u0643\u062a\u0627\u0628", want: "\u06a9\u062a\u0627\u0628", why: "U+0643 ARABIC KAF -> U+06A9 KEHEH; hazm/parsivar"},
	{name: "yeh-arabic-to-farsi", in: "\u0639\u0644\u064a", want: "\u0639\u0644\u06cc", why: "U+064A ARABIC YEH -> U+06CC FARSI YEH; hazm/parsivar"},
	{name: "alef-maksura-to-farsi-yeh", in: "\u0645\u0648\u0633\u0649", want: "\u0645\u0648\u0633\u06cc", why: "U+0649 ALEF MAKSURA -> U+06CC FARSI YEH; hazm/parsivar"},
	{name: "arabic-indic-digits-to-persian", in: "\u0664\u0665\u0666", want: "\u06f4\u06f5\u06f6", why: "Arabic-Indic U+0664..0666 -> Persian U+06F4..06F6"},
	{name: "persian-digits-unchanged", in: "\u06f1\u06f2\u06f3", want: "\u06f1\u06f2\u06f3", why: "Extended Arabic-Indic already canonical"},
	{name: "ascii-digits-preserved", in: "2024", want: "2024", why: "Latin digits deliberately NOT folded (codes/URLs)"},
	{name: "tatweel-removed", in: "\u0643\u0640\u0640\u062a\u0627\u0628", want: "\u06a9\u062a\u0627\u0628", why: "U+0640 TATWEEL removed; also folds kaf"},
	{name: "zwnj-preserved-mi-prefix", in: "\u0645\u06cc\u200c\u0631\u0648\u0645", want: "\u0645\u06cc\u200c\u0631\u0648\u0645", why: "ZWNJ between farsi-yeh(D) and reh(R) -> kept (mi-)"},
	{name: "zwnj-preserved-ha-suffix", in: "\u06a9\u062a\u0627\u0628\u200c\u0647\u0627", want: "\u06a9\u062a\u0627\u0628\u200c\u0647\u0627", why: "ZWNJ between beh(D) and heh(D) -> kept (-ha)"},
	{name: "zwnj-collapsed-then-kept", in: "\u0645\u06cc\u200c\u200c\u0631\u0648\u0645", want: "\u0645\u06cc\u200c\u0631\u0648\u0645", why: "consecutive ZWNJ collapse to one; stays meaningful"},
	{name: "zwnj-dropped-after-nonjoining-alef", in: "\u0628\u0627\u200c\u0645\u0646", want: "\u0628\u0627\u0645\u0646", why: "alef Right_Joining -> ZWNJ ineffective, dropped"},
	{name: "zwnj-dropped-at-boundaries", in: "\u200c\u0633\u0644\u0627\u0645\u200c", want: "\u0633\u0644\u0627\u0645", why: "leading/trailing ZWNJ -> dropped"},
	{name: "zwnj-dropped-before-space", in: "\u06a9\u062a\u0627\u0628\u200c\u0020\u0647\u0627", want: "\u06a9\u062a\u0627\u0628\u0020\u0647\u0627", why: "space Non_Joining -> ZWNJ dropped"},
	{name: "bom-removed", in: "\ufeff\u0633\u0644\u0627\u0645", want: "\u0633\u0644\u0627\u0645", why: "U+FEFF BOM/ZWNBSP stripped"},
	{name: "bidi-marks-removed", in: "\u0633\u0644\u0627\u0645\u200f\u200e", want: "\u0633\u0644\u0627\u0645", why: "Bidi_Control stripped (UAX #9)"},
	{name: "zwsp-shy-removed", in: "\u0633\u200b\u0644\u00ad\u0627\u0645", want: "\u0633\u0644\u0627\u0645", why: "U+200B ZWSP and U+00AD SHY stripped"},
	{name: "harakat-preserved", in: "\u0628\u0650\u0633\u0644\u0627\u0645", want: "\u0628\u0650\u0633\u0644\u0627\u0645", why: "harakat U+0650 is content -> preserved"},
	{name: "zwnj-kept-across-transparent", in: "\u0645\u06cc\u0650\u200c\u0631\u0648\u0645", want: "\u0645\u06cc\u0650\u200c\u0631\u0648\u0645", why: "harakat skipped; yeh(D)/reh(R) join -> ZWNJ kept"},
	{name: "zwj-preserved", in: "\u0628\u200d\u0645", want: "\u0628\u200d\u0645", why: "U+200D ZWJ semantic -> untouched"},
	{name: "latin-unchanged", in: "Hello 2024", want: "Hello 2024", why: "non-Persian text passes through"},
	{name: "empty", in: "", want: "", why: "empty string"},
}

func TestNormalize(t *testing.T) {
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("%s: Normalize(%q) = %q, want %q (%s)", c.name, c.in, got, c.want, c.why)
		}
	}
}

// TestNormalizeIdempotent is the property test required by the spec:
// Normalize(Normalize(x)) == Normalize(x) over the whole fixture corpus (every
// input AND every expected output) plus adversarial seeds. Verbatim-caption
// determinism depends on this.
func TestNormalizeIdempotent(t *testing.T) {
	corpus := idempotencyCorpus()
	for _, x := range corpus {
		once := Normalize(x)
		twice := Normalize(once)
		if once != twice {
			t.Errorf("not idempotent for %q: once=%q twice=%q", x, once, twice)
		}
	}
}

// idempotencyCorpus is the union of every table input/output and a set of
// adversarial strings that stress the join-control and hygiene passes.
func idempotencyCorpus() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, c := range cases {
		add(c.in)
		add(c.want)
	}
	for _, s := range fuzzSeeds() {
		add(s)
	}
	return out
}

// fuzzSeeds are adversarial inputs reused by the corpus and the fuzz target.
func fuzzSeeds() []string {
	return []string{
		"",                                     // empty
		"\u200c",                               // lone ZWNJ
		"\u200c\u200c\u200c",                   // ZWNJ run only
		"\u0628\u200c\u200c\u200c\u0645",       // beh + ZWNJ run + meem
		"\u0627\u200c\u0627",                   // alef ZWNJ alef (both Right_Joining)
		"\u0645\u06cc\u200c\u0020\u200c\u0631", // ZWNJ, space, ZWNJ, reh
		"\ufeff\ufeff\u0633",                   // repeated BOM
		"\u0640\u0640\u0640",                   // tatweel only
		"\u0643\u064a\u0649\u0664",             // kaf yeh maksura arabic-4 (all folded)
		"\u0628\u0650\u200c\u0645",             // beh + harakat + ZWNJ + meem
		"\u200d\u200c\u200d",                   // ZWJ ZWNJ ZWJ
		"sal\u00e1m \u0645\u06cc\u200c\u0631\u0648\u0645 123", // mixed scripts + digits
	}
}

// TestNormalizeInvariants asserts post-conditions that must hold for ANY input:
// the output is valid UTF-8 and contains none of the artefacts the rules remove.
func TestNormalizeInvariants(t *testing.T) {
	for _, x := range idempotencyCorpus() {
		got := Normalize(x)
		if !utf8.ValidString(got) {
			t.Errorf("Normalize(%q) produced invalid UTF-8", x)
		}
		for _, bad := range []rune{arabicKaf, arabicYeh, alefMaksura, tatweel, bom, zwsp, shy} {
			if strings.ContainsRune(got, bad) {
				t.Errorf("Normalize(%q) = %q still contains U+%04X", x, got, bad)
			}
		}
		for r := arabicIndicZero; r <= arabicIndicNine; r++ {
			if strings.ContainsRune(got, r) {
				t.Errorf("Normalize(%q) = %q still contains Arabic-Indic digit U+%04X", x, got, r)
			}
		}
	}
}

// FuzzNormalize: Normalize must never panic, must always return valid UTF-8, and
// must be idempotent on arbitrary input.
func FuzzNormalize(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	for _, c := range cases {
		f.Add(c.in)
	}
	f.Fuzz(func(t *testing.T, s string) {
		once := Normalize(s)
		if !utf8.ValidString(once) {
			// Invalid input can yield replacement runes; only require validity
			// when the input was valid UTF-8.
			if utf8.ValidString(s) {
				t.Fatalf("valid input %q normalized to invalid UTF-8 %q", s, once)
			}
		}
		if twice := Normalize(once); once != twice {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, once, twice)
		}
	})
}
