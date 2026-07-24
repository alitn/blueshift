package pipeline

import (
	"context"
	"testing"

	"blueshift/internal/asr"
)

// TestReprocessSkipFastForward is the cost-safety guarantee behind the
// POST /api/episodes/{id}/reprocess endpoint (task m1-reprocess-api): re-entering
// a COMPLETED episode is safe and cheap because every billable stage SKIPS when
// its output already exists, so only the stages whose outputs are MISSING run and
// bill.
//
// The scenario is exactly the state reprocess produces: an episode reset to
// 'uploaded' with current_stage cleared, that ALREADY has segments + speakers
// but NO moments (a pre-moments READY row a newer chain now backfills). One
// Run(ingest) drives the whole [ingest, transcribe, diarize, moments] chain in
// fake mode (auto-advance via runningTrigger, the same effect the exec trigger
// achieves in make demo/e2e). The proof:
//   - ingest RUNS (free remux/probe — no metered engine),
//   - transcribe SKIPS (segments exist) — its billable ASR engine is called ZERO
//     times,
//   - diarize SKIPS (speakers assigned) — its billable LLM diarizer is called ZERO
//     times,
//   - moments RUNS (no moments yet) — its selector is called exactly once,
//   - the episode re-finalizes 'ready' having consumed exactly ONE billable
//     attempt (moments) — the transcribe/diarize skips never touched the counter.
func TestReprocessSkipFastForward(t *testing.T) {
	repo := newFakeRepo()
	// The exact post-reprocess shape: 'uploaded', no current_stage, unclaimed.
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")

	// Billable transcribe engine behind a counter: it must NEVER be called, because
	// the transcript already exists.
	asrEngine := &countingEngine{label: "bs-asr-1", tr: oneSegmentTranscript()}
	segs := newFakeSegments()
	segs.seed(epA, []asr.Segment{ // already transcribed -> HasSegments true -> skip
		{Idx: 0, StartMs: 0, EndMs: 900, Text: "سلام", Words: []asr.Word{{Text: "سلام", StartMs: 0, EndMs: 900, Conf: 0.9}}},
		{Idx: 1, StartMs: 1000, EndMs: 1600, Text: "خوش آمدید", Words: []asr.Word{{Text: "خوش", StartMs: 1000, EndMs: 1300, Conf: 0.9}, {Text: "آمدید", StartMs: 1350, EndMs: 1600, Conf: 0.9}}},
	})

	// Billable diarizer behind a counter: must NEVER be called (speakers assigned).
	diar := &fakeDiarizer{byIdx: map[int]string{0: "S1", 1: "S2"}}
	speakers := diarizeStore()
	speakers.assigned = true // already diarized -> SpeakersAssigned true -> skip

	// Moments are MISSING, so the selector MUST run exactly once.
	sel := &fakeSelector{props: demoProposals()}
	moments := momentsStore() // found=true, two diarized segments with word timings
	moments.exists = false    // MomentsExist false -> the stage runs

	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2, AutoAdvance: true},
		ASR:      fakeASR{engine: asrEngine},
		Segments: segs,
		Diarizer: diar,
		Speakers: speakers,
		Selector: sel,
		Moments:  moments,
	}
	if err := r.SetActiveStages([]Stage{StageIngest, StageTranscribe, StageDiarize, StageMoments}); err != nil {
		t.Fatalf("SetActiveStages: %v", err)
	}
	tr := &runningTrigger{r: r}
	r.Trigger = tr

	// One Run(ingest) fast-forwards the whole chain, exactly like the ingest trigger
	// the reprocess endpoint fires on a freshly-reset 'uploaded' episode.
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest): %v", err)
	}

	e := repo.get(epA)
	t.Logf("[reprocess fast-forward] chain=%v", tr.snapshot())
	t.Logf("[reprocess fast-forward] final status=%q stage=%q", e.status, e.stage)
	t.Logf("[reprocess fast-forward] billable calls: transcribe(ASR)=%d diarize(LLM)=%d moments(selector)=%d",
		asrEngine.callCount(), diar.calls, sel.calls)
	t.Logf("[reprocess fast-forward] process_attempts=%d (only the moments stage billed)", e.processAttempts)

	// The chain reached the terminal moments stage and finalized 'ready'.
	if e.status != "ready" || e.stage != "moments" {
		t.Fatalf("after fast-forward: state = (%q,%q), want (ready,moments)", e.status, e.stage)
	}
	// Auto-advance fired for exactly the three downstream stages, in order.
	if calls := tr.snapshot(); len(calls) != 3 ||
		calls[0] != [2]string{epA, "transcribe"} ||
		calls[1] != [2]string{epA, "diarize"} ||
		calls[2] != [2]string{epA, "moments"} {
		t.Fatalf("auto-advance fired %v, want [[%s transcribe] [%s diarize] [%s moments]]", calls, epA, epA, epA)
	}

	// COST SAFETY — the whole point of reprocess being safe & cheap:
	if got := asrEngine.callCount(); got != 0 {
		t.Errorf("transcribe ASR engine called %d times, want 0 (segments already existed — bill ZERO)", got)
	}
	if diar.calls != 0 {
		t.Errorf("diarize LLM called %d times, want 0 (speakers already assigned — bill ZERO)", diar.calls)
	}
	if segs.calls != 0 {
		t.Errorf("ReplaceSegments called %d times, want 0 (transcribe skipped, persisted nothing)", segs.calls)
	}
	if speakers.setCalls != 0 {
		t.Errorf("SetSegmentSpeakers called %d times, want 0 (diarize skipped, persisted nothing)", speakers.setCalls)
	}

	// The one MISSING output was produced: moments ran and persisted its proposals.
	if sel.calls != 1 {
		t.Errorf("moments selector called %d times, want 1 (moments were missing — this stage runs)", sel.calls)
	}
	if moments.replaceCalls != 1 || len(moments.saved) != len(demoProposals()) {
		t.Errorf("ReplaceMoments calls=%d saved=%d, want 1 and %d", moments.replaceCalls, len(moments.saved), len(demoProposals()))
	}

	// Exactly ONE billable attempt across the whole re-entry — the moments call.
	// The transcribe/diarize skips happen BEFORE BeginBillableAttempt, so they never
	// touched the per-episode cap counter.
	if e.processAttempts != 1 {
		t.Errorf("process_attempts = %d, want 1 (only the moments stage billed; the two skips bill zero)", e.processAttempts)
	}
}
