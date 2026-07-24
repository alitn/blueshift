package pipeline

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"blueshift/internal/asr"
)

// fourStageActive returns the [ingest, transcribe, diarize, moments] active
// chain the moments stage tests run under. Moments is registered but PARKED
// (out of the default ingest-only chain), so these tests activate it
// explicitly; moments is the chain's terminal stage, claimed as a continuation
// from diarize.
func fourStageActive() []stageDef {
	return mustResolveActiveStages([]Stage{StageIngest, StageTranscribe, StageDiarize, StageMoments})
}

// --- moments test doubles ------------------------------------------------------

// fakeSelector is a scriptable pipeline.MomentSelector: it returns preset
// proposals (or an error) and records what the stage passed it, so a test can
// assert the stage wired the language, audit scope, and segments through.
type fakeSelector struct {
	props   []ProposedMoment
	err     error
	calls   int
	gotLang string
	gotOrg  int64
	gotEp   int64
	gotSegs []MomentSegment
}

func (f *fakeSelector) SelectMoments(_ context.Context, language string, orgID, episodeID int64, segs []MomentSegment) ([]ProposedMoment, error) {
	f.calls++
	f.gotLang, f.gotOrg, f.gotEp, f.gotSegs = language, orgID, episodeID, segs
	if f.err != nil {
		return nil, f.err
	}
	return f.props, nil
}

// fakeMomentStore is a scriptable pipeline.MomentStore: it serves a preset
// MomentSegmentSet and captures the rows handed to ReplaceMoments. `exists`
// tracks the cost-safety idempotency state — MomentsExist reports it, and a
// successful ReplaceMoments flips it true (mirroring the store, where a
// completed moments stage leaves rows behind).
type fakeMomentStore struct {
	set          MomentSegmentSet
	found        bool
	readErr      error
	replaceErr   error
	replaceCalls int
	saved        []MomentRow
	exists       bool
	existsErr    error // scripted error for MomentsExist
}

func (f *fakeMomentStore) SegmentsForMoments(_ context.Context, _, _ string) (MomentSegmentSet, bool, error) {
	if f.readErr != nil {
		return MomentSegmentSet{}, false, f.readErr
	}
	return f.set, f.found, nil
}

func (f *fakeMomentStore) ReplaceMoments(_ context.Context, _, _ string, rows []MomentRow) error {
	f.replaceCalls++
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.saved = rows
	f.exists = true // a completed moments stage leaves the proposal set behind
	return nil
}

// MomentsExist is the cost-safety idempotency probe: true once the episode has
// moments (a prior ReplaceMoments succeeded, or a test seeded it).
func (f *fakeMomentStore) MomentsExist(_ context.Context, _, _ string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.exists, nil
}

// momentsStore builds a MomentStore serving two diarized fa segments — with
// word timings, the source the stage derives quote-accurate times from — plus
// the internal audit ids the stage forwards. Word times sit strictly inside
// the (padded) segment bounds so word-accuracy is observable.
func momentsStore() *fakeMomentStore {
	return &fakeMomentStore{
		found: true,
		set: MomentSegmentSet{
			OrgID: 11, EpisodeID: 22,
			Segments: []MomentSegment{
				{Segment: asr.Segment{Idx: 0, StartMs: 0, EndMs: 900, Text: "سلام", Words: []asr.Word{
					{Text: "سلام", StartMs: 40, EndMs: 520, Conf: 0.98},
				}}, SpeakerKey: "S1"},
				{Segment: asr.Segment{Idx: 1, StartMs: 1000, EndMs: 1600, Text: "خیلی خوش آمدید", Words: []asr.Word{
					{Text: "خیلی", StartMs: 1000, EndMs: 1180, Conf: 0.96},
					{Text: "خوش", StartMs: 1240, EndMs: 1400, Conf: 0.96},
					{Text: "آمدید", StartMs: 1450, EndMs: 1600, Conf: 0.94},
				}}, SpeakerKey: "S2"},
			},
		},
	}
}

// demoProposals is the two-moment proposal set the fake selector returns over
// momentsStore()'s segments.
func demoProposals() []ProposedMoment {
	return []ProposedMoment{
		{Rank: 1, StartIdx: 1, EndIdx: 1, RationaleEn: "Guest reply.", QuoteFa: "خوش آمدید"},
		{Rank: 2, StartIdx: 0, EndIdx: 0, RationaleEn: "Greeting.", QuoteFa: "سلام"},
	}
}

// --- tests -----------------------------------------------------------------

// TestMomentsStageRegisteredButParked is the core acceptance: moments is a
// registered (runnable) stage argument, yet it is NOT in the default active
// chain — the default worker stays ingest-only/terminal, unchanged. Enabling it
// is PIPELINE_STAGES' job.
func TestMomentsStageRegisteredButParked(t *testing.T) {
	if !ValidStage("moments") {
		t.Error("ValidStage(moments) = false, want true (moments is registered)")
	}
	// The default active chain is still exactly [ingest].
	if len(defaultActiveDefs) != 1 || defaultActiveDefs[0].name != StageIngest {
		t.Fatalf("defaultActiveDefs = %v, want exactly [ingest] (moments parked)", stageNames(defaultActiveDefs))
	}
	// A default runner does not carry moments in its chain.
	var r Runner
	if r.HasStage(StageMoments) {
		t.Error("default runner HasStage(moments) = true, want false (parked)")
	}
	// Moments can still be activated explicitly (registered), and is then terminal.
	if err := r.SetActiveStages([]Stage{StageIngest, StageTranscribe, StageDiarize, StageMoments}); err != nil {
		t.Fatalf("SetActiveStages with moments: %v", err)
	}
	if !r.HasStage(StageMoments) {
		t.Error("HasStage(moments) = false after activating it")
	}
	// Render remains unregistered: the four-stage chain is the ceiling today.
	if ValidStage("render") {
		t.Error("ValidStage(render) = true, want false (render lands with its own task)")
	}
}

// TestMomentsStagePersistsMoments drives the stage under the four-stage chain:
// it reads the speaker-aware transcript, asks the selector for proposals,
// derives each span's ASR times from the transcript rows, persists the set, and
// (as the terminal stage) marks the episode ready while preserving ingest's
// proxy + duration.
func TestMomentsStagePersistsMoments(t *testing.T) {
	repo := newFakeRepo()
	// Seeded already handed off from diarize (processing at diarize), with
	// ingest's proxy + measured duration recorded — the state moments claims.
	repo.addAtStage(epA, orgA, "diarize", "fa", 90_000)

	sel := &fakeSelector{props: demoProposals()}
	ms := momentsStore()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		Selector: sel,
		Moments:  ms,
		stages:   fourStageActive(),
	}

	if err := r.Run(context.Background(), epA, "moments"); err != nil {
		t.Fatalf("Run(moments): %v", err)
	}

	// Terminal: ready, at moments, with ingest's outputs preserved (COALESCE).
	e := repo.get(epA)
	if e.status != "ready" || e.stage != "moments" {
		t.Errorf("state = (%q,%q), want (ready,moments)", e.status, e.stage)
	}
	wantProxy := orgA + "/" + epA + "/proxies/" + proxyFilename
	if e.proxyKey != wantProxy || e.durationMs != 90_000 {
		t.Errorf("outputs = (%q,%d), want ingest's proxy %q + duration 90000 preserved", e.proxyKey, e.durationMs, wantProxy)
	}

	// The exact rows were persisted, once, with QUOTE-derived WORD-ACCURATE
	// times: rank 1's quote "خوش آمدید" is the TAIL of segment 1, so its times
	// are the quote's own words (1240..1600), NOT the segment bounds
	// (1000..1600); rank 2's quote is segment 0's single word (40..520), not the
	// padded segment bounds (0..900) — measured word values looked up from the
	// transcript rows, never selector output.
	if ms.replaceCalls != 1 {
		t.Errorf("ReplaceMoments calls = %d, want 1", ms.replaceCalls)
	}
	if len(ms.saved) != 2 {
		t.Fatalf("persisted %d moments, want 2", len(ms.saved))
	}
	if ms.saved[0].Rank != 1 || ms.saved[0].StartMs != 1240 || ms.saved[0].EndMs != 1600 {
		t.Errorf("rank 1 row = %+v, want quote-word times 1240..1600 (word-accurate, not segment-snapped)", ms.saved[0])
	}
	if ms.saved[1].Rank != 2 || ms.saved[1].StartMs != 40 || ms.saved[1].EndMs != 520 {
		t.Errorf("rank 2 row = %+v, want quote-word times 40..520 (word-accurate, not segment-snapped)", ms.saved[1])
	}
	if ms.saved[0].QuoteFa != "خوش آمدید" || ms.saved[1].QuoteFa != "سلام" {
		t.Errorf("persisted quotes = (%q,%q), want the verbatim proposals", ms.saved[0].QuoteFa, ms.saved[1].QuoteFa)
	}
	// The stage forwarded the language, the internal audit ids, and the segments.
	if sel.gotLang != "fa" || sel.gotOrg != 11 || sel.gotEp != 22 {
		t.Errorf("selector got (lang=%q, org=%d, ep=%d), want (fa, 11, 22)", sel.gotLang, sel.gotOrg, sel.gotEp)
	}
	if len(sel.gotSegs) != 2 || sel.gotSegs[0].Text != "سلام" || sel.gotSegs[0].SpeakerKey != "S1" {
		t.Errorf("selector got %d segments (first %+v), want the 2 speaker-aware transcript segments", len(sel.gotSegs), sel.gotSegs)
	}
}

// TestMomentsStageFailsNeutralOnSelectorError: a persistent selector failure
// (e.g. the llm one-retry-then-fail exhausted upstream) exhausts the stage's
// attempts and marks the episode failed with a NEUTRAL error_id — no cause text
// leaks — and nothing is persisted.
func TestMomentsStageFailsNeutralOnSelectorError(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "diarize", "fa", 5000)
	sel := &fakeSelector{err: errors.New("model output failed validation: gemini said no")}
	ms := momentsStore()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		Selector: sel,
		Moments:  ms,
		stages:   fourStageActive(),
	}

	err := r.Run(context.Background(), epA, "moments")
	if !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed", err)
	}
	e := repo.get(epA)
	if e.status != "failed" || e.stage != "moments" {
		t.Errorf("state = (%q,%q), want (failed,moments)", e.status, e.stage)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.errorID) {
		t.Errorf("error_id = %q, want a neutral 16-hex id", e.errorID)
	}
	if !regexp.MustCompile(`error_id=[0-9a-f]{16}`).MatchString(err.Error()) {
		t.Errorf("returned error = %q, want a neutral error_id", err.Error())
	}
	// The returned error never carries the provider/cause text.
	for _, leak := range []string{"gemini", "model output", "said no"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("returned error %q leaked %q", err.Error(), leak)
		}
	}
	// Every stage attempt re-ran the selector; nothing was persisted on failure.
	if sel.calls != 3 {
		t.Errorf("selector calls = %d, want 3 (1 + 2 retries)", sel.calls)
	}
	if ms.replaceCalls != 0 {
		t.Errorf("ReplaceMoments calls = %d, want 0 on a failed run", ms.replaceCalls)
	}
}

// TestMomentsStageReDriveBillsZero is the moments cost-safety idempotency
// proof: a plain re-drive of an episode that already has moments makes ZERO
// billable LLM calls. After the first run proposes, re-seeding at diarize and
// re-running SKIPS the paid call entirely (the selector call counter and the
// persist counter both stay put, and process_attempts does NOT advance) while
// the episode still finalizes ready.
func TestMomentsStageReDriveBillsZero(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "diarize", "fa", 90_000)
	sel := &fakeSelector{props: demoProposals()}
	ms := momentsStore()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		Selector: sel,
		Moments:  ms,
		stages:   fourStageActive(),
	}

	// First drive: the billable LLM call runs once and the set is persisted.
	if err := r.Run(context.Background(), epA, "moments"); err != nil {
		t.Fatalf("Run(moments) #1: %v", err)
	}
	if sel.calls != 1 || ms.replaceCalls != 1 {
		t.Fatalf("after drive #1: selector calls=%d, persist calls=%d, want 1 and 1", sel.calls, ms.replaceCalls)
	}
	billedAfterFirst := repo.get(epA).processAttempts
	if billedAfterFirst != 1 {
		t.Fatalf("process_attempts after drive #1 = %d, want 1 (one billable attempt)", billedAfterFirst)
	}

	// Second drive (a plain re-drive): re-seed at diarize and re-run. The stage
	// must SKIP the billable call — no selector call, no persist, no attempt consumed.
	repo.addAtStage(epA, orgA, "diarize", "fa", 90_000)
	repo.setProcessAttempts(epA, billedAfterFirst) // addAtStage reset the fresh fakeEp
	if err := r.Run(context.Background(), epA, "moments"); err != nil {
		t.Fatalf("Run(moments) #2: %v", err)
	}
	if sel.calls != 1 {
		t.Errorf("selector calls after re-drive = %d, want 1 (ZERO billable calls on the second run)", sel.calls)
	}
	if ms.replaceCalls != 1 {
		t.Errorf("ReplaceMoments calls after re-drive = %d, want 1 (the re-drive persisted nothing)", ms.replaceCalls)
	}
	if got := repo.get(epA).processAttempts; got != billedAfterFirst {
		t.Errorf("process_attempts after re-drive = %d, want %d (a skipped run consumes no billable budget)", got, billedAfterFirst)
	}
	if e := repo.get(epA); e.status != "ready" || e.stage != "moments" {
		t.Errorf("state after re-drive = (%q,%q), want (ready,moments) — a skip still finalizes", e.status, e.stage)
	}
}

// TestMomentsStageReprocessReproposes proves the explicit reprocess override:
// with Config.Reprocess set, the stage IGNORES the idempotency skip and re-runs
// the paid LLM even though the episode already has moments — the deliberate
// operator re-process a plain retry/re-drive must never trigger.
func TestMomentsStageReprocessReproposes(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "diarize", "fa", 90_000)
	sel := &fakeSelector{props: demoProposals()}
	ms := momentsStore()
	ms.exists = true // moments already proposed
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2, Reprocess: true},
		Selector: sel,
		Moments:  ms,
		stages:   fourStageActive(),
	}
	if err := r.Run(context.Background(), epA, "moments"); err != nil {
		t.Fatalf("Run(moments) reprocess: %v", err)
	}
	if sel.calls != 1 {
		t.Errorf("selector calls = %d, want 1 (reprocess forces the billable call despite existing moments)", sel.calls)
	}
	if got := repo.get(epA).processAttempts; got != 1 {
		t.Errorf("process_attempts = %d, want 1 (reprocess still consumes and respects the attempt budget)", got)
	}
}

// TestMomentsStageAttemptCapBlocksBeforeBillableCall proves the per-episode
// cap: an episode already at MAX_PROCESS_ATTEMPTS hard-fails WITHOUT ever
// calling the LLM, with a neutral error_id and no increment past the cap — the
// runaway backstop for the moments stage.
func TestMomentsStageAttemptCapBlocksBeforeBillableCall(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "diarize", "fa", 5000)
	repo.setProcessAttempts(epA, DefaultMaxProcessAttempts) // already at the cap
	sel := &fakeSelector{props: demoProposals()}
	ms := momentsStore()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2}, // MaxProcessAttempts unset -> DefaultMaxProcessAttempts
		Selector: sel,
		Moments:  ms,
		stages:   fourStageActive(),
	}

	err := r.Run(context.Background(), epA, "moments")
	if !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed (attempt cap)", err)
	}
	e := repo.get(epA)
	if e.status != "failed" || e.stage != "moments" {
		t.Errorf("state = (%q,%q), want (failed,moments)", e.status, e.stage)
	}
	if sel.calls != 0 {
		t.Errorf("selector calls = %d, want 0 (the cap blocks BEFORE any billable call)", sel.calls)
	}
	if ms.replaceCalls != 0 {
		t.Errorf("ReplaceMoments calls = %d, want 0 on a capped run", ms.replaceCalls)
	}
	if e.processAttempts != DefaultMaxProcessAttempts {
		t.Errorf("process_attempts = %d, want %d (unchanged — the cap does not increment)", e.processAttempts, DefaultMaxProcessAttempts)
	}
	if !regexp.MustCompile(`error_id=[0-9a-f]{16}`).MatchString(err.Error()) {
		t.Errorf("returned error = %q, want a neutral error_id", err.Error())
	}
}

// TestMomentsStageNoSegmentsFails proves the stage refuses to run when the
// episode has no transcript (an out-of-order run) rather than proposing from
// nothing — and that the refusal consumes no billable budget.
func TestMomentsStageNoSegmentsFails(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "diarize", "fa", 5000)
	sel := &fakeSelector{props: demoProposals()}
	// found=false: the episode resolves to no transcript.
	ms := &fakeMomentStore{found: false}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 0},
		Selector: sel,
		Moments:  ms,
		stages:   fourStageActive(),
	}
	if err := r.Run(context.Background(), epA, "moments"); !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed (no segments)", err)
	}
	if sel.calls != 0 {
		t.Errorf("selector calls = %d, want 0 (no segments to select from)", sel.calls)
	}
	if got := repo.get(epA).processAttempts; got != 0 {
		t.Errorf("process_attempts = %d, want 0 (non-billable prep failed first)", got)
	}
}

// TestMomentsStageNilSeamsFails proves a moments stage wired without its seams
// fails cleanly (rather than panicking), matching the other stages' nil-seam
// contract.
func TestMomentsStageNilSeamsFails(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "diarize", "fa", 5000)
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config: Config{Retries: 0},
		// Selector and Moments left nil.
		stages: fourStageActive(),
	}
	if err := r.Run(context.Background(), epA, "moments"); !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed (nil seams)", err)
	}
	if e := repo.get(epA); e.status != "failed" {
		t.Errorf("status = %q, want failed", e.status)
	}
}

// TestMomentsStageUnknownSpanIdxFails proves the defensive time-derivation
// guard: a proposal citing a segment idx the transcript does not carry fails
// the stage (never a guessed time) and persists nothing.
func TestMomentsStageUnknownSpanIdxFails(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "diarize", "fa", 5000)
	sel := &fakeSelector{props: []ProposedMoment{{Rank: 1, StartIdx: 7, EndIdx: 7, RationaleEn: "x", QuoteFa: "x"}}}
	ms := momentsStore()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 0},
		Selector: sel,
		Moments:  ms,
		stages:   fourStageActive(),
	}
	if err := r.Run(context.Background(), epA, "moments"); !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed (unknown span idx)", err)
	}
	if ms.replaceCalls != 0 {
		t.Errorf("ReplaceMoments calls = %d, want 0 (nothing persisted on a bad span)", ms.replaceCalls)
	}
}

// TestDeriveMomentRowsWordAccurate pins the word-accurate time derivation the
// amendment demands: times come from the QUOTE's first/last word — located
// deterministically within the span under the resegmentation join rule — never
// from segment bounds.
func TestDeriveMomentRowsWordAccurate(t *testing.T) {
	w := func(text string, s, e int) asr.Word { return asr.Word{Text: text, StartMs: s, EndMs: e, Conf: 0.95} }
	segs := []MomentSegment{
		// A long segment (0..10000) with five words: a mid-segment quote must
		// yield times STRICTLY tighter than the segment bounds.
		{Segment: asr.Segment{Idx: 0, StartMs: 0, EndMs: 10_000,
			Text: "سال اول همه چیز سخت بود",
			Words: []asr.Word{
				w("سال", 200, 700), w("اول", 800, 1200), w("همه", 2000, 2400),
				w("چیز", 2500, 2900), w("سخت", 4000, 4500), w("بود", 4600, 5000),
			}}, SpeakerKey: "S2"},
		{Segment: asr.Segment{Idx: 1, StartMs: 10_200, EndMs: 14_000,
			Text: "ولی دوباره شروع کردیم",
			Words: []asr.Word{
				w("ولی", 10_200, 10_500), w("دوباره", 10_600, 11_100),
				w("شروع", 11_200, 11_600), w("کردیم", 11_700, 12_200),
			}}, SpeakerKey: "S2"},
		// The word "سال" occurs here AGAIN, so a span covering 0..2 with quote
		// "سال" must deterministically take the FIRST in-span occurrence.
		{Segment: asr.Segment{Idx: 2, StartMs: 14_200, EndMs: 16_000,
			Text: "سال بعد بهتر شد",
			Words: []asr.Word{
				w("سال", 14_300, 14_700), w("بعد", 14_800, 15_100),
				w("بهتر", 15_200, 15_500), w("شد", 15_600, 15_900),
			}}, SpeakerKey: "S2"},
	}

	// Middle of a long segment: quote "همه چیز" -> its own words 2000..2900,
	// strictly inside the 0..10000 segment.
	rows, err := DeriveMomentRows([]ProposedMoment{
		{Rank: 1, StartIdx: 0, EndIdx: 0, RationaleEn: "x", QuoteFa: "همه چیز"},
	}, segs)
	if err != nil {
		t.Fatalf("DeriveMomentRows(middle): %v", err)
	}
	if rows[0].StartMs != 2000 || rows[0].EndMs != 2900 {
		t.Errorf("middle quote times = %d..%d, want 2000..2900 (word-accurate, not 0..10000)", rows[0].StartMs, rows[0].EndMs)
	}

	// Crossing a segment boundary: quote "بود ولی دوباره" starts at segment 0's
	// last word and ends inside segment 1.
	rows, err = DeriveMomentRows([]ProposedMoment{
		{Rank: 1, StartIdx: 0, EndIdx: 1, RationaleEn: "x", QuoteFa: "بود ولی دوباره"},
	}, segs)
	if err != nil {
		t.Fatalf("DeriveMomentRows(crossing): %v", err)
	}
	if rows[0].StartMs != 4600 || rows[0].EndMs != 11_100 {
		t.Errorf("crossing quote times = %d..%d, want 4600..11100 (across the boundary)", rows[0].StartMs, rows[0].EndMs)
	}

	// Ambiguous quote: "سال" occurs in segments 0 AND 2; a 0..2 span takes the
	// FIRST occurrence (200..700), deterministically.
	rows, err = DeriveMomentRows([]ProposedMoment{
		{Rank: 1, StartIdx: 0, EndIdx: 2, RationaleEn: "x", QuoteFa: "سال"},
	}, segs)
	if err != nil {
		t.Fatalf("DeriveMomentRows(ambiguous): %v", err)
	}
	if rows[0].StartMs != 200 || rows[0].EndMs != 700 {
		t.Errorf("ambiguous quote times = %d..%d, want 200..700 (first in-span occurrence)", rows[0].StartMs, rows[0].EndMs)
	}
	// And a span starting PAST the first occurrence takes the next one.
	rows, err = DeriveMomentRows([]ProposedMoment{
		{Rank: 1, StartIdx: 2, EndIdx: 2, RationaleEn: "x", QuoteFa: "سال"},
	}, segs)
	if err != nil {
		t.Fatalf("DeriveMomentRows(later span): %v", err)
	}
	if rows[0].StartMs != 14_300 || rows[0].EndMs != 14_700 {
		t.Errorf("later-span quote times = %d..%d, want 14300..14700", rows[0].StartMs, rows[0].EndMs)
	}

	// A quote the span's WORD data cannot locate is a hard error (the stage
	// fails rather than guessing a time), as is an unknown span idx.
	if _, err := DeriveMomentRows([]ProposedMoment{
		{Rank: 1, StartIdx: 0, EndIdx: 0, RationaleEn: "x", QuoteFa: "این متن وجود ندارد"},
	}, segs); err == nil {
		t.Error("DeriveMomentRows(absent quote) = nil, want alignment error")
	}
	if _, err := DeriveMomentRows([]ProposedMoment{
		{Rank: 1, StartIdx: 9, EndIdx: 9, RationaleEn: "x", QuoteFa: "سال"},
	}, segs); err == nil {
		t.Error("DeriveMomentRows(unknown idx) = nil, want error")
	}
}

// TestFourStageAutoAdvanceProposesThenReDriveBillsZero is the end-to-end proof
// of the activated demo/e2e chain (PIPELINE_STAGES=ingest,transcribe,diarize,
// moments): ONE Run(ingest) on an UPLOADED episode auto-advances through
// transcribe (the REAL offline ASR fake) and diarize into moments and finalizes
// 'ready' at moments with the proposal set persisted. It then proves
// cost-safety holds across ALL THREE billable stages sharing the one
// process_attempts counter:
//   - the full chain consumes exactly 3 billable attempts (one transcribe, one
//     diarize, one moments) — the shared cap covers all three;
//   - a plain re-drive of the fully-processed episode from transcribe onward
//     bills ZERO further calls on any stage (all three idempotency skips hold,
//     the counter stays put) while still finalizing 'ready'.
func TestFourStageAutoAdvanceProposesThenReDriveBillsZero(t *testing.T) {
	engine, err := asr.NewDefaultFakeEngine("bs-asr-1")
	if err != nil {
		t.Fatalf("NewDefaultFakeEngine: %v", err)
	}
	reg, err := asr.NewRegistry(engine)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4") // 'uploaded', language fa
	segs := newFakeSegments()
	diar := &fakeDiarizer{byIdx: map[int]string{0: "S1", 1: "S2"}}
	speakers := diarizeStore()
	sel := &fakeSelector{props: demoProposals()}
	ms := momentsStore()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2, AutoAdvance: true},
		ASR:      LangEngineResolver{Registry: reg, Label: "bs-asr-1"},
		Segments: segs,
		Diarizer: diar,
		Speakers: speakers,
		Selector: sel,
		Moments:  ms,
	}
	if err := r.SetActiveStages([]Stage{StageIngest, StageTranscribe, StageDiarize, StageMoments}); err != nil {
		t.Fatalf("SetActiveStages: %v", err)
	}
	tr := &runningTrigger{r: r}
	r.Trigger = tr

	// One Run(ingest) drives ingest -> transcribe -> diarize -> moments -> ready,
	// the whole chain in fake mode, just like an uploaded episode in make demo/e2e.
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest): %v", err)
	}

	e := repo.get(epA)
	if e.status != "ready" || e.stage != "moments" {
		t.Fatalf("after chain: state = (%q,%q), want (ready,moments)", e.status, e.stage)
	}
	if got := segs.get(epA); len(got) != 2 { // the offline fa fixture is two segments
		t.Fatalf("persisted %d segments, want 2 (the fa fake fixture)", len(segs.get(epA)))
	}
	if diar.calls != 1 || speakers.setCalls != 1 {
		t.Fatalf("after chain: diarizer calls=%d, speaker persists=%d, want 1 and 1", diar.calls, speakers.setCalls)
	}
	if sel.calls != 1 || ms.replaceCalls != 1 {
		t.Fatalf("after chain: selector calls=%d, moment persists=%d, want 1 and 1", sel.calls, ms.replaceCalls)
	}
	if len(ms.saved) != 2 || ms.saved[0].Rank != 1 || ms.saved[0].StartMs != 1240 {
		t.Fatalf("persisted moments = %+v, want the 2 ranked rows with quote-word-accurate times", ms.saved)
	}
	if calls := tr.snapshot(); len(calls) != 3 ||
		calls[0] != [2]string{epA, "transcribe"} || calls[1] != [2]string{epA, "diarize"} ||
		calls[2] != [2]string{epA, "moments"} {
		t.Fatalf("auto-advance fired %v, want [[%s transcribe] [%s diarize] [%s moments]]", calls, epA, epA, epA)
	}
	// SHARED CAP: all three billable stages drew on the one per-episode counter.
	billedAfterChain := e.processAttempts
	if billedAfterChain != 3 {
		t.Fatalf("process_attempts after chain = %d, want 3 (one transcribe + one diarize + one moments billable attempt)", billedAfterChain)
	}

	// Re-drive the processed episode from transcribe onward: re-seed at ingest
	// (the state a transcribe re-drive claims from), preserving process_attempts.
	// All three outputs already exist, so ALL THREE idempotency guards must SKIP
	// their paid calls — transcribe skips (segments exist) and hands off, diarize
	// skips (speakers assigned) and hands off, moments skips (moments exist) —
	// and the episode re-finalizes 'ready' having billed ZERO.
	repo.addAtStage(epA, orgA, "ingest", "fa", e.durationMs)
	repo.setProcessAttempts(epA, billedAfterChain) // addAtStage reset the fresh fakeEp
	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe) re-drive: %v", err)
	}
	if got := repo.get(epA).processAttempts; got != billedAfterChain {
		t.Errorf("process_attempts after re-drive = %d, want %d (a 4-stage re-drive bills ZERO)", got, billedAfterChain)
	}
	if segs.calls != 1 {
		t.Errorf("ReplaceSegments calls after re-drive = %d, want 1 (the re-drive persisted nothing)", segs.calls)
	}
	if diar.calls != 1 {
		t.Errorf("diarizer calls after re-drive = %d, want 1 (ZERO further billable LLM calls)", diar.calls)
	}
	if sel.calls != 1 {
		t.Errorf("selector calls after re-drive = %d, want 1 (ZERO further billable LLM calls)", sel.calls)
	}
	if ms.replaceCalls != 1 {
		t.Errorf("ReplaceMoments calls after re-drive = %d, want 1 (the re-drive persisted nothing)", ms.replaceCalls)
	}
	if e := repo.get(epA); e.status != "ready" || e.stage != "moments" {
		t.Errorf("state after re-drive = (%q,%q), want (ready,moments) — the skips still finalize", e.status, e.stage)
	}
}
