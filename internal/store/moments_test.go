package store

import (
	"strings"
	"testing"

	"blueshift/internal/pipeline"
)

// sampleMomentRows is a two-moment proposal set over sampleSegments(): the
// span-derived ASR times come from the segments' start/end, and rank 1's quote
// carries the U+200C ZWNJ so verbatim storage is proven at rest.
func sampleMomentRows() []pipeline.MomentRow {
	return []pipeline.MomentRow{
		{
			ProposedMoment: pipeline.ProposedMoment{
				Rank: 1, StartIdx: 1, EndIdx: 1,
				RationaleEn: "The guest's reply is the quotable beat.",
				QuoteFa:     "خیلی " + zwnjWord,
			},
			StartMs: 1000, EndMs: 1600,
		},
		{
			ProposedMoment: pipeline.ProposedMoment{
				Rank: 2, StartIdx: 0, EndIdx: 0,
				RationaleEn: "The greeting works as a cold open.",
				QuoteFa:     "سلام",
			},
			StartMs: 0, EndMs: 900,
		},
	}
}

// TestReplaceMomentsRoundTripRankOrdered inserts a proposal set and reads it
// back: rank-ordered, every field verbatim (ZWNJ included), status defaulted
// to 'proposed' with a NULL status_changed_at.
func TestReplaceMomentsRoundTripRankOrdered(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	want := sampleMomentRows()
	// Insert deliberately rank-2-first: the read must come back rank-ordered.
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, []pipeline.MomentRow{want[1], want[0]}); err != nil {
		t.Fatalf("ReplaceMoments: %v", err)
	}

	got, err := st.EpisodeMoments(ctx, f.orgEncoded, f.epEncoded)
	if err != nil {
		t.Fatalf("EpisodeMoments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d moments, want 2", len(got))
	}
	for i, w := range want {
		g := got[i]
		if g.Rank != w.Rank || g.StartIdx != w.StartIdx || g.EndIdx != w.EndIdx ||
			g.StartMs != w.StartMs || g.EndMs != w.EndMs {
			t.Errorf("moment %d = %+v, want %+v", i, g, w)
		}
		if g.RationaleEn != w.RationaleEn || g.QuoteFa != w.QuoteFa {
			t.Errorf("moment %d texts = (%q,%q), want (%q,%q) verbatim", i, g.RationaleEn, g.QuoteFa, w.RationaleEn, w.QuoteFa)
		}
		if g.Status != MomentStatusProposed {
			t.Errorf("moment %d status = %q, want proposed (the default)", i, g.Status)
		}
		if !g.StatusChangedAt.IsZero() {
			t.Errorf("moment %d status_changed_at = %v, want zero (never reviewed)", i, g.StatusChangedAt)
		}
	}
	// The ZWNJ survived to the read verbatim.
	if !strings.ContainsRune(got[0].QuoteFa, '‌') {
		t.Errorf("quote %q lost its U+200C ZWNJ", got[0].QuoteFa)
	}
}

// TestReplaceMomentsIdempotentReplace proves the replace-per-episode
// choreography: a re-run of the same set is clean, and a different set
// overwrites wholesale (including resetting any human verdicts — reprocessing
// deliberately restarts review).
func TestReplaceMomentsIdempotentReplace(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, sampleMomentRows()); err != nil {
		t.Fatalf("ReplaceMoments (first): %v", err)
	}
	// Approve rank 1, then re-run the SAME set: the replace resets it to proposed.
	if ok, err := st.SetMomentStatus(ctx, f.orgEncoded, f.epEncoded, 1, MomentStatusApproved); err != nil || !ok {
		t.Fatalf("SetMomentStatus(approve) = (%v, %v), want (true, nil)", ok, err)
	}
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, sampleMomentRows()); err != nil {
		t.Fatalf("ReplaceMoments (re-run): %v", err)
	}
	got, _ := st.EpisodeMoments(ctx, f.orgEncoded, f.epEncoded)
	if len(got) != 2 || got[0].Status != MomentStatusProposed {
		t.Fatalf("after re-run: %d moments, rank-1 status %q, want 2 and proposed", len(got), got[0].Status)
	}

	// A smaller set overwrites wholesale (no stale rank-2 row survives).
	one := sampleMomentRows()[:1]
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, one); err != nil {
		t.Fatalf("ReplaceMoments (overwrite): %v", err)
	}
	got, _ = st.EpisodeMoments(ctx, f.orgEncoded, f.epEncoded)
	if len(got) != 1 || got[0].Rank != 1 {
		t.Errorf("after overwrite: %+v, want exactly the rank-1 moment", got)
	}
}

// TestMomentsExist proves the moments stage's cost-safety idempotency probe:
// false with no rows, true once the proposal set is persisted, false again
// after a wholesale replace with nothing (and always false cross-org).
func TestMomentsExist(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	if done, err := st.MomentsExist(ctx, f.orgEncoded, f.epEncoded); err != nil || done {
		t.Fatalf("MomentsExist (none) = (%v, %v), want (false, nil)", done, err)
	}
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, sampleMomentRows()); err != nil {
		t.Fatalf("ReplaceMoments: %v", err)
	}
	if done, err := st.MomentsExist(ctx, f.orgEncoded, f.epEncoded); err != nil || !done {
		t.Fatalf("MomentsExist (proposed) = (%v, %v), want (true, nil)", done, err)
	}
	// Org-scoped: a foreign org never sees another tenant's moments as done.
	if done, err := st.MomentsExist(ctx, foreignOrg(), f.epEncoded); err != nil || done {
		t.Errorf("cross-org MomentsExist = (%v, %v), want (false, nil)", done, err)
	}
}

// TestSetMomentStatusTransitions walks the legal transition set — proposed ->
// approved -> proposed -> dismissed -> proposed — and proves the illegal ones
// (approved -> dismissed, same-status no-op, unknown rank, unknown status)
// refuse cleanly without touching the row.
func TestSetMomentStatusTransitions(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, sampleMomentRows()); err != nil {
		t.Fatalf("ReplaceMoments: %v", err)
	}
	statusOf := func(rank int) string {
		t.Helper()
		ms, err := st.EpisodeMoments(ctx, f.orgEncoded, f.epEncoded)
		if err != nil {
			t.Fatalf("EpisodeMoments: %v", err)
		}
		for _, m := range ms {
			if m.Rank == rank {
				return m.Status
			}
		}
		t.Fatalf("rank %d not found", rank)
		return ""
	}

	// proposed -> approved (stamps status_changed_at).
	if ok, err := st.SetMomentStatus(ctx, f.orgEncoded, f.epEncoded, 1, MomentStatusApproved); err != nil || !ok {
		t.Fatalf("proposed->approved = (%v, %v), want (true, nil)", ok, err)
	}
	if got := statusOf(1); got != MomentStatusApproved {
		t.Fatalf("status = %q, want approved", got)
	}
	ms, _ := st.EpisodeMoments(ctx, f.orgEncoded, f.epEncoded)
	if ms[0].StatusChangedAt.IsZero() {
		t.Error("status_changed_at not stamped by the approve")
	}

	// approved -> dismissed is NOT a legal transition (undo goes through proposed).
	if ok, err := st.SetMomentStatus(ctx, f.orgEncoded, f.epEncoded, 1, MomentStatusDismissed); err != nil || ok {
		t.Fatalf("approved->dismissed = (%v, %v), want (false, nil) refusal", ok, err)
	}
	if got := statusOf(1); got != MomentStatusApproved {
		t.Errorf("status after refused transition = %q, want approved (untouched)", got)
	}

	// approved -> proposed (the undo), then proposed -> dismissed.
	if ok, err := st.SetMomentStatus(ctx, f.orgEncoded, f.epEncoded, 1, MomentStatusProposed); err != nil || !ok {
		t.Fatalf("approved->proposed = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := st.SetMomentStatus(ctx, f.orgEncoded, f.epEncoded, 1, MomentStatusDismissed); err != nil || !ok {
		t.Fatalf("proposed->dismissed = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := st.SetMomentStatus(ctx, f.orgEncoded, f.epEncoded, 1, MomentStatusProposed); err != nil || !ok {
		t.Fatalf("dismissed->proposed = (%v, %v), want (true, nil)", ok, err)
	}

	// A same-status "transition" refuses (proposed -> proposed matches no row).
	if ok, err := st.SetMomentStatus(ctx, f.orgEncoded, f.epEncoded, 1, MomentStatusProposed); err != nil || ok {
		t.Errorf("proposed->proposed = (%v, %v), want (false, nil) refusal", ok, err)
	}
	// An unknown rank refuses.
	if ok, err := st.SetMomentStatus(ctx, f.orgEncoded, f.epEncoded, 99, MomentStatusApproved); err != nil || ok {
		t.Errorf("unknown rank = (%v, %v), want (false, nil) refusal", ok, err)
	}
	// An unknown status is a hard error (programming fault), before the DB.
	if _, err := st.SetMomentStatus(ctx, f.orgEncoded, f.epEncoded, 1, "bogus"); err == nil {
		t.Error("unknown status accepted, want error")
	}
}

// TestMomentsOrgScoped proves every moment operation is org-scoped: a foreign
// org can neither write, read, nor flip another tenant's moments.
func TestMomentsOrgScoped(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	if err := st.ReplaceMoments(ctx, f.orgEncoded, f.epEncoded, sampleMomentRows()); err != nil {
		t.Fatalf("ReplaceMoments: %v", err)
	}
	other := foreignOrg()

	// A foreign org cannot replace this episode's moments (clean no-op).
	if err := st.ReplaceMoments(ctx, other, f.epEncoded, sampleMomentRows()[:1]); err != nil {
		t.Fatalf("cross-org ReplaceMoments returned error: %v", err)
	}
	if got, _ := st.EpisodeMoments(ctx, f.orgEncoded, f.epEncoded); len(got) != 2 {
		t.Errorf("cross-org replace leaked: %d moments, want 2 untouched", len(got))
	}
	// A foreign org cannot read them.
	if got, err := st.EpisodeMoments(ctx, other, f.epEncoded); err != nil || got != nil {
		t.Errorf("cross-org EpisodeMoments = (%v, %v), want (nil, nil)", got, err)
	}
	if _, ok, err := st.SegmentsForMoments(ctx, other, f.epEncoded); err != nil || ok {
		t.Errorf("cross-org SegmentsForMoments = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	// A foreign org cannot flip a status.
	if ok, err := st.SetMomentStatus(ctx, other, f.epEncoded, 1, MomentStatusApproved); err != nil || ok {
		t.Errorf("cross-org SetMomentStatus = (%v, %v), want (false, nil)", ok, err)
	}
	if got, _ := st.EpisodeMoments(ctx, f.orgEncoded, f.epEncoded); got[0].Status != MomentStatusProposed {
		t.Errorf("cross-org flip leaked: status %q, want proposed", got[0].Status)
	}
}

// TestSegmentsForMomentsSpeakerAware proves the moments read carries the
// diarization speaker_key alongside the verbatim transcript, idx-ordered, with
// the internal audit ids — and "" for a not-yet-diarized segment.
func TestSegmentsForMomentsSpeakerAware(t *testing.T) {
	f := newSegFixture(t)
	st, ctx := f.st, f.ctx

	if err := st.ReplaceSegments(ctx, f.orgEncoded, f.epEncoded, sampleSegments()); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	// Diarize only segment 0: segment 1 must read back with an empty key.
	if err := st.SetSegmentSpeakers(ctx, f.orgEncoded, f.epEncoded, map[int]string{0: "S1"}); err != nil {
		t.Fatalf("SetSegmentSpeakers: %v", err)
	}
	set, ok, err := st.SegmentsForMoments(ctx, f.orgEncoded, f.epEncoded)
	if err != nil || !ok {
		t.Fatalf("SegmentsForMoments = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if set.EpisodeID != f.epID {
		t.Errorf("EpisodeID = %d, want %d (internal episode id)", set.EpisodeID, f.epID)
	}
	if len(set.Segments) != 2 || set.Segments[0].Idx != 0 || set.Segments[1].Idx != 1 {
		t.Fatalf("segments not idx-ordered: %+v", set.Segments)
	}
	if set.Segments[0].SpeakerKey != "S1" || set.Segments[1].SpeakerKey != "" {
		t.Errorf("speaker keys = (%q, %q), want (S1, \"\")", set.Segments[0].SpeakerKey, set.Segments[1].SpeakerKey)
	}
	if set.Segments[1].Text != sampleSegments()[1].Text {
		t.Errorf("text = %q, want verbatim %q", set.Segments[1].Text, sampleSegments()[1].Text)
	}
}
