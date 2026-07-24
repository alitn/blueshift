package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeTrigger records the (episode, stage) it was asked to launch and can be
// scripted to fail, so a test can assert exactly which next stage auto-advance
// fired — and that a trigger failure is best-effort, never a run failure.
type fakeTrigger struct {
	mu    sync.Mutex
	calls [][2]string
	err   error
}

func (t *fakeTrigger) Trigger(_ context.Context, episodePublicID, stage string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, [2]string{episodePublicID, stage})
	return t.err
}

func (t *fakeTrigger) snapshot() [][2]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][2]string, len(t.calls))
	copy(out, t.calls)
	return out
}

// twoStageRegistry builds a fake ingest -> transcribe pipeline. ingest reports
// the given outputs (a proxy key + duration, like the real stage); transcribe is
// the terminal stage and produces nothing. ran[0]/ran[1] count how many times
// each stage's body executed.
func twoStageRegistry(ingestOut stageOutput, ran *[2]int) []stageDef {
	return []stageDef{
		{name: StageIngest, run: func(_ *Runner, _ context.Context, _ Episode, _ int) (stageOutput, error) {
			ran[0]++
			return ingestOut, nil
		}},
		{name: StageTranscribe, run: func(_ *Runner, _ context.Context, _ Episode, _ int) (stageOutput, error) {
			ran[1]++
			return stageOutput{}, nil
		}},
	}
}

// TestAutoAdvanceTriggersNextStage walks a two-stage pipeline: ingest succeeds
// and, because a next stage is registered and AutoAdvance is on, the episode is
// handed off (still 'processing', outputs recorded) and transcribe is triggered —
// never marked ready. Running the triggered transcribe stage (the terminal one)
// then marks the episode ready and fires no further trigger.
func TestAutoAdvanceTriggersNextStage(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	var ran [2]int
	tr := &fakeTrigger{}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:  Config{Retries: 2, AutoAdvance: true},
		Trigger: tr,
		stages:  twoStageRegistry(stageOutput{ProxyKey: "pk", DurationMs: 42}, &ran),
	}

	// --- ingest: non-terminal, hands off ---
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest): %v", err)
	}
	e := repo.get(epA)
	if e.status != "processing" {
		t.Errorf("after ingest status = %q, want processing (handed off, not ready)", e.status)
	}
	if e.stage != "ingest" {
		t.Errorf("after ingest current_stage = %q, want ingest (next stage's claim advances it)", e.stage)
	}
	if e.proxyKey != "pk" || e.durationMs != 42 {
		t.Errorf("ingest outputs not recorded on handoff: proxy=%q dur=%d", e.proxyKey, e.durationMs)
	}
	if calls := tr.snapshot(); len(calls) != 1 || calls[0] != [2]string{epA, "transcribe"} {
		t.Fatalf("trigger calls = %v, want exactly [[%s transcribe]]", calls, epA)
	}

	// --- transcribe: terminal, marks ready ---
	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe): %v", err)
	}
	e = repo.get(epA)
	if e.status != "ready" {
		t.Errorf("after transcribe status = %q, want ready (terminal stage)", e.status)
	}
	if e.stage != "transcribe" {
		t.Errorf("after transcribe current_stage = %q, want transcribe", e.stage)
	}
	if ran != [2]int{1, 1} {
		t.Errorf("stage bodies ran %v, want each once", ran)
	}
	// A terminal stage fires no trigger: still exactly the one transcribe launch.
	if calls := tr.snapshot(); len(calls) != 1 {
		t.Errorf("trigger fired %d times total, want 1 (terminal stage must not auto-advance)", len(calls))
	}
}

// TestAutoAdvanceDisabledRecordsHandoffButDoesNotTrigger proves the flag's off
// state: the completed non-terminal stage still records its handoff durably
// (outputs written, still 'processing'), but no next stage is launched.
func TestAutoAdvanceDisabledRecordsHandoffButDoesNotTrigger(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	var ran [2]int
	tr := &fakeTrigger{}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:  Config{Retries: 2, AutoAdvance: false},
		Trigger: tr,
		stages:  twoStageRegistry(stageOutput{ProxyKey: "pk", DurationMs: 42}, &ran),
	}

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest): %v", err)
	}
	e := repo.get(epA)
	if e.status != "processing" || e.stage != "ingest" {
		t.Errorf("handoff state = (%q,%q), want (processing,ingest)", e.status, e.stage)
	}
	if e.proxyKey != "pk" || e.durationMs != 42 {
		t.Errorf("outputs not recorded despite auto-advance off: proxy=%q dur=%d", e.proxyKey, e.durationMs)
	}
	if calls := tr.snapshot(); len(calls) != 0 {
		t.Errorf("trigger fired %v with auto-advance disabled, want none", calls)
	}
}

// TestAutoAdvanceIsLoopProof proves a stage can only trigger the NEXT stage and a
// completed entry stage cannot be re-run: after ingest hands off, a stray re-run
// of `ingest` no-ops (its entry claim needs 'uploaded'), and the trigger is only
// ever fired for transcribe — never ingest or an earlier stage.
func TestAutoAdvanceIsLoopProof(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	var ran [2]int
	tr := &fakeTrigger{}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:  Config{Retries: 2, AutoAdvance: true},
		Trigger: tr,
		stages:  twoStageRegistry(stageOutput{ProxyKey: "pk", DurationMs: 42}, &ran),
	}

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest): %v", err)
	}
	// Stray re-run of the already-completed entry stage: a clean no-op, no re-run,
	// no extra trigger.
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("re-Run(ingest): %v", err)
	}
	if ran[0] != 1 {
		t.Errorf("ingest body ran %d times, want 1 (a completed entry stage cannot re-run itself)", ran[0])
	}
	for _, c := range tr.snapshot() {
		if c[1] != "transcribe" {
			t.Errorf("trigger fired for %q, want only the next stage 'transcribe' (never itself/earlier)", c[1])
		}
	}
	if got := len(tr.snapshot()); got != 1 {
		t.Errorf("trigger fired %d times, want exactly 1", got)
	}
}

// TestAutoAdvanceTriggerFailureIsBestEffort: a trigger that fails must not fail
// the run — the handoff is already durable, so Run returns nil and the episode is
// left recoverable (still 'processing' at the completed stage), not 'failed'.
func TestAutoAdvanceTriggerFailureIsBestEffort(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	var ran [2]int
	tr := &fakeTrigger{err: errors.New("trigger boom")}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:  Config{Retries: 2, AutoAdvance: true},
		Trigger: tr,
		stages:  twoStageRegistry(stageOutput{ProxyKey: "pk", DurationMs: 42}, &ran),
	}

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest) = %v, want nil (a trigger miss is best-effort, not a run failure)", err)
	}
	if e := repo.get(epA); e.status != "processing" {
		t.Errorf("status = %q, want processing (a trigger miss must leave the episode recoverable, not failed)", e.status)
	}
	if len(tr.snapshot()) != 1 {
		t.Errorf("trigger attempted %d times, want 1", len(tr.snapshot()))
	}
}

// TestAutoAdvanceNoTriggerConfigured: a non-terminal stage with AutoAdvance on but
// no Trigger wired records the handoff and returns cleanly (logged), rather than
// panicking on a nil trigger.
func TestAutoAdvanceNoTriggerConfigured(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	var ran [2]int
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config: Config{Retries: 2, AutoAdvance: true},
		// Trigger intentionally nil.
		stages: twoStageRegistry(stageOutput{ProxyKey: "pk", DurationMs: 42}, &ran),
	}
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest) with nil trigger = %v, want nil", err)
	}
	if e := repo.get(epA); e.status != "processing" || e.proxyKey != "pk" {
		t.Errorf("handoff not recorded with nil trigger: status=%q proxy=%q", e.status, e.proxyKey)
	}
}

// TestDefaultActiveChainIsIngestOnlyTerminal locks in the reversible ingest-only
// default: with PIPELINE_STAGES unset the active chain is just [ingest], so
// ingest is terminal — its success marks the episode ready and fires no
// auto-advance trigger — even though transcribe stays registered (a valid but
// inactive stage). This is the last-known-good state the config gate restores.
func TestDefaultActiveChainIsIngestOnlyTerminal(t *testing.T) {
	if got := stageNames(defaultActiveDefs); len(defaultActiveDefs) != 1 || defaultActiveDefs[0].name != StageIngest {
		t.Fatalf("defaultActiveDefs = %v, want exactly [ingest]", got)
	}
	// Transcribe is still registered (runnable, a valid stage argument) — it is
	// only out of the *active* chain.
	if !ValidStage("transcribe") {
		t.Error("ValidStage(transcribe) = false, want true (transcribe stays registered)")
	}
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	tr := &fakeTrigger{}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:  Config{Retries: 2, AutoAdvance: true}, // on, but there is no next stage
		Trigger: tr,
		// stages nil -> default ingest-only active chain.
	}
	if !r.HasStage(StageIngest) || r.HasStage(StageTranscribe) {
		t.Errorf("default active chain HasStage(ingest)=%v HasStage(transcribe)=%v, want true/false",
			r.HasStage(StageIngest), r.HasStage(StageTranscribe))
	}
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest): %v", err)
	}
	if e := repo.get(epA); e.status != "ready" {
		t.Errorf("status = %q, want ready (ingest is terminal under the default chain)", e.status)
	}
	if len(tr.snapshot()) != 0 {
		t.Errorf("terminal ingest fired a trigger %v, want none", tr.snapshot())
	}
}

// TestActiveChainWithTranscribeAutoAdvances proves the gate opens the other way:
// SetActiveStages([ingest, transcribe]) makes ingest intermediate again, so it
// hands off (records outputs, stays 'processing') and auto-advances into
// transcribe — the multi-stage machinery preserved, driven by the real registry
// defs rather than the default.
func TestActiveChainWithTranscribeAutoAdvances(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	tr := &fakeTrigger{}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:  Config{Retries: 2, AutoAdvance: true},
		Trigger: tr,
	}
	if err := r.SetActiveStages([]Stage{StageIngest, StageTranscribe}); err != nil {
		t.Fatalf("SetActiveStages: %v", err)
	}
	if !r.HasStage(StageTranscribe) {
		t.Fatal("HasStage(transcribe) = false after activating it")
	}
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest): %v", err)
	}
	if e := repo.get(epA); e.status != "processing" || e.stage != "ingest" {
		t.Errorf("state = (%q,%q), want (processing,ingest) — ingest hands off, not terminal", e.status, e.stage)
	}
	if calls := tr.snapshot(); len(calls) != 1 || calls[0] != [2]string{epA, "transcribe"} {
		t.Errorf("trigger calls = %v, want exactly [[%s transcribe]]", calls, epA)
	}
}

// TestSetActiveStagesValidation covers the startup fail-fast: an empty list is
// the default ingest-only chain; a valid multi-stage list resolves; and a list
// that names an unregistered stage, does not start with ingest, or repeats a
// stage is rejected so cmd/worker never boots a misconfigured chain.
func TestSetActiveStagesValidation(t *testing.T) {
	ok := []struct {
		name  string
		in    []Stage
		chain []string
	}{
		{"empty -> default ingest-only", nil, []string{"ingest"}},
		{"ingest only", []Stage{StageIngest}, []string{"ingest"}},
		{"ingest then transcribe", []Stage{StageIngest, StageTranscribe}, []string{"ingest", "transcribe"}},
	}
	for _, c := range ok {
		var r Runner
		if err := r.SetActiveStages(c.in); err != nil {
			t.Errorf("%s: SetActiveStages(%v) = %v, want nil", c.name, c.in, err)
			continue
		}
		if got := stageNames(r.registry()); !equalStrings(got, c.chain) {
			t.Errorf("%s: active chain = %v, want %v", c.name, got, c.chain)
		}
	}

	bad := []struct {
		name string
		in   []Stage
	}{
		{"unregistered stage", []Stage{StageIngest, StageMoments}},
		{"must start with ingest", []Stage{StageTranscribe}},
		{"duplicate stage", []Stage{StageIngest, StageTranscribe, StageTranscribe}},
	}
	for _, c := range bad {
		var r Runner
		if err := r.SetActiveStages(c.in); err == nil {
			t.Errorf("%s: SetActiveStages(%v) = nil, want error", c.name, c.in)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stageNames(defs []stageDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = string(d.name)
	}
	return out
}
