package asr

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

// testFake builds a FakeEngine over the committed testdata fixtures.
func testFake(t *testing.T) *FakeEngine {
	t.Helper()
	f, err := NewFakeEngine("bs-asr-1", os.DirFS("testdata"))
	if err != nil {
		t.Fatalf("NewFakeEngine: %v", err)
	}
	return f
}

func TestFakeResolvesByExactKey(t *testing.T) {
	f := testFake(t)
	tr, err := f.Transcribe(context.Background(), TranscribeRequest{
		AudioKey: "org_demo/ep_demo/proxies/audio.wav",
		Language: "en", // deliberately not fa: the exact key must win over language
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if len(tr.Segments) == 0 {
		t.Fatal("exact-key resolution returned an empty transcript")
	}
}

func TestFakeResolvesByLanguage(t *testing.T) {
	f := testFake(t)
	for _, tag := range []string{"fa", "fa-IR", "FA", "fa_AF"} {
		tr, err := f.Transcribe(context.Background(), TranscribeRequest{
			AudioKey: "unmapped/key/audio.wav",
			Language: tag,
		})
		if err != nil {
			t.Fatalf("Transcribe(lang=%q): %v", tag, err)
		}
		if tr.Language != tag {
			t.Errorf("Transcript.Language = %q, want echo of request %q", tr.Language, tag)
		}
		if tr.Engine != "bs-asr-1" {
			t.Errorf("Transcript.Engine = %q, want bs-asr-1", tr.Engine)
		}
		if len(tr.Segments) != 2 {
			t.Fatalf("segments = %d, want 2", len(tr.Segments))
		}
	}
}

func TestFakeNoFixtureIsExplicitError(t *testing.T) {
	f := testFake(t)
	_, err := f.Transcribe(context.Background(), TranscribeRequest{
		AudioKey: "unmapped/key/audio.wav",
		Language: "en",
	})
	if err == nil {
		t.Fatal("want error for unmatched request")
	}
	assertNoLeak(t, "no-fixture error", err.Error())
}

func TestFakeContextCancelled(t *testing.T) {
	f := testFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Transcribe(ctx, TranscribeRequest{Language: "fa"}); err == nil {
		t.Fatal("want error on cancelled context")
	}
}

func TestFakeIsDeterministic(t *testing.T) {
	f := testFake(t)
	req := TranscribeRequest{Language: "fa"}
	a, err := f.Transcribe(context.Background(), req)
	if err != nil {
		t.Fatalf("Transcribe a: %v", err)
	}
	// Bias terms and options must not change the deterministic output.
	b, err := f.Transcribe(context.Background(), TranscribeRequest{
		Language:  "fa",
		BiasTerms: []string{"foo", "bar"},
		Options:   map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatalf("Transcribe b: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("output not deterministic across bias/options:\n a=%+v\n b=%+v", a, b)
	}
}

func TestFakeReturnsIndependentCopies(t *testing.T) {
	f := testFake(t)
	req := TranscribeRequest{Language: "fa"}
	first, err := f.Transcribe(context.Background(), req)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	// Mutate the returned transcript; a subsequent call must be unaffected, proving
	// the fake hands back a deep copy and never leaks its cached fixture.
	first.Segments[0].Text = "MUTATED"
	first.Segments[0].Words[0].Text = "MUTATED"
	first.Segments[0].StartMs = -999
	if len(first.Raw) > 0 {
		first.Raw[0] = 'X'
	}

	second, err := f.Transcribe(context.Background(), req)
	if err != nil {
		t.Fatalf("Transcribe again: %v", err)
	}
	if second.Segments[0].Text == "MUTATED" || second.Segments[0].Words[0].Text == "MUTATED" || second.Segments[0].StartMs == -999 {
		t.Fatal("mutating a returned transcript affected a later call: fixture is shared, not copied")
	}
}

func TestFakeOutputValidates(t *testing.T) {
	f := testFake(t)
	tr, err := f.Transcribe(context.Background(), TranscribeRequest{Language: "fa"})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if err := tr.Validate(); err != nil {
		t.Fatalf("fake output failed Validate: %v", err)
	}
}

// TestFakePersianFixture checks the committed Persian recording against its
// hand-computed values: RTL text, ms-int timings, and a preserved ZWNJ.
func TestFakePersianFixture(t *testing.T) {
	f := testFake(t)
	tr, err := f.Transcribe(context.Background(), TranscribeRequest{Language: "fa"})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if len(tr.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(tr.Segments))
	}
	// Spot-check the first word's exact hand-checked timing.
	first := tr.Segments[0].Words[0]
	if first.Text != "سلام" || first.StartMs != 0 || first.EndMs != 520 {
		t.Errorf("first word = %+v, want {سلام 0 520}", first)
	}
	// The guest reply's second word carries a ZWNJ that must survive verbatim.
	var zwnjSeen bool
	for _, w := range tr.Segments[1].Words {
		if strings.ContainsRune(w.Text, '‌') {
			zwnjSeen = true
		}
	}
	if !zwnjSeen {
		t.Error("ZWNJ (U+200C) not preserved in guest reply words")
	}
	// Every word must be real Persian (Arabic-script range), not placeholder text.
	for _, s := range tr.Segments {
		for _, w := range s.Words {
			if !hasArabicScript(w.Text) {
				t.Errorf("word %q is not Persian script", w.Text)
			}
		}
	}
}

func TestNewFakeEngineRejectsBadConfig(t *testing.T) {
	// Empty label.
	if _, err := NewFakeEngine("", os.DirFS("testdata")); err == nil {
		t.Error("NewFakeEngine with empty label: want error")
	}
	// A directory with no fixtures.
	empty := t.TempDir()
	if _, err := NewFakeEngine("bs-asr-1", os.DirFS(empty)); err == nil {
		t.Error("NewFakeEngine over empty dir: want error")
	}
}

// hasArabicScript reports whether s contains at least one character in the Arabic
// Unicode block (U+0600..U+06FF), i.e. it is real Persian script rather than a
// Latin placeholder.
func hasArabicScript(s string) bool {
	for _, r := range s {
		if r >= 0x0600 && r <= 0x06FF {
			return true
		}
	}
	return false
}
