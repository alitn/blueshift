package lang

import (
	"errors"
	"testing"
)

// fakeLang is a minimal Language used to exercise the registry without pulling
// in any real lang/<code> package (which would create an import cycle: the real
// packages import lang). It registers under a private-use subtag so it can never
// collide with a real language.
type fakeLang struct {
	code string
	dir  Direction
}

func (f fakeLang) Code() string               { return f.code }
func (f fakeLang) Direction() Direction       { return f.dir }
func (f fakeLang) Normalize(s string) string  { return s }
func (f fakeLang) PreserveJoinControls() bool { return false }
func (f fakeLang) EngineKeys() []EngineKey    { return []EngineKey{EngineASR} }

func TestGetUnknownIsExplicitError(t *testing.T) {
	for _, code := range []string{"zz", "klingon", "", "  ", "xx-YY"} {
		if _, err := Get(code); !errors.Is(err, ErrUnknownLanguage) {
			t.Errorf("Get(%q) err = %v, want ErrUnknownLanguage", code, err)
		}
	}
}

func TestRegisterAndGet(t *testing.T) {
	Register(fakeLang{code: "qaa", dir: RTL})

	got, err := Get("qaa")
	if err != nil {
		t.Fatalf("Get(qaa): %v", err)
	}
	if got.Code() != "qaa" || got.Direction() != RTL {
		t.Fatalf("Get(qaa) = %+v", got)
	}

	// Casing and separator variants resolve to the same registration.
	for _, variant := range []string{"QAA", " qaa ", "qaa"} {
		if _, err := Get(variant); err != nil {
			t.Errorf("Get(%q): %v", variant, err)
		}
	}
}

func TestGetPrimarySubtagFallback(t *testing.T) {
	Register(fakeLang{code: "qab", dir: LTR})

	// Regional/extended tags resolve to the registered primary subtag.
	for _, tag := range []string{"qab-XA", "qab_xb", "QAB-Latn-XA"} {
		l, err := Get(tag)
		if err != nil {
			t.Fatalf("Get(%q): %v", tag, err)
		}
		if l.Code() != "qab" {
			t.Fatalf("Get(%q) = %q, want qab", tag, l.Code())
		}
	}

	// A tag whose primary subtag is not registered is still unknown.
	if _, err := Get("qac-XA"); !errors.Is(err, ErrUnknownLanguage) {
		t.Errorf("Get(qac-XA) err = %v, want ErrUnknownLanguage", err)
	}
}

func TestMustGetPanicsOnUnknown(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustGet(unknown) did not panic")
		}
	}()
	MustGet("nope")
}

func TestRegisterEmptyCodePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register(empty code) did not panic")
		}
	}()
	Register(fakeLang{code: "   "})
}

func TestRegisterDuplicatePanics(t *testing.T) {
	Register(fakeLang{code: "qad"})
	defer func() {
		if recover() == nil {
			t.Error("duplicate Register did not panic")
		}
	}()
	Register(fakeLang{code: "QAD"}) // canonicalises to the same key
}

func TestRegisteredIsSorted(t *testing.T) {
	Register(fakeLang{code: "qaz"})
	Register(fakeLang{code: "qay"})
	got := Registered()
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("Registered() not sorted: %v", got)
		}
	}
}
