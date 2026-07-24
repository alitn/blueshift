package diarize

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"blueshift/internal/asr"
	"blueshift/internal/llm"
)

// TestDefaultFakeGroupingMatchesDemoTranscript is the pairing proof between the
// two committed offline fixtures: the embedded ASR fa recording (what the demo
// transcribe stage persists) and the embedded grouping recording (what the demo
// diarize stage replays). It reproduces the demo transcript exactly as the
// transcribe stage does — default fake engine, then the deterministic
// resegmentation with code defaults — and replays the grouping through the REAL
// llm.Client validate/retry loop and the REAL Engine.Diarize validation
// (every idx assigned exactly once). If either fixture drifts (a new ASR turn, a
// renumbered idx, a malformed key), Diarize hard-fails here, long before demo/e2e.
func TestDefaultFakeGroupingMatchesDemoTranscript(t *testing.T) {
	segs := demoTranscriptSegments(t)

	fe := llm.NewFakeEngine("bs-lm-1", "bs-lm-fake", DefaultFakeGroupingResponse())
	client, err := llm.NewFakeClient(nil, fe)
	if err != nil {
		t.Fatalf("NewFakeClient: %v", err)
	}
	eng := Engine{Gen: client, Labels: LangLabelResolver{Label: "bs-lm-1"}}

	byIdx, err := eng.Diarize(context.Background(), "fa", 1, 1, segs)
	if err != nil {
		t.Fatalf("Diarize(demo transcript): %v — the committed grouping fixture no longer matches the committed ASR fixture", err)
	}
	if len(byIdx) != len(segs) {
		t.Fatalf("grouping covers %d segments, want %d", len(byIdx), len(segs))
	}

	// The demo sample is a host + guest opening: the grouping must resolve at
	// least two distinct speakers or the speaker chips the demo exists to show
	// would all collapse into one.
	distinct := map[string]bool{}
	for _, k := range byIdx {
		distinct[k] = true
	}
	if len(distinct) < 2 {
		t.Errorf("grouping resolves %d distinct speakers %v, want >= 2 (host + guest)", len(distinct), byIdx)
	}
	if byIdx[0] != "S1" || byIdx[1] != "S2" {
		t.Errorf("grouping = %v, want {0:S1 1:S2} (host first, guest second — the committed recording)", byIdx)
	}
}

// TestDefaultFakeGroupingIsDeterministic proves the fake path is a pure
// replayer: two full Diarize runs over the same transcript produce identical
// groupings, and the accessor hands out equal-but-independent copies of the
// recording (a caller mutating one copy can never corrupt another run).
func TestDefaultFakeGroupingIsDeterministic(t *testing.T) {
	a, b := DefaultFakeGroupingResponse(), DefaultFakeGroupingResponse()
	if !bytes.Equal(a, b) {
		t.Fatal("DefaultFakeGroupingResponse returned different bytes across calls")
	}
	a[0] = 'X'
	if bytes.Equal(a, b) {
		t.Fatal("mutating one returned copy affected another (shared backing array)")
	}

	segs := demoTranscriptSegments(t)
	run := func() map[int]string {
		fe := llm.NewFakeEngine("bs-lm-1", "bs-lm-fake", DefaultFakeGroupingResponse())
		client, err := llm.NewFakeClient(nil, fe)
		if err != nil {
			t.Fatalf("NewFakeClient: %v", err)
		}
		eng := Engine{Gen: client, Labels: LangLabelResolver{Label: "bs-lm-1"}}
		byIdx, err := eng.Diarize(context.Background(), "fa", 1, 1, segs)
		if err != nil {
			t.Fatalf("Diarize: %v", err)
		}
		return byIdx
	}
	if first, second := run(), run(); !reflect.DeepEqual(first, second) {
		t.Errorf("groupings differ across runs: %v vs %v", first, second)
	}
}

// demoTranscriptSegments reproduces the transcript the demo sample carries: the
// embedded offline fa recording resolved by language fallback (exactly what the
// fake transcribe stage serves for any demo audio key), passed through the same
// deterministic resegmentation the transcribe stage applies with the code
// defaults.
func demoTranscriptSegments(t *testing.T) []asr.Segment {
	t.Helper()
	engine, err := asr.NewDefaultFakeEngine("bs-asr-1")
	if err != nil {
		t.Fatalf("NewDefaultFakeEngine: %v", err)
	}
	tr, err := engine.Transcribe(context.Background(), asr.TranscribeRequest{
		AudioKey: "org_demo/ep_demo/proxies/audio.flac",
		Language: "fa",
	})
	if err != nil {
		t.Fatalf("Transcribe(demo fa): %v", err)
	}
	segmented := asr.Resegment(tr, asr.ResegmentOptions{})
	if len(segmented.Segments) == 0 {
		t.Fatal("demo transcript has no segments")
	}
	return segmented.Segments
}
