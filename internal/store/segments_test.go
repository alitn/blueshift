package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/asr"
	"blueshift/internal/ids"
	"blueshift/internal/store/db"
)

// segFixture spins up an org/show and an inserted episode, returning the encoded
// org/episode ids plus the internal episode id, for the segment-store tests.
type segFixture struct {
	st         *Store
	ctx        context.Context
	orgEncoded string
	epEncoded  string
	epID       int64
}

func newSegFixture(t *testing.T) segFixture {
	t.Helper()
	dsn := requireDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)
	applyDevSeed(t, st, ctx)

	var orgID, showID int64
	if err := st.Pool().QueryRow(ctx, `SELECT id FROM orgs WHERE name = 'Blueshift Pilot'`).Scan(&orgID); err != nil {
		t.Fatalf("find org: %v", err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT id FROM shows WHERE org_id = $1 ORDER BY id LIMIT 1`, orgID).Scan(&showID); err != nil {
		t.Fatalf("find show: %v", err)
	}
	org, err := st.GetOrg(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrg: %v", err)
	}
	ep, err := st.InsertEpisode(ctx, db.InsertEpisodeParams{
		OrgID: orgID, ShowID: showID, Title: "Seg", SourceFilename: "m.mp4", Language: "fa",
		MasterObjectKey: pgtype.Text{String: "k/masters/m.mp4", Valid: true},
	})
	if err != nil {
		t.Fatalf("InsertEpisode: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(c, `DELETE FROM segments WHERE episode_id = $1`, ep.ID)
	})
	deleteEpisodeOnCleanup(t, st, ep.ID)
	return segFixture{st: st, ctx: ctx, orgEncoded: ids.Encode(ids.Org, org.PublicID.Bytes), epEncoded: ids.Encode(ids.Episode, ep.PublicID.Bytes), epID: ep.ID}
}

// zwnjWord carries a literal U+200C between its two morphemes; the store must
// round-trip it byte-for-byte (verbatim invariant, no normalization at rest).
const zwnjWord = "خوش\u200cحالم"

func sampleSegments() []asr.Segment {
	return []asr.Segment{
		{Idx: 0, StartMs: 0, EndMs: 900, Text: "سلام", Words: []asr.Word{
			{Text: "سلام", StartMs: 0, EndMs: 520, Conf: 0.98},
		}},
		{Idx: 1, StartMs: 1000, EndMs: 1600, Text: "خیلی " + zwnjWord, Words: []asr.Word{
			{Text: "خیلی", StartMs: 1000, EndMs: 1200, Conf: 0.96},
			{Text: zwnjWord, StartMs: 1240, EndMs: 1600, Conf: 0.95},
		}},
	}
}

// TestReplaceSegmentsRoundTripVerbatim inserts a transcript and reads it back,
// asserting exact preservation of text, word timings, and confidences through
// Postgres jsonb — including the U+200C ZWNJ byte-for-byte.
func TestReplaceSegmentsRoundTripVerbatim(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	want := sampleSegments()
	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, want); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}

	got, err := st.EpisodeSegments(ctx, f.orgEncoded, f.epEncoded)
	if err != nil {
		t.Fatalf("EpisodeSegments: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d segments, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Idx != want[i].Idx || got[i].StartMs != want[i].StartMs || got[i].EndMs != want[i].EndMs {
			t.Errorf("segment %d meta = %+v, want %+v", i, got[i], want[i])
		}
		if got[i].Text != want[i].Text {
			t.Errorf("segment %d text = %q, want %q (verbatim)", i, got[i].Text, want[i].Text)
		}
		if len(got[i].Words) != len(want[i].Words) {
			t.Fatalf("segment %d words = %d, want %d", i, len(got[i].Words), len(want[i].Words))
		}
		for j := range want[i].Words {
			if got[i].Words[j] != want[i].Words[j] {
				t.Errorf("segment %d word %d = %+v, want %+v", i, j, got[i].Words[j], want[i].Words[j])
			}
		}
	}

	// The ZWNJ survived to the decoded value.
	if !strings.ContainsRune(got[1].Words[1].Text, '\u200c') {
		t.Errorf("word %q lost its U+200C ZWNJ", got[1].Words[1].Text)
	}

	// Belt: the raw jsonb column holds the ZWNJ's UTF-8 bytes (0xE2 0x80 0x8C), so
	// the byte-exactness is not just an artefact of the decode path.
	var raw []byte
	if err := st.Pool().QueryRow(ctx,
		`SELECT words FROM segments WHERE episode_id = $1 AND idx = 1`, f.epID).Scan(&raw); err != nil {
		t.Fatalf("read raw words: %v", err)
	}
	if !strings.Contains(string(raw), "\u200c") {
		t.Errorf("raw words jsonb %q does not contain the U+200C byte sequence", string(raw))
	}
}

// TestReplaceSegmentsIdempotentReplace proves a re-run of the transcribe stage
// REPLACES the transcript rather than duplicating or appending it: a second call
// with a different, shorter set leaves exactly that set (UNIQUE(episode_id, idx)
// would reject a naive re-insert; delete-then-insert keeps it clean).
func TestReplaceSegmentsIdempotentReplace(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	// First: three segments.
	first := append(sampleSegments(), asr.Segment{Idx: 2, StartMs: 1700, EndMs: 2000, Text: "پایان", Words: []asr.Word{{Text: "پایان", StartMs: 1700, EndMs: 2000, Conf: 0.9}}})
	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, first); err != nil {
		t.Fatalf("ReplaceSegments (first): %v", err)
	}
	// Re-run the SAME set: idempotent (no duplicate-key error, same rows).
	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, first); err != nil {
		t.Fatalf("ReplaceSegments (re-run same): %v", err)
	}
	if n := f.count(t); n != 3 {
		t.Fatalf("after re-run of same set, rows = %d, want 3 (no duplicates)", n)
	}
	// Now a shorter, different set: the transcript is replaced wholesale.
	second := []asr.Segment{
		{Idx: 0, StartMs: 0, EndMs: 500, Text: "دوباره", Words: []asr.Word{{Text: "دوباره", StartMs: 0, EndMs: 500, Conf: 0.91}}},
		{Idx: 1, StartMs: 600, EndMs: 900, Text: "نو", Words: []asr.Word{{Text: "نو", StartMs: 600, EndMs: 900, Conf: 0.92}}},
	}
	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, second); err != nil {
		t.Fatalf("ReplaceSegments (second): %v", err)
	}
	if n := f.count(t); n != 2 {
		t.Fatalf("after replace, rows = %d, want 2 (old transcript gone)", n)
	}
	got, err := st.EpisodeSegments(ctx, f.orgEncoded, f.epEncoded)
	if err != nil {
		t.Fatalf("EpisodeSegments: %v", err)
	}
	if got[0].Text != "دوباره" || got[1].Text != "نو" {
		t.Errorf("replaced transcript = %q,%q, want دوباره,نو", got[0].Text, got[1].Text)
	}
}

// TestReplaceSegmentsOrderedByIdx proves ListSegmentsByEpisode returns segments in
// idx order regardless of insert order.
func TestReplaceSegmentsOrderedByIdx(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	// Insert in a shuffled slice order; idx values still ascend when read.
	shuffled := []asr.Segment{
		{Idx: 2, StartMs: 2000, EndMs: 2400, Text: "c"},
		{Idx: 0, StartMs: 0, EndMs: 400, Text: "a"},
		{Idx: 1, StartMs: 1000, EndMs: 1400, Text: "b"},
	}
	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, shuffled); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	got, err := st.EpisodeSegments(ctx, f.orgEncoded, f.epEncoded)
	if err != nil {
		t.Fatalf("EpisodeSegments: %v", err)
	}
	wantText := []string{"a", "b", "c"}
	for i, w := range wantText {
		if got[i].Idx != i || got[i].Text != w {
			t.Errorf("segment %d = (idx %d,%q), want (idx %d,%q)", i, got[i].Idx, got[i].Text, i, w)
		}
	}
}

// TestReplaceSegmentsOrgScoped proves the write and read are org-scoped: a foreign
// org can neither persist nor read another tenant's segments.
func TestReplaceSegmentsOrgScoped(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	otherOrg := ids.Encode(ids.Org, [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})

	// A foreign org cannot write this episode's segments (clean no-op).
	if err := st.ReplaceSegments(ctx, otherOrg, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("cross-org ReplaceSegments returned error: %v", err)
	}
	if n := f.count(t); n != 0 {
		t.Errorf("cross-org write persisted %d segments, want 0", n)
	}

	// The owning org writes them; a foreign org cannot read them back.
	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	cross, err := st.EpisodeSegments(ctx, otherOrg, f.epEncoded)
	if err != nil {
		t.Fatalf("cross-org EpisodeSegments returned error: %v", err)
	}
	if cross != nil {
		t.Errorf("cross-org read = %v, want nil (org-scoped)", cross)
	}
}

// TestHasSegments proves the transcribe stage's cost-safety idempotency probe:
// false before any transcript exists, true once segments are persisted, and
// org-scoped so a foreign org never sees another tenant's transcript as "already
// transcribed" (which would wrongly skip that tenant's billable ASR call).
func TestHasSegments(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if has, err := st.HasSegments(ctx, f.orgEncoded, f.epEncoded); err != nil || has {
		t.Fatalf("HasSegments before transcript = (%v, %v), want (false, nil)", has, err)
	}
	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	if has, err := st.HasSegments(ctx, f.orgEncoded, f.epEncoded); err != nil || !has {
		t.Fatalf("HasSegments after transcript = (%v, %v), want (true, nil)", has, err)
	}
	// Org-scoped: a foreign org must not see the segments.
	otherOrg := ids.Encode(ids.Org, [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	if has, err := st.HasSegments(ctx, otherOrg, f.epEncoded); err != nil || has {
		t.Errorf("cross-org HasSegments = (%v, %v), want (false, nil)", has, err)
	}
}

func (f segFixture) count(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := f.st.Pool().QueryRow(f.ctx, `SELECT count(*) FROM segments WHERE episode_id = $1`, f.epID).Scan(&n); err != nil {
		t.Fatalf("count segments: %v", err)
	}
	return n
}
