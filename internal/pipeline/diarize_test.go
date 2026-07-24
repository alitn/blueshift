package pipeline

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"blueshift/internal/asr"
)

// threeStageActive returns the [ingest, transcribe, diarize] active chain the
// diarize stage tests run under. Diarize is registered but PARKED (out of the
// default ingest-only chain), so these tests activate it explicitly; diarize is
// the chain's terminal stage, claimed as a continuation from transcribe.
func threeStageActive() []stageDef {
	return mustResolveActiveStages([]Stage{StageIngest, StageTranscribe, StageDiarize})
}

// --- diarize test doubles ----------------------------------------------------

// fakeDiarizer is a scriptable pipeline.Diarizer: it returns a preset idx ->
// speaker_key map (or an error) and records what the stage passed it, so a test
// can assert the stage wired the language, audit scope, and segments through.
type fakeDiarizer struct {
	byIdx   map[int]string
	err     error
	calls   int
	gotLang string
	gotOrg  int64
	gotEp   int64
	gotSegs []asr.Segment
}

func (f *fakeDiarizer) Diarize(_ context.Context, language string, orgID, episodeID int64, segs []asr.Segment) (map[int]string, error) {
	f.calls++
	f.gotLang, f.gotOrg, f.gotEp, f.gotSegs = language, orgID, episodeID, segs
	if f.err != nil {
		return nil, f.err
	}
	return f.byIdx, nil
}

// fakeSpeakerStore is a scriptable pipeline.SpeakerStore: it serves a preset
// SegmentSet and captures the speaker map handed to SetSegmentSpeakers.
type fakeSpeakerStore struct {
	set      SegmentSet
	found    bool
	readErr  error
	setErr   error
	setCalls int
	saved    map[int]string
}

func (f *fakeSpeakerStore) SegmentsForDiarize(_ context.Context, _, _ string) (SegmentSet, bool, error) {
	if f.readErr != nil {
		return SegmentSet{}, false, f.readErr
	}
	return f.set, f.found, nil
}

func (f *fakeSpeakerStore) SetSegmentSpeakers(_ context.Context, _, _ string, byIdx map[int]string) error {
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	f.saved = byIdx
	return nil
}

// diarizeStore builds a SpeakerStore serving two fa segments with the internal
// audit ids the stage forwards.
func diarizeStore() *fakeSpeakerStore {
	return &fakeSpeakerStore{
		found: true,
		set: SegmentSet{
			OrgID: 11, EpisodeID: 22,
			Segments: []asr.Segment{
				{Idx: 0, StartMs: 0, EndMs: 900, Text: "سلام"},
				{Idx: 1, StartMs: 1000, EndMs: 1600, Text: "خیلی خوش آمدید"},
			},
		},
	}
}

// --- tests -------------------------------------------------------------------

// TestDiarizeStageRegisteredButParked is the core acceptance: diarize is a
// registered (runnable) stage argument, yet it is NOT in the default active chain
// — the default worker stays ingest-only/terminal, unchanged. Enabling it is
// PIPELINE_STAGES' job (a separate, human-gated task).
func TestDiarizeStageRegisteredButParked(t *testing.T) {
	if !ValidStage("diarize") {
		t.Error("ValidStage(diarize) = false, want true (diarize is registered)")
	}
	// The default active chain is still exactly [ingest].
	if len(defaultActiveDefs) != 1 || defaultActiveDefs[0].name != StageIngest {
		t.Fatalf("defaultActiveDefs = %v, want exactly [ingest] (diarize parked)", stageNames(defaultActiveDefs))
	}
	// A default runner does not carry diarize in its chain.
	var r Runner
	if r.HasStage(StageDiarize) {
		t.Error("default runner HasStage(diarize) = true, want false (parked)")
	}
	// Diarize can still be activated explicitly (registered), and is then terminal.
	if err := r.SetActiveStages([]Stage{StageIngest, StageTranscribe, StageDiarize}); err != nil {
		t.Fatalf("SetActiveStages with diarize: %v", err)
	}
	if !r.HasStage(StageDiarize) {
		t.Error("HasStage(diarize) = false after activating it")
	}
}

// TestDiarizeStagePersistsSpeakers drives the stage under the [ingest, transcribe,
// diarize] chain: it reads the transcript, asks the diarizer to group it, persists
// the idx -> speaker_key map, and (as the terminal stage) marks the episode ready
// while preserving ingest's proxy + duration.
func TestDiarizeStagePersistsSpeakers(t *testing.T) {
	repo := newFakeRepo()
	// Seeded already handed off from transcribe (processing at transcribe), with
	// ingest's proxy + measured duration recorded — the state diarize claims.
	repo.addAtStage(epA, orgA, "transcribe", "fa", 90_000)

	diar := &fakeDiarizer{byIdx: map[int]string{0: "S1", 1: "S2"}}
	speakers := diarizeStore()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		Diarizer: diar,
		Speakers: speakers,
		stages:   threeStageActive(),
	}

	if err := r.Run(context.Background(), epA, "diarize"); err != nil {
		t.Fatalf("Run(diarize): %v", err)
	}

	// Terminal: ready, at diarize, with ingest's outputs preserved (COALESCE).
	e := repo.get(epA)
	if e.status != "ready" || e.stage != "diarize" {
		t.Errorf("state = (%q,%q), want (ready,diarize)", e.status, e.stage)
	}
	wantProxy := orgA + "/" + epA + "/proxies/" + proxyFilename
	if e.proxyKey != wantProxy || e.durationMs != 90_000 {
		t.Errorf("outputs = (%q,%d), want ingest's proxy %q + duration 90000 preserved", e.proxyKey, e.durationMs, wantProxy)
	}

	// The exact grouping was persisted, once.
	if speakers.setCalls != 1 {
		t.Errorf("SetSegmentSpeakers calls = %d, want 1", speakers.setCalls)
	}
	if len(speakers.saved) != 2 || speakers.saved[0] != "S1" || speakers.saved[1] != "S2" {
		t.Errorf("persisted grouping = %v, want {0:S1, 1:S2}", speakers.saved)
	}
	// The stage forwarded the language, the internal audit ids, and the segments.
	if diar.gotLang != "fa" || diar.gotOrg != 11 || diar.gotEp != 22 {
		t.Errorf("diarizer got (lang=%q, org=%d, ep=%d), want (fa, 11, 22)", diar.gotLang, diar.gotOrg, diar.gotEp)
	}
	if len(diar.gotSegs) != 2 || diar.gotSegs[0].Text != "سلام" {
		t.Errorf("diarizer got %d segments (first %q), want the 2 transcript segments", len(diar.gotSegs), textOf(diar.gotSegs))
	}
}

// TestDiarizeStageFailsNeutralOnDiarizerError: a persistent diarizer failure
// (e.g. the llm one-retry-then-fail exhausted upstream) exhausts the stage's
// attempts and marks the episode failed with a NEUTRAL error_id — no cause text
// leaks — and nothing is persisted.
func TestDiarizeStageFailsNeutralOnDiarizerError(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "transcribe", "fa", 5000)
	diar := &fakeDiarizer{err: errors.New("model output failed validation: gemini said no")}
	speakers := diarizeStore()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		Diarizer: diar,
		Speakers: speakers,
		stages:   threeStageActive(),
	}

	err := r.Run(context.Background(), epA, "diarize")
	if !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed", err)
	}
	e := repo.get(epA)
	if e.status != "failed" || e.stage != "diarize" {
		t.Errorf("state = (%q,%q), want (failed,diarize)", e.status, e.stage)
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
	// Every stage attempt re-ran the diarizer; nothing was persisted on failure.
	if diar.calls != 3 {
		t.Errorf("diarizer calls = %d, want 3 (1 + 2 retries)", diar.calls)
	}
	if speakers.setCalls != 0 {
		t.Errorf("SetSegmentSpeakers calls = %d, want 0 on a failed run", speakers.setCalls)
	}
}

// TestDiarizeStageIdempotentRedrive proves a re-driven diarize stage persists the
// SAME grouping (idempotent): after the first ready, re-seeding at transcribe and
// re-running writes the identical map again with no error.
func TestDiarizeStageIdempotentRedrive(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "transcribe", "fa", 90_000)
	diar := &fakeDiarizer{byIdx: map[int]string{0: "S1", 1: "S1"}}
	speakers := diarizeStore()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		Diarizer: diar,
		Speakers: speakers,
		stages:   threeStageActive(),
	}

	if err := r.Run(context.Background(), epA, "diarize"); err != nil {
		t.Fatalf("Run(diarize) #1: %v", err)
	}
	first := speakers.saved
	// Re-seed the episode at transcribe and re-run the stage (a re-drive).
	repo.addAtStage(epA, orgA, "transcribe", "fa", 90_000)
	if err := r.Run(context.Background(), epA, "diarize"); err != nil {
		t.Fatalf("Run(diarize) #2: %v", err)
	}
	if speakers.setCalls != 2 {
		t.Errorf("SetSegmentSpeakers calls = %d, want 2 (one per drive)", speakers.setCalls)
	}
	if first[0] != "S1" || first[1] != "S1" || speakers.saved[0] != "S1" || speakers.saved[1] != "S1" {
		t.Errorf("re-drive produced a different grouping: first=%v second=%v", first, speakers.saved)
	}
}

// TestDiarizeStageNoSegmentsFails proves the stage refuses to run when the episode
// has no transcript (an out-of-order run) rather than diarizing nothing.
func TestDiarizeStageNoSegmentsFails(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "transcribe", "fa", 5000)
	diar := &fakeDiarizer{byIdx: map[int]string{}}
	// found=false: the episode resolves to no transcript.
	speakers := &fakeSpeakerStore{found: false}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 0},
		Diarizer: diar,
		Speakers: speakers,
		stages:   threeStageActive(),
	}
	if err := r.Run(context.Background(), epA, "diarize"); !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed (no segments)", err)
	}
	if diar.calls != 0 {
		t.Errorf("diarizer calls = %d, want 0 (no segments to diarize)", diar.calls)
	}
}

// TestDiarizeStageNilSeamsFails proves a diarize stage wired without its seams
// fails cleanly (rather than panicking), matching the transcribe stage's nil-seam
// contract.
func TestDiarizeStageNilSeamsFails(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "transcribe", "fa", 5000)
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config: Config{Retries: 0},
		// Diarizer and Speakers left nil.
		stages: threeStageActive(),
	}
	if err := r.Run(context.Background(), epA, "diarize"); !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed (nil seams)", err)
	}
	if e := repo.get(epA); e.status != "failed" {
		t.Errorf("status = %q, want failed", e.status)
	}
}

// textOf is a tiny helper for a diagnostic message.
func textOf(segs []asr.Segment) string {
	if len(segs) == 0 {
		return ""
	}
	return segs[0].Text
}
