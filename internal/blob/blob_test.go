package blob

import (
	"errors"
	"strings"
	"testing"
)

func TestMasterKeyLayout(t *testing.T) {
	key, err := MasterKey("org_abc", "ep_xyz", "Interview Final.mp4")
	if err != nil {
		t.Fatalf("MasterKey: %v", err)
	}
	if key != "org_abc/ep_xyz/masters/Interview_Final.mp4" {
		t.Fatalf("key = %q", key)
	}
}

func TestMasterKeyRejectsBadIDs(t *testing.T) {
	cases := []struct{ org, ep string }{
		{"", "ep_x"},
		{"org_x", ""},
		{"org/../x", "ep_x"},
		{"org_x", "ep/y"},
		{"..", "ep_x"},
		{"org_x", "."},
	}
	for _, c := range cases {
		if _, err := MasterKey(c.org, c.ep, "f.mp4"); err == nil {
			t.Errorf("MasterKey(%q,%q) = nil err, want rejection", c.org, c.ep)
		}
	}
}

func TestMasterKeyNeverEscapesPrefix(t *testing.T) {
	// A hostile filename must not add path segments beyond the masters/ folder.
	key, err := MasterKey("org_a", "ep_b", "../../../../etc/passwd")
	if err != nil {
		t.Fatalf("MasterKey: %v", err)
	}
	if !strings.HasPrefix(key, "org_a/ep_b/masters/") {
		t.Fatalf("key escaped prefix: %q", key)
	}
	if strings.Contains(key, "..") {
		t.Fatalf("key contains traversal: %q", key)
	}
	if strings.Count(key, "/") != 3 {
		t.Fatalf("key has extra segments: %q", key)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"clip.mp4", "clip.mp4"},
		{"My File.mov", "My_File.mov"},
		{"../../etc/passwd", "passwd"},
		{`C:\Users\x\a.mxf`, "a.mxf"},
		{"weird\x00name.mp4", "weird_name.mp4"},
		{"a   b   c.mp4", "a_b_c.mp4"},
		{"  spaced.mp4  ", "spaced.mp4"},
		{"...leading.mp4", "leading.mp4"},
		{"tab\tsep.mp4", "tab_sep.mp4"},
	}
	for _, c := range cases {
		got, err := SanitizeFilename(c.in)
		if err != nil {
			t.Errorf("SanitizeFilename(%q) err = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.ContainsAny(got, "/\\") || strings.Contains(got, "..") {
			t.Errorf("SanitizeFilename(%q) = %q leaves unsafe chars", c.in, got)
		}
	}
}

func TestSanitizeFilenameRejectsEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", ".", "..", "/", "///", "___", "...", "\x00"} {
		if _, err := SanitizeFilename(in); !errors.Is(err, ErrBadFilename) {
			t.Errorf("SanitizeFilename(%q) err = %v, want ErrBadFilename", in, err)
		}
	}
}

func TestSanitizeFilenameLengthCap(t *testing.T) {
	long := strings.Repeat("a", 500) + ".mp4"
	got, err := SanitizeFilename(long)
	if err != nil {
		t.Fatalf("SanitizeFilename: %v", err)
	}
	if len(got) > maxFilenameLen {
		t.Fatalf("length = %d, want <= %d", len(got), maxFilenameLen)
	}
	if !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("extension not preserved: %q", got)
	}
}
