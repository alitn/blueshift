package store

// DB-backed tests for the compose store surface: the source column semantics
// (reprocess deletes ONLY source='auto'; kept composed rows survive and
// renumber), the next-free-rank composed insert, the auto-scoped idempotency
// probe, and the review-dialect compose transcript read. They run against the
// per-run scratch Postgres and skip without TEST_DATABASE_URL, like every
// other store test.

import (
	"strings"
	"testing"

	"blueshift/internal/pipeline"
)

// composedRow is a kept composed moment over sampleSegments(): segment 1's
// ZWNJ-carrying quote with its word-accurate times (the seam derives these
// before the store is called; the store only persists).
func composedRow() pipeline.MomentRow {
	return pipeline.MomentRow{
		ProposedMoment: pipeline.ProposedMoment{
			// Rank is ignored by the insert (the store assigns next-free).
			Rank: 1, StartIdx: 1, EndIdx: 1,
			RationaleEn: "Keep the joy beat.",
			QuoteFa:     zwnjWord,
		},
		StartMs: 1240, EndMs: 1600,
	}
}

// momentSource reads a moment's source column by (episode, rank) — the test's
// eye on the column the DTOs deliberately never expose.
func momentSource(t *testing.T, f segFixture, rank int) string {
	t.Helper()
	var src string
	if err := f.st.Pool().QueryRow(f.ctx,
		`SELECT source FROM moments WHERE episode_id = $1 AND rank = $2`, f.epID, rank).Scan(&src); err != nil {
		t.Fatalf("read source of rank %d: %v", rank, err)
	}
	return src
}

// TestInsertComposedMomentNextFreeRank proves approve-to-keep lands at
// rank = max(rank)+1 with source='prompt', status='approved', and a stamped
// status_changed_at — and at rank 1 on an episode with no moments at all.
func TestInsertComposedMomentNextFreeRank(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}

	// No moments yet: the first keep takes rank 1.
	m, ok, err := st.InsertComposedMoment(ctx, f.orgUUID, f.epEncoded, composedRow())
	if err != nil || !ok {
		t.Fatalf("InsertComposedMoment (empty) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if m.Rank != 1 {
		t.Errorf("first kept rank = %d, want 1 (no moments yet)", m.Rank)
	}
	if m.Status != MomentStatusApproved || m.StatusChangedAt.IsZero() {
		t.Errorf("kept moment = status %q changed-at %v, want approved and stamped", m.Status, m.StatusChangedAt)
	}
	if got := momentSource(t, f, m.Rank); got != "prompt" {
		t.Errorf("source = %q, want prompt", got)
	}
	if !strings.ContainsRune(m.QuoteFa, '‌') {
		t.Errorf("kept quote %q lost its U+200C", m.QuoteFa)
	}

	// Behind an auto set: the next keep takes max(rank)+1.
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, sampleMomentRows()); err != nil {
		t.Fatalf("ReplaceMoments: %v", err)
	}
	// (The replace renumbered the kept row to follow the new auto set: 2+1.)
	m2, ok, err := st.InsertComposedMoment(ctx, f.orgUUID, f.epEncoded, composedRow())
	if err != nil || !ok {
		t.Fatalf("InsertComposedMoment (behind autos) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if m2.Rank != 4 {
		t.Errorf("second kept rank = %d, want 4 (after 2 autos + 1 kept)", m2.Rank)
	}

	// Cross-org: an org that owns nothing cannot keep into this episode.
	other := foreignOrgUUID(t, f)
	if _, ok, err := st.InsertComposedMoment(ctx, other, f.epEncoded, composedRow()); err != nil || ok {
		t.Errorf("cross-org InsertComposedMoment = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	if got, _ := st.EpisodeMoments(ctx, f.orgUUID, f.epEncoded); len(got) != 4 {
		t.Errorf("moments after cross-org attempt = %d, want 4 untouched", len(got))
	}
}

// TestReplaceMomentsSparesComposed is the reprocess-gap proof: a wholesale
// stage replace deletes ONLY source='auto' rows; a kept composed moment
// survives with its verdict, renumbered to follow the fresh auto set even
// when the new set is LARGER than the old one (the rank-collision case).
func TestReplaceMomentsSparesComposed(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	// 2 autos + 1 kept (rank 3).
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, sampleMomentRows()); err != nil {
		t.Fatalf("ReplaceMoments (seed): %v", err)
	}
	kept, ok, err := st.InsertComposedMoment(ctx, f.orgUUID, f.epEncoded, composedRow())
	if err != nil || !ok || kept.Rank != 3 {
		t.Fatalf("InsertComposedMoment = (rank=%d, ok=%v, err=%v), want rank 3", kept.Rank, ok, err)
	}

	// Reprocess with a LARGER auto set (3 rows): the fresh rank 3 would
	// collide with the kept row if the replace did not renumber it.
	three := append(sampleMomentRows(), pipeline.MomentRow{
		ProposedMoment: pipeline.ProposedMoment{Rank: 3, StartIdx: 0, EndIdx: 1,
			RationaleEn: "The full exchange.", QuoteFa: "سلام"},
		StartMs: 0, EndMs: 520,
	})
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, three); err != nil {
		t.Fatalf("ReplaceMoments (reprocess, larger set): %v", err)
	}

	got, err := st.EpisodeMoments(ctx, f.orgUUID, f.epEncoded)
	if err != nil {
		t.Fatalf("EpisodeMoments: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("moments after reprocess = %d, want 4 (3 fresh autos + 1 surviving kept)", len(got))
	}
	// Fresh autos: ranks 1..3, reset to proposed, source auto.
	for i := 0; i < 3; i++ {
		if got[i].Rank != i+1 || got[i].Status != MomentStatusProposed {
			t.Errorf("auto %d = rank %d status %q, want rank %d proposed", i, got[i].Rank, got[i].Status, i+1)
		}
		if src := momentSource(t, f, i+1); src != "auto" {
			t.Errorf("rank %d source = %q, want auto", i+1, src)
		}
	}
	// The kept row: renumbered to 4, verdict and texts intact, source prompt.
	k := got[3]
	if k.Rank != 4 || k.Status != MomentStatusApproved {
		t.Errorf("kept row = rank %d status %q, want rank 4 approved (verdict survives reprocess)", k.Rank, k.Status)
	}
	if k.QuoteFa != composedRow().QuoteFa || k.StartMs != 1240 || k.EndMs != 1600 {
		t.Errorf("kept row content = %+v, want the original quote and times verbatim", k)
	}
	if src := momentSource(t, f, 4); src != "prompt" {
		t.Errorf("kept row source = %q, want prompt", src)
	}

	// A replace with an EMPTY set (the delete path) also spares the kept row,
	// renumbering it to rank 1.
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, nil); err != nil {
		t.Fatalf("ReplaceMoments (empty): %v", err)
	}
	got, _ = st.EpisodeMoments(ctx, f.orgUUID, f.epEncoded)
	if len(got) != 1 || got[0].Rank != 1 || got[0].Status != MomentStatusApproved {
		t.Errorf("after empty replace = %+v, want exactly the kept row at rank 1, still approved", got)
	}
}

// TestMomentsExistIgnoresComposed proves the cost-safety probe is auto-scoped:
// a kept composed moment alone must NOT read as "stage already ran" (it would
// silently suppress the stage's own proposals forever).
func TestMomentsExistIgnoresComposed(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	if _, ok, err := st.InsertComposedMoment(ctx, f.orgUUID, f.epEncoded, composedRow()); err != nil || !ok {
		t.Fatalf("InsertComposedMoment = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if done, err := st.MomentsExist(ctx, f.orgEncoded, f.epEncoded); err != nil || done {
		t.Fatalf("MomentsExist (composed only) = (%v, %v), want (false, nil) — composed rows must not mark the stage done", done, err)
	}
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, sampleMomentRows()); err != nil {
		t.Fatalf("ReplaceMoments: %v", err)
	}
	if done, err := st.MomentsExist(ctx, f.orgEncoded, f.epEncoded); err != nil || !done {
		t.Errorf("MomentsExist (auto set persisted) = (%v, %v), want (true, nil)", done, err)
	}
}

// TestTranscriptForCompose proves the review-dialect compose read: the
// speaker-aware idx-ordered transcript with words, the episode's language, the
// internal audit ids — and the two refusal shapes (foreign org: ok=false;
// found but untranscribed: ok=true with zero segments).
func TestTranscriptForCompose(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	// Found, not yet transcribed: ok with an empty transcript.
	set, _, ok, err := st.TranscriptForCompose(ctx, f.orgUUID, f.epEncoded)
	if err != nil || !ok {
		t.Fatalf("TranscriptForCompose (no segments) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if len(set.Segments) != 0 {
		t.Errorf("segments = %d, want 0 before transcription", len(set.Segments))
	}

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	if err := st.SetSegmentSpeakers(ctx, f.orgEncoded, f.epEncoded, map[int]string{0: "S1"}); err != nil {
		t.Fatalf("SetSegmentSpeakers: %v", err)
	}

	var language string
	set, language, ok, err = st.TranscriptForCompose(ctx, f.orgUUID, f.epEncoded)
	if err != nil || !ok {
		t.Fatalf("TranscriptForCompose = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if language != "fa" {
		t.Errorf("language = %q, want fa", language)
	}
	if set.EpisodeID != f.epID || set.OrgID <= 0 {
		t.Errorf("audit ids = org %d episode %d, want internal ids (episode %d)", set.OrgID, set.EpisodeID, f.epID)
	}
	if len(set.Segments) != 2 || set.Segments[0].Idx != 0 || set.Segments[1].Idx != 1 {
		t.Fatalf("segments not idx-ordered: %+v", set.Segments)
	}
	if set.Segments[0].SpeakerKey != "S1" || set.Segments[1].SpeakerKey != "" {
		t.Errorf("speaker keys = (%q, %q), want (S1, \"\")", set.Segments[0].SpeakerKey, set.Segments[1].SpeakerKey)
	}
	// Words round-trip verbatim — the compose validator aligns quotes on them.
	if len(set.Segments[1].Words) != 2 || set.Segments[1].Words[1].Text != zwnjWord {
		t.Errorf("segment 1 words = %+v, want the verbatim ZWNJ word data", set.Segments[1].Words)
	}

	// Foreign org (exists, owns nothing): an indistinguishable miss.
	other := foreignOrgUUID(t, f)
	if _, _, ok, err := st.TranscriptForCompose(ctx, other, f.epEncoded); err != nil || ok {
		t.Errorf("cross-org TranscriptForCompose = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}
