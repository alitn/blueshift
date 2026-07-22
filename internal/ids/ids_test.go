package ids

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
)

// allPrefixes is the full registered set, asserted against the registry so a
// new prefix cannot be added without updating the tests.
var allPrefixes = []Prefix{Episode, Show, Moment, Clip, Speaker}

func TestRegistryMatchesConstants(t *testing.T) {
	if len(registry) != len(allPrefixes) {
		t.Fatalf("registry has %d entries, test knows %d", len(registry), len(allPrefixes))
	}
	for _, p := range allPrefixes {
		if !Valid(p) {
			t.Errorf("prefix %q not registered", p)
		}
	}
}

func TestEncodeShape(t *testing.T) {
	var b [16]byte
	for _, p := range allPrefixes {
		s := Encode(p, b)
		wantPrefix := string(p) + "_"
		if !strings.HasPrefix(s, wantPrefix) {
			t.Errorf("Encode(%q) = %q, missing prefix %q", p, s, wantPrefix)
		}
		body := strings.TrimPrefix(s, wantPrefix)
		if len(body) != bodyLen {
			t.Errorf("Encode(%q) body len = %d, want %d", p, len(body), bodyLen)
		}
		if body != strings.ToLower(body) {
			t.Errorf("Encode(%q) body not lowercase: %q", p, body)
		}
	}
}

// TestRoundTripAllPrefixes round-trips every prefix over random and edge UUIDs.
func TestRoundTripAllPrefixes(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var edge [][16]byte
	var zero, max [16]byte
	for i := range max {
		max[i] = 0xff
	}
	edge = append(edge, zero, max)
	for i := 0; i < 1000; i++ {
		var b [16]byte
		rng.Read(b[:])
		edge = append(edge, b)
	}

	for _, p := range allPrefixes {
		for _, b := range edge {
			s := Encode(p, b)
			got, err := Decode(p, s)
			if err != nil {
				t.Fatalf("Decode(%q, %q): %v", p, s, err)
			}
			if got != b {
				t.Fatalf("round-trip mismatch for %q: got %x want %x", p, got, b)
			}
			// Parse (prefix-inferring) must agree.
			gp, gb, err := Parse(s)
			if err != nil {
				t.Fatalf("Parse(%q): %v", s, err)
			}
			if gp != p || gb != b {
				t.Fatalf("Parse(%q) = (%q,%x), want (%q,%x)", s, gp, gb, p, b)
			}
		}
	}
}

func TestZeroAndMaxCanonical(t *testing.T) {
	var zero, max [16]byte
	for i := range max {
		max[i] = 0xff
	}
	if got := Encode(Episode, zero); got != "ep_"+strings.Repeat("0", bodyLen) {
		t.Errorf("zero encode = %q", got)
	}
	// Max must round-trip and the encoding must be canonical (decodes clean).
	if _, err := Decode(Episode, Encode(Episode, max)); err != nil {
		t.Errorf("max decode: %v", err)
	}
}

func TestWrongPrefixRejected(t *testing.T) {
	var b [16]byte
	rand.New(rand.NewSource(2)).Read(b[:])
	s := Encode(Show, b) // sh_...
	if _, err := Decode(Episode, s); !errors.Is(err, ErrWrongPrefix) {
		t.Errorf("Decode(Episode, %q) err = %v, want ErrWrongPrefix", s, err)
	}
}

func TestUnknownPrefixRejected(t *testing.T) {
	var b [16]byte
	if _, err := Decode(Prefix("xyz"), "xyz_"+strings.Repeat("0", bodyLen)); !errors.Is(err, ErrUnknownPrefix) {
		t.Errorf("Decode(unknown) err = %v, want ErrUnknownPrefix", err)
	}
	if _, _, err := Parse("xyz_" + strings.Repeat("0", bodyLen)); !errors.Is(err, ErrUnknownPrefix) {
		t.Errorf("Parse(unknown) err = %v, want ErrUnknownPrefix", err)
	}
	_ = b
}

func TestNoSeparatorRejected(t *testing.T) {
	for _, s := range []string{"", "ep", strings.Repeat("0", bodyLen), "_abc"} {
		if _, _, err := Parse(s); !errors.Is(err, ErrNoSeparator) && !errors.Is(err, ErrUnknownPrefix) {
			t.Errorf("Parse(%q) err = %v, want ErrNoSeparator/ErrUnknownPrefix", s, err)
		}
	}
	// Explicitly: no underscore at all -> ErrNoSeparator.
	if _, _, err := Parse("ep"); !errors.Is(err, ErrNoSeparator) {
		t.Errorf("Parse(\"ep\") err = %v, want ErrNoSeparator", err)
	}
}

func TestBadLengthRejected(t *testing.T) {
	cases := []string{
		"",
		strings.Repeat("0", bodyLen-1),
		strings.Repeat("0", bodyLen+1),
		strings.Repeat("0", 32),
	}
	for _, body := range cases {
		if _, err := Decode(Episode, "ep_"+body); !errors.Is(err, ErrBadLength) {
			t.Errorf("Decode body len %d err = %v, want ErrBadLength", len(body), err)
		}
	}
}

func TestBadCharRejected(t *testing.T) {
	// u is excluded from Crockford; punctuation and space are invalid too.
	for _, bad := range []byte{'u', 'U', '!', ' ', '-', '/', '\x00'} {
		body := string(bad) + strings.Repeat("0", bodyLen-1)
		if _, err := Decode(Episode, "ep_"+body); !errors.Is(err, ErrBadChar) {
			t.Errorf("Decode with %q err = %v, want ErrBadChar", bad, err)
		}
	}
}

func TestNonCanonicalRejected(t *testing.T) {
	// The last symbol carries only the top 3 of its 5 bits; the low 2 bits are
	// padding and must be zero. A zero body with a last symbol whose low bits
	// are set is non-canonical. Symbol '1' = 00001 has a low bit set.
	body := strings.Repeat("0", bodyLen-1) + "1"
	if _, err := Decode(Episode, "ep_"+body); !errors.Is(err, ErrNonCanonical) {
		t.Errorf("Decode non-canonical err = %v, want ErrNonCanonical", err)
	}
}

// TestCaseInsensitiveDecode: uppercase and Crockford ambiguity folds decode to
// the same bytes as the canonical lowercase form.
func TestCaseInsensitiveDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 200; i++ {
		var b [16]byte
		rng.Read(b[:])
		s := Encode(Episode, b)
		body := strings.TrimPrefix(s, "ep_")

		upper, err := Decode(Episode, "ep_"+strings.ToUpper(body))
		if err != nil {
			t.Fatalf("uppercase decode %q: %v", body, err)
		}
		if upper != b {
			t.Fatalf("uppercase decode mismatch: got %x want %x", upper, b)
		}
	}

	// Ambiguity folds: O->0, I->1, L->1. Build a body of zeros (all '0') and
	// swap the leading symbol for its ambiguous spellings.
	base := strings.Repeat("0", bodyLen)
	oForm := "O" + base[1:] // O folds to 0 -> identical to all-zero body
	got, err := Decode(Episode, "ep_"+oForm)
	if err != nil {
		t.Fatalf("O-fold decode: %v", err)
	}
	var zero [16]byte
	if got != zero {
		t.Fatalf("O-fold decode = %x, want zero", got)
	}

	// I and L fold to 1; place them where the padding stays canonical: as a
	// symbol contributing value 1 in a non-terminal position must equal '1'.
	iBody := "i" + base[1:]
	lBody := "l" + base[1:]
	oneBody := "1" + base[1:]
	gi, err1 := Decode(Episode, "ep_"+iBody)
	gl, err2 := Decode(Episode, "ep_"+lBody)
	g1, err3 := Decode(Episode, "ep_"+oneBody)
	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatalf("fold decode errors: %v %v %v", err1, err2, err3)
	}
	if gi != g1 || gl != g1 {
		t.Fatalf("i/l did not fold to 1: gi=%x gl=%x g1=%x", gi, gl, g1)
	}
}

func TestValid(t *testing.T) {
	if Valid(Prefix("nope")) {
		t.Error("Valid(nope) = true")
	}
	if !Valid(Clip) {
		t.Error("Valid(Clip) = false")
	}
}

// FuzzDecode: Decode must never panic on arbitrary input, and whenever it
// succeeds the value's canonical encoding must be a fixed point (re-decoding it
// yields the same bytes, re-encoding those bytes yields the same string). We do
// not require equality with the input: Crockford folds (o->0, i/l->1) and case
// mean several inputs legitimately map to one canonical form.
func FuzzDecode(f *testing.F) {
	f.Add("ep_" + strings.Repeat("0", bodyLen))
	f.Add("sh_" + strings.Repeat("z", bodyLen))
	f.Add("clip_")
	f.Add("")
	f.Add("ep_UUUU")
	f.Add("not an id")
	f.Add("ep_0000000000L000000000000000")
	f.Fuzz(func(t *testing.T, s string) {
		b, err := Decode(Episode, s)
		if err != nil {
			return
		}
		canon := Encode(Episode, b)
		b2, err := Decode(Episode, canon)
		if err != nil {
			t.Fatalf("canonical form %q failed to decode: %v", canon, err)
		}
		if b2 != b {
			t.Fatalf("canonical re-decode changed bytes: %x -> %x", b, b2)
		}
		if got := Encode(Episode, b2); got != canon {
			t.Fatalf("canonical form not stable: %q -> %q", canon, got)
		}
	})
}

// FuzzParse: Parse must never panic, and on success the decoded bytes re-encode
// under the parsed prefix to a canonical, stable form.
func FuzzParse(f *testing.F) {
	f.Add("mo_" + strings.Repeat("0", bodyLen))
	f.Add("sp_" + strings.Repeat("v", bodyLen))
	f.Add("_")
	f.Add("xyz_abc")
	f.Add("sp_000000000000000000O0000000")
	f.Fuzz(func(t *testing.T, s string) {
		p, b, err := Parse(s)
		if err != nil {
			return
		}
		canon := Encode(p, b)
		p2, b2, err := Parse(canon)
		if err != nil {
			t.Fatalf("canonical form %q failed to parse: %v", canon, err)
		}
		if p2 != p || b2 != b {
			t.Fatalf("canonical re-parse changed: (%q,%x) -> (%q,%x)", p, b, p2, b2)
		}
	})
}
