package store

import (
	"reflect"
	"strings"
	"testing"

	"blueshift/internal/ids"
)

// TestSetSegmentSpeakersPersistsAndReads inserts a transcript, stamps a speaker
// grouping, and reads it back — asserting the speaker_key per idx AND that the
// verbatim transcript (text, word timings, the U+200C ZWNJ) is untouched: the
// diarize write only ever touches speaker_key.
func TestSetSegmentSpeakersPersistsAndReads(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	segs := sampleSegments() // idx 0,1 — seg 1 carries a ZWNJ
	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, segs); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}

	// Before diarization, speaker_key is NULL -> "" on read.
	pre, err := st.EpisodeSegmentsWithSpeakers(ctx, f.orgEncoded, f.epEncoded)
	if err != nil {
		t.Fatalf("EpisodeSegmentsWithSpeakers (pre): %v", err)
	}
	if len(pre) != 2 || pre[0].SpeakerKey != "" || pre[1].SpeakerKey != "" {
		t.Fatalf("pre-diarization speaker_keys = %q,%q, want empty (NULL)", speakerKeys(pre)[0], speakerKeys(pre)[1])
	}

	if err := st.SetSegmentSpeakers(ctx, f.orgEncoded, f.epEncoded, map[int]string{0: "S1", 1: "S2"}); err != nil {
		t.Fatalf("SetSegmentSpeakers: %v", err)
	}

	got, err := st.EpisodeSegmentsWithSpeakers(ctx, f.orgEncoded, f.epEncoded)
	if err != nil {
		t.Fatalf("EpisodeSegmentsWithSpeakers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d segments, want 2", len(got))
	}
	if got[0].SpeakerKey != "S1" || got[1].SpeakerKey != "S2" {
		t.Errorf("speaker_keys = %q,%q, want S1,S2", got[0].SpeakerKey, got[1].SpeakerKey)
	}

	// Verbatim: the transcript read (asr.Segment) is byte-identical to what was
	// inserted — the speaker write moved no timestamp and rewrote no text.
	back, err := st.EpisodeSegments(ctx, f.orgEncoded, f.epEncoded)
	if err != nil {
		t.Fatalf("EpisodeSegments: %v", err)
	}
	if !reflect.DeepEqual(back, segs) {
		t.Errorf("transcript changed by the speaker write:\n got %+v\nwant %+v", back, segs)
	}
	// And the ZWNJ survived on the speaker-bearing read too.
	if !strings.ContainsRune(got[1].Words[1].Text, '\u200c') {
		t.Errorf("word %q lost its U+200C ZWNJ after the speaker write", got[1].Words[1].Text)
	}
}

// TestSetSegmentSpeakersIdempotentReplace proves re-running the diarize write is
// idempotent (a re-run of the SAME grouping leaves the same rows) and that a
// different grouping overwrites the prior one wholesale.
func TestSetSegmentSpeakersIdempotentReplace(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	group := map[int]string{0: "S1", 1: "S1"}
	if err := st.SetSegmentSpeakers(ctx, f.orgEncoded, f.epEncoded, group); err != nil {
		t.Fatalf("SetSegmentSpeakers (first): %v", err)
	}
	// Re-run the SAME grouping: idempotent (no error, same values).
	if err := st.SetSegmentSpeakers(ctx, f.orgEncoded, f.epEncoded, group); err != nil {
		t.Fatalf("SetSegmentSpeakers (re-run): %v", err)
	}
	got, _ := st.EpisodeSegmentsWithSpeakers(ctx, f.orgEncoded, f.epEncoded)
	if got[0].SpeakerKey != "S1" || got[1].SpeakerKey != "S1" {
		t.Errorf("after re-run, speaker_keys = %q,%q, want S1,S1", got[0].SpeakerKey, got[1].SpeakerKey)
	}

	// A different grouping overwrites.
	if err := st.SetSegmentSpeakers(ctx, f.orgEncoded, f.epEncoded, map[int]string{0: "S1", 1: "S2"}); err != nil {
		t.Fatalf("SetSegmentSpeakers (overwrite): %v", err)
	}
	got, _ = st.EpisodeSegmentsWithSpeakers(ctx, f.orgEncoded, f.epEncoded)
	if got[0].SpeakerKey != "S1" || got[1].SpeakerKey != "S2" {
		t.Errorf("after overwrite, speaker_keys = %q,%q, want S1,S2", got[0].SpeakerKey, got[1].SpeakerKey)
	}
}

// TestSegmentsForDiarizeOrderedWithIDs proves the diarize read returns the
// transcript in idx order together with the internal org/episode ids the audit is
// scoped by.
func TestSegmentsForDiarizeOrderedWithIDs(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	set, ok, err := st.SegmentsForDiarize(ctx, f.orgEncoded, f.epEncoded)
	if err != nil {
		t.Fatalf("SegmentsForDiarize: %v", err)
	}
	if !ok {
		t.Fatal("SegmentsForDiarize found=false, want true")
	}
	if len(set.Segments) != 2 || set.Segments[0].Idx != 0 || set.Segments[1].Idx != 1 {
		t.Errorf("segments not idx-ordered: %+v", set.Segments)
	}
	if set.EpisodeID != f.epID {
		t.Errorf("EpisodeID = %d, want %d (internal episode id)", set.EpisodeID, f.epID)
	}
	var wantOrg int64
	if err := st.Pool().QueryRow(ctx, `SELECT id FROM orgs WHERE name = 'Blueshift Pilot'`).Scan(&wantOrg); err != nil {
		t.Fatalf("find org: %v", err)
	}
	if set.OrgID != wantOrg {
		t.Errorf("OrgID = %d, want %d (internal org id)", set.OrgID, wantOrg)
	}
}

// TestSetSegmentSpeakersOrgScoped proves the write and read are org-scoped: a
// foreign org can neither stamp speakers nor read the diarize inputs.
func TestSetSegmentSpeakersOrgScoped(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	otherOrg := foreignOrg()

	// A foreign org cannot stamp this episode's speakers (clean no-op).
	if err := st.SetSegmentSpeakers(ctx, otherOrg, f.epEncoded, map[int]string{0: "S9", 1: "S9"}); err != nil {
		t.Fatalf("cross-org SetSegmentSpeakers returned error: %v", err)
	}
	got, _ := st.EpisodeSegmentsWithSpeakers(ctx, f.orgEncoded, f.epEncoded)
	if got[0].SpeakerKey != "" || got[1].SpeakerKey != "" {
		t.Errorf("cross-org write leaked speaker_keys %q,%q, want empty", got[0].SpeakerKey, got[1].SpeakerKey)
	}

	// A foreign org cannot read the diarize inputs.
	if _, ok, err := st.SegmentsForDiarize(ctx, otherOrg, f.epEncoded); err != nil || ok {
		t.Errorf("cross-org SegmentsForDiarize = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

// TestSpeakersAssigned proves the diarize stage's cost-safety idempotency probe:
// false with no segments, false while segments exist but are not ALL diarized (a
// partial/interrupted prior run), and true only when every segment carries a
// speaker_key — the precise "already done" condition that skips the billable LLM.
func TestSpeakersAssigned(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	// No segments yet -> not diarized (never "already done").
	if done, err := st.SpeakersAssigned(ctx, f.orgEncoded, f.epEncoded); err != nil || done {
		t.Fatalf("SpeakersAssigned (no segments) = (%v, %v), want (false, nil)", done, err)
	}
	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	// Segments exist but none carry a speaker_key -> not done.
	if done, err := st.SpeakersAssigned(ctx, f.orgEncoded, f.epEncoded); err != nil || done {
		t.Fatalf("SpeakersAssigned (undiarized) = (%v, %v), want (false, nil)", done, err)
	}
	// Diarize only ONE of the two segments -> partial, still not done (so the stage
	// re-diarizes rather than leaving a segment unattributed).
	if err := st.SetSegmentSpeakers(ctx, f.orgEncoded, f.epEncoded, map[int]string{0: "S1"}); err != nil {
		t.Fatalf("SetSegmentSpeakers (partial): %v", err)
	}
	if done, err := st.SpeakersAssigned(ctx, f.orgEncoded, f.epEncoded); err != nil || done {
		t.Fatalf("SpeakersAssigned (partial) = (%v, %v), want (false, nil)", done, err)
	}
	// Diarize every segment -> fully diarized.
	if err := st.SetSegmentSpeakers(ctx, f.orgEncoded, f.epEncoded, map[int]string{0: "S1", 1: "S2"}); err != nil {
		t.Fatalf("SetSegmentSpeakers (full): %v", err)
	}
	if done, err := st.SpeakersAssigned(ctx, f.orgEncoded, f.epEncoded); err != nil || !done {
		t.Fatalf("SpeakersAssigned (full) = (%v, %v), want (true, nil)", done, err)
	}
	// Org-scoped: a foreign org never sees another tenant's diarization as done.
	if done, err := st.SpeakersAssigned(ctx, foreignOrg(), f.epEncoded); err != nil || done {
		t.Errorf("cross-org SpeakersAssigned = (%v, %v), want (false, nil)", done, err)
	}
}

// speakerKeys is a diagnostic helper.
func speakerKeys(segs []SegmentWithSpeaker) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.SpeakerKey
	}
	return out
}

// foreignOrg is a well-formed encoded org id for a tenant that does not own the
// fixture episode.
func foreignOrg() string {
	return ids.Encode(ids.Org, [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
}
