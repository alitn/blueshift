package main

import (
	"strings"
	"testing"

	"blueshift/internal/blob"
	"blueshift/internal/ids"
)

// zwnj is U+200C, the zero-width non-joiner. The sample title must carry it
// verbatim so the demo exercises RTL + ZWNJ preservation end to end.
const zwnj = "\u200c"

func TestSampleTitleContainsZWNJ(t *testing.T) {
	if !strings.Contains(sampleTitle, zwnj) {
		t.Fatalf("sampleTitle %q is missing the ZWNJ (U+200C) — the demo must exercise ZWNJ preservation", sampleTitle)
	}
}

func TestSampleEpisodeUUIDRoundTrips(t *testing.T) {
	b, err := parseUUID(sampleEpisodeUUID)
	if err != nil {
		t.Fatalf("parseUUID(%q): %v", sampleEpisodeUUID, err)
	}
	enc := ids.Encode(ids.Episode, b)
	if !strings.HasPrefix(enc, "ep_") {
		t.Fatalf("encoded episode id %q missing ep_ prefix", enc)
	}
	got, err := ids.Decode(ids.Episode, enc)
	if err != nil {
		t.Fatalf("Decode(%q): %v", enc, err)
	}
	if got != b {
		t.Fatalf("round-trip mismatch: got %x want %x", got, b)
	}
}

func TestSampleMasterKeyIsOrgOwned(t *testing.T) {
	epBytes, err := parseUUID(sampleEpisodeUUID)
	if err != nil {
		t.Fatalf("parseUUID: %v", err)
	}
	// A representative (canonically-encoded) org id stands in for the runtime one.
	orgEncoded := ids.Encode(ids.Org, [16]byte{0: 0x01, 15: 0x0a})
	epEncoded := ids.Encode(ids.Episode, epBytes)
	key, err := blob.MasterKey(orgEncoded, epEncoded, sampleSourceFilename)
	if err != nil {
		t.Fatalf("MasterKey: %v", err)
	}
	if !strings.HasPrefix(key, orgEncoded+"/") {
		t.Fatalf("master key %q is not prefixed by the org id %q", key, orgEncoded)
	}
	if !strings.Contains(key, "/"+epEncoded+"/masters/") {
		t.Fatalf("master key %q missing episode masters segment", key)
	}
}
