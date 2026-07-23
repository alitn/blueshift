//go:build live

package asr

// speech_live_test.go is the nightly LIVE smoke for the Speech-v2-backed engine.
// It is guarded two ways so it never runs in `make check`/CI and never makes a
// real API call by accident:
//
//   - Build tag `live`: this file is compiled ONLY under `go test -tags live`.
//     The default `make check` build excludes it entirely (the record/replay
//     tests in speech_test.go are the CI coverage).
//   - Env gate `ASR_LIVE_SMOKE`: even under -tags live the test self-skips unless
//     ASR_LIVE_SMOKE is truthy, so a developer running `go test -tags live ./...`
//     without live config gets a clean skip, not a spurious failure or a billed
//     call. The nightly sets ASR_LIVE_SMOKE=1 plus the coordinates below.
//
// Required env when the gate is on (a misconfigured nightly fails loudly rather
// than silently skipping): ASR_SMOKE_PROJECT, ASR_SMOKE_REGION, ASR_SMOKE_MODEL,
// ASR_SMOKE_BUCKET, ASR_SMOKE_AUDIO_KEY, ASR_SMOKE_LANGUAGE. Credentials come from
// ADC (Token nil → adc.go); the caller's service agent must be able to read the
// audio object (see docs/RUNBOOK.md). See docs/RUNBOOK.md for the full var list.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func liveEnv(t *testing.T, key string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		t.Fatalf("live smoke: %s is required when ASR_LIVE_SMOKE is set", key)
	}
	return v
}

// TestSpeechLiveSmoke transcribes one real audio object through the real provider
// and asserts the boundary contract holds end-to-end: a validated, non-empty,
// vendor-neutral Transcript with monotonic word timing. It is the drift detector
// for the nightly, not part of the offline suite.
func TestSpeechLiveSmoke(t *testing.T) {
	if !truthy(os.Getenv("ASR_LIVE_SMOKE")) {
		t.Skip("live smoke disabled (set ASR_LIVE_SMOKE=1 with the ASR_SMOKE_* coordinates to run)")
	}

	cfg := SpeechConfig{
		Label:             "bs-asr-1",
		Model:             liveEnv(t, "ASR_SMOKE_MODEL"),
		Region:            liveEnv(t, "ASR_SMOKE_REGION"),
		Project:           liveEnv(t, "ASR_SMOKE_PROJECT"),
		Bucket:            liveEnv(t, "ASR_SMOKE_BUCKET"),
		LanguageCodes:     map[string]string{},
		AdaptationEnabled: false,
		// Token nil → Application Default Credentials (adc.go).
	}
	audioKey := liveEnv(t, "ASR_SMOKE_AUDIO_KEY")
	language := liveEnv(t, "ASR_SMOKE_LANGUAGE")

	e, err := NewSpeechEngine(cfg)
	if err != nil {
		t.Fatalf("NewSpeechEngine: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	tr, err := e.Transcribe(ctx, TranscribeRequest{AudioKey: audioKey, Language: language})
	if err != nil {
		t.Fatalf("live Transcribe: %v", err)
	}

	if err := tr.Validate(); err != nil {
		t.Fatalf("live transcript failed Validate: %v", err)
	}
	if len(tr.Segments) == 0 {
		t.Fatal("live transcript has no segments")
	}
	words := 0
	for _, s := range tr.Segments {
		words += len(s.Words)
	}
	if words == 0 {
		t.Fatal("live transcript has no words (word timing missing)")
	}
	if tr.Engine != cfg.Label {
		t.Errorf("Engine = %q, want the neutral label %q", tr.Engine, cfg.Label)
	}
	if tr.Language != language {
		t.Errorf("Language = %q, want the echoed request tag %q", tr.Language, language)
	}
	// The caller-visible payload must never name a provider, even live.
	assertNoLeak(t, "live engine label", tr.Engine)
	for _, s := range tr.Segments {
		assertNoLeak(t, "live segment text", s.Text)
	}
	t.Logf("live smoke OK: %d segments, %d words", len(tr.Segments), words)
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
