package moments

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"blueshift/internal/asr"
	"blueshift/internal/diarize"
	"blueshift/internal/llm"
	"blueshift/internal/pipeline"
)

// TestDefaultFakeSelectionMatchesDemoTranscript is the pairing proof between
// the committed offline fixtures: the embedded ASR fa recording (what the demo
// transcribe stage persists), the embedded diarize grouping (the speaker keys
// the demo moments stage reads), and the embedded proposal recording (what the
// demo moments stage replays). It reproduces the demo transcript exactly as
// the transcribe stage does — default fake engine, then the deterministic
// resegmentation with code defaults — decorates it with the committed speaker
// grouping, and replays the proposals through the REAL llm.Client
// validate/retry loop and the REAL Engine validation (count window, contiguous
// ranks, non-overlapping spans, VERBATIM quotes). If any fixture drifts (a new
// ASR turn, a renumbered idx, a paraphrased quote), SelectMoments hard-fails
// here, long before demo/e2e.
func TestDefaultFakeSelectionMatchesDemoTranscript(t *testing.T) {
	segs := demoMomentSegments(t)

	fe := llm.NewFakeEngine("bs-lm-1", "bs-lm-fake", DefaultFakeSelectionResponse())
	client, err := llm.NewFakeClient(nil, fe)
	if err != nil {
		t.Fatalf("NewFakeClient: %v", err)
	}
	eng := Engine{Gen: client, Labels: LangLabelResolver{Label: "bs-lm-1"}}

	props, err := eng.SelectMoments(context.Background(), "fa", 1, 1, segs)
	if err != nil {
		t.Fatalf("SelectMoments(demo transcript): %v — the committed proposal fixture no longer matches the committed ASR fixture", err)
	}
	// The demo sample is two segments, so the clamped window admits exactly the
	// two committed single-segment moments: the guest reply ranked first, the
	// host greeting second.
	if len(props) != 2 {
		t.Fatalf("proposals = %d, want 2 (the committed recording)", len(props))
	}
	if props[0].Rank != 1 || props[0].StartIdx != 1 || props[0].EndIdx != 1 {
		t.Errorf("rank 1 = %+v, want the guest reply span 1..1", props[0])
	}
	if props[1].Rank != 2 || props[1].StartIdx != 0 || props[1].EndIdx != 0 {
		t.Errorf("rank 2 = %+v, want the host greeting span 0..0", props[1])
	}
	// Belt: re-assert the verbatim property against the live transcript text
	// (the engine already validated it — this pins the pairing explicitly).
	for _, p := range props {
		if !strings.Contains(segs[p.StartIdx].Text, p.QuoteFa) {
			t.Errorf("rank %d quote %q is not a substring of segment %d text %q", p.Rank, p.QuoteFa, p.StartIdx, segs[p.StartIdx].Text)
		}
	}
}

// TestDefaultFakeSelectionIsDeterministic proves the fake path is a pure
// replayer: two full SelectMoments runs over the same transcript produce
// identical proposals, and the accessor hands out equal-but-independent copies
// of the recording (a caller mutating one copy can never corrupt another run).
func TestDefaultFakeSelectionIsDeterministic(t *testing.T) {
	a, b := DefaultFakeSelectionResponse(), DefaultFakeSelectionResponse()
	if !bytes.Equal(a, b) {
		t.Fatal("DefaultFakeSelectionResponse returned different bytes across calls")
	}
	a[0] = 'X'
	if bytes.Equal(a, b) {
		t.Fatal("mutating one returned copy affected another (shared backing array)")
	}

	segs := demoMomentSegments(t)
	run := func() []pipeline.ProposedMoment {
		fe := llm.NewFakeEngine("bs-lm-1", "bs-lm-fake", DefaultFakeSelectionResponse())
		client, err := llm.NewFakeClient(nil, fe)
		if err != nil {
			t.Fatalf("NewFakeClient: %v", err)
		}
		eng := Engine{Gen: client, Labels: LangLabelResolver{Label: "bs-lm-1"}}
		props, err := eng.SelectMoments(context.Background(), "fa", 1, 1, segs)
		if err != nil {
			t.Fatalf("SelectMoments: %v", err)
		}
		return props
	}
	if first, second := run(), run(); !reflect.DeepEqual(first, second) {
		t.Errorf("proposals differ across runs: %v vs %v", first, second)
	}
}

// demoMomentSegments reproduces the transcript the demo sample carries — the
// embedded offline fa recording resolved by language fallback, passed through
// the same deterministic resegmentation the transcribe stage applies with the
// code defaults — decorated with the committed diarize grouping (the demo
// moments stage runs after diarize, so its input segments carry speaker keys).
func demoMomentSegments(t *testing.T) []pipeline.MomentSegment {
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

	var grouping struct {
		Assignments []struct {
			SegmentIdx int    `json:"segment_idx"`
			SpeakerKey string `json:"speaker_key"`
		} `json:"assignments"`
	}
	if err := json.Unmarshal(diarize.DefaultFakeGroupingResponse(), &grouping); err != nil {
		t.Fatalf("committed grouping recording is not valid JSON: %v", err)
	}
	keyByIdx := make(map[int]string, len(grouping.Assignments))
	for _, a := range grouping.Assignments {
		keyByIdx[a.SegmentIdx] = a.SpeakerKey
	}

	out := make([]pipeline.MomentSegment, 0, len(segmented.Segments))
	for _, s := range segmented.Segments {
		out = append(out, pipeline.MomentSegment{Segment: s, SpeakerKey: keyByIdx[s.Idx]})
	}
	return out
}
