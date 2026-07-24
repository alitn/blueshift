package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"syscall"
	"testing"
	"time"

	"blueshift/internal/config"
	"blueshift/internal/diarize"
	"blueshift/internal/llm"
	"blueshift/internal/moments"
	"blueshift/internal/pipeline"
)

// TestShutdownContextCancelsOnSIGTERM proves the worker traps SIGTERM — the stop
// signal Cloud Run sends a task ~10s before SIGKILL — and cancels the run
// context, which is what lets the pipeline tear down ffmpeg and mark the claimed
// episode failed within the grace window. If SIGTERM were not in the trapped set
// its default action would terminate this test binary, so a green run also proves
// the signal is handled, not fatal. Bounded well under a second; no long sleeps.
func TestShutdownContextCancelsOnSIGTERM(t *testing.T) {
	ctx, cancel := shutdownContext()
	defer cancel()

	if ctx.Err() != nil {
		t.Fatalf("context already cancelled before signal: %v", ctx.Err())
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("raise SIGTERM: %v", err)
	}

	select {
	case <-ctx.Done():
		if got := ctx.Err(); got != context.Canceled {
			t.Errorf("ctx.Err() = %v, want context.Canceled", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM did not cancel the shutdown context within 2s")
	}
}

// discardLogger silences the wiring-under-test.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestBuildLLMClientFakeModeNeedsNoConfig proves the demo/CI path: fake mode
// builds a working client from ZERO provider configuration — no model, no
// endpoint, no credential — replaying the calling stage's committed
// deterministic recording under the neutral label. This is what
// LLM_ENGINE_MODE=fake resolves to for `make demo`/e2e, so no live LLM can
// ever be constructed there.
func TestBuildLLMClientFakeModeNeedsNoConfig(t *testing.T) {
	cfg := config.Config{LLMMode: config.LLMModeFake, LLMEngineLabel: "bs-lm-1"}
	client, err := buildLLMClient(cfg, nil, discardLogger(), diarize.DefaultFakeGroupingResponse())
	if err != nil {
		t.Fatalf("buildLLMClient(fake): %v", err)
	}
	if client == nil {
		t.Fatal("buildLLMClient(fake) returned a nil client")
	}
	// The committed recording is the fixture the diarize fake replays; it must
	// be the well-formed grouping shape (assignments array), or the demo diarize
	// stage could never validate it.
	var out struct {
		Assignments []struct {
			SegmentIdx int    `json:"segment_idx"`
			SpeakerKey string `json:"speaker_key"`
		} `json:"assignments"`
	}
	if err := json.Unmarshal(diarize.DefaultFakeGroupingResponse(), &out); err != nil {
		t.Fatalf("committed grouping recording is not valid JSON: %v", err)
	}
	if len(out.Assignments) == 0 {
		t.Fatal("committed grouping recording has no assignments")
	}
}

// TestBuildLLMClientFakeModeMomentsFixture proves the moments stage's fake
// wiring mirrors diarize's: fake mode builds a working client around the
// committed proposal recording with zero provider configuration, and the
// recording is the well-formed proposal shape (moments array), or the demo
// moments stage could never validate it.
func TestBuildLLMClientFakeModeMomentsFixture(t *testing.T) {
	cfg := config.Config{LLMMode: config.LLMModeFake, LLMEngineLabel: "bs-lm-1"}
	client, err := buildLLMClient(cfg, nil, discardLogger(), moments.DefaultFakeSelectionResponse())
	if err != nil {
		t.Fatalf("buildLLMClient(fake, moments fixture): %v", err)
	}
	if client == nil {
		t.Fatal("buildLLMClient(fake, moments fixture) returned a nil client")
	}
	var out struct {
		Moments []struct {
			Rank        int    `json:"rank"`
			StartIdx    int    `json:"start_idx"`
			EndIdx      int    `json:"end_idx"`
			RationaleEn string `json:"rationale_en"`
			QuoteFa     string `json:"quote_fa"`
		} `json:"moments"`
	}
	if err := json.Unmarshal(moments.DefaultFakeSelectionResponse(), &out); err != nil {
		t.Fatalf("committed proposal recording is not valid JSON: %v", err)
	}
	if len(out.Moments) == 0 {
		t.Fatal("committed proposal recording has no moments")
	}
}

// TestBuildLLMClientLiveModeFailsFastOnMisconfig proves the live path mirrors
// the ASR fail-fast contract: a live worker with missing or unknown provider
// coordinates errors at wiring time — before any claim, long before any billable
// call — instead of stalling an episode mid-pipeline.
func TestBuildLLMClientLiveModeFailsFastOnMisconfig(t *testing.T) {
	cases := map[string]config.Config{
		"missing model": {
			LLMMode: config.LLMModeLive, LLMEngineLabel: "bs-lm-1",
			LLMProvider: llm.ProviderGemini, LLMEndpoint: "https://models.example.test/v1/models",
		},
		"unknown provider": {
			LLMMode: config.LLMModeLive, LLMEngineLabel: "bs-lm-1",
			LLMProvider: "nonesuch", LLMModel: "m",
		},
		"missing provider": {
			LLMMode: config.LLMModeLive, LLMEngineLabel: "bs-lm-1", LLMModel: "m",
		},
		"no endpoint and no project/region": {
			LLMMode: config.LLMModeLive, LLMEngineLabel: "bs-lm-1",
			LLMProvider: llm.ProviderGemini, LLMModel: "m",
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := buildLLMClient(cfg, nil, discardLogger(), nil); err == nil {
				t.Fatal("buildLLMClient(live, misconfigured): expected error, got nil")
			}
		})
	}
}

// TestBuildLLMClientLiveModeConstructs proves a fully-specified live config
// wires cleanly through /internal/llm's real seams — the explicit-endpoint form
// the prod deploy uses, and the project+region derivation — without any network
// or credential at construction time.
func TestBuildLLMClientLiveModeConstructs(t *testing.T) {
	cases := map[string]config.Config{
		"explicit endpoint": {
			LLMMode: config.LLMModeLive, LLMEngineLabel: "bs-lm-1",
			LLMProvider: llm.ProviderGemini, LLMModel: "some-model",
			LLMEndpoint:            "https://models.example.test/v1/models",
			LLMPriceInCentsPerMTok: 150, LLMPriceOutCentsPerMTok: 900,
		},
		"project and region": {
			LLMMode: config.LLMModeLive, LLMEngineLabel: "bs-lm-1",
			LLMProvider: llm.ProviderGemini, LLMModel: "some-model",
			LLMProject: "bs-proj", LLMRegion: "some-region",
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			// A nil fixture is fine here: live mode never touches it.
			client, err := buildLLMClient(cfg, nil, discardLogger(), nil)
			if err != nil {
				t.Fatalf("buildLLMClient(live): %v", err)
			}
			if client == nil {
				t.Fatal("buildLLMClient(live) returned a nil client")
			}
		})
	}
}

// TestStageEnginesIdentity pins the provenance engine map the worker records on
// stage_runs rows: the PUBLIC config-driven labels per stage, and the PRIVATE
// detail — the concrete model@location in live/speech mode, the literal "fake"
// in fake mode (so a replayed run is never mistaken for a provider run).
func TestStageEnginesIdentity(t *testing.T) {
	live := stageEngines(config.Config{
		MediaEngineLabel: "bs-media-1",
		ASRMode:          config.ASRModeSpeech,
		ASREngineLabel:   "bs-asr-2",
		ASRModel:         "model-x",
		ASRRegion:        "loc",
		LLMMode:          config.LLMModeLive,
		LLMEngineLabel:   "bs-lm-1",
		LLMProvider:      "prov",
		LLMModel:         "m-9",
		LLMRegion:        "global",
	})
	if e := live[pipeline.StageIngest]; e.Label != "bs-media-1" || e.Detail != "ffmpeg" {
		t.Errorf("ingest engine = %+v, want bs-media-1/ffmpeg", e)
	}
	if e := live[pipeline.StageTranscribe]; e.Label != "bs-asr-2" || e.Detail != "model-x@loc" {
		t.Errorf("transcribe engine = %+v, want bs-asr-2/model-x@loc", e)
	}
	for _, st := range []pipeline.Stage{pipeline.StageDiarize, pipeline.StageMoments} {
		if e := live[st]; e.Label != "bs-lm-1" || e.Detail != "prov/m-9@global" {
			t.Errorf("%s engine = %+v, want bs-lm-1/prov/m-9@global (one LLM identity serves both stages)", st, e)
		}
	}

	fake := stageEngines(config.Config{
		MediaEngineLabel: "bs-media-1",
		ASRMode:          config.ASRModeFake,
		ASREngineLabel:   "bs-asr-2",
		LLMMode:          config.LLMModeFake,
		LLMEngineLabel:   "bs-lm-1",
	})
	if e := fake[pipeline.StageTranscribe]; e.Detail != "fake" {
		t.Errorf("fake-mode transcribe detail = %q, want fake", e.Detail)
	}
	if e := fake[pipeline.StageDiarize]; e.Detail != "fake" {
		t.Errorf("fake-mode diarize detail = %q, want fake", e.Detail)
	}
}
