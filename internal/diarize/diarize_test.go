package diarize

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"blueshift/internal/asr"
	"blueshift/internal/llm"
	"blueshift/internal/pipeline"

	// Register Persian so LangLabelResolver resolves its llm slot from the real
	// registry (import for the side effect).
	_ "blueshift/internal/lang/fa"
)

// Engine is the production diarizer the pipeline drives through the neutral
// pipeline.Diarizer seam — guard the contract at compile time.
var _ pipeline.Diarizer = Engine{}

// forbidden mirrors the vendor-leak name list: no returned error may name a
// provider even though internal error strings may.
var forbidden = []string{
	"chirp", "gemini", "vertex", "google", "speech-to-text",
	"anthropic", "claude", "elevenlabs", "openai", "whisper", "deepgram",
}

func assertNeutralErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	low := strings.ToLower(err.Error())
	for _, name := range forbidden {
		if strings.Contains(low, name) {
			t.Errorf("error %q leaks provider name %q", err.Error(), name)
		}
	}
}

// captureAuditor records the llm_calls rows a run would write, so a test can
// assert the audit without a database.
type captureAuditor struct{ rows []llm.CallRecord }

func (c *captureAuditor) RecordLLMCall(_ context.Context, rec llm.CallRecord) error {
	c.rows = append(c.rows, rec)
	return nil
}

// zwnj is the U+200C zero-width non-joiner; the diarize request must carry the
// segment text byte-for-byte (it anchors to the exact text).
const zwnj = "\u200c"

// fixtureSegments is a small fa transcript with word timings and a ZWNJ, used to
// prove the request carries text (with the ZWNJ) but none of the timings/words.
func fixtureSegments() []asr.Segment {
	return []asr.Segment{
		{Idx: 0, StartMs: 0, EndMs: 2200, Text: "سلام به برنامه خوش آمدید", Words: []asr.Word{
			{Text: "سلام", StartMs: 0, EndMs: 520, Conf: 0.98},
			{Text: "آمدید", StartMs: 1660, EndMs: 2200, Conf: 0.94},
		}},
		{Idx: 1, StartMs: 2600, EndMs: 4600, Text: "خیلی خوش" + zwnj + "حالم که اینجا هستم", Words: []asr.Word{
			{Text: "خیلی", StartMs: 2600, EndMs: 2900, Conf: 0.96},
			{Text: "هستم", StartMs: 4240, EndMs: 4600, Conf: 0.95},
		}},
		{Idx: 2, StartMs: 4700, EndMs: 6000, Text: "از حضور شما سپاسگزارم", Words: []asr.Word{
			{Text: "سپاسگزارم", StartMs: 5400, EndMs: 6000, Conf: 0.93},
		}},
	}
}

// newEngine wires a diarize Engine around a fake-backed llm.Client returning the
// given recorded output, plus the real fa label resolver. It returns the engine,
// the fake engine (to inspect what was sent), and the auditor (to inspect rows).
func newEngine(t *testing.T, output string) (Engine, *llm.FakeEngine, *captureAuditor) {
	t.Helper()
	fe := llm.NewFakeEngine("bs-lm-1", "bs-lm-test", []byte(output), llm.WithFakeUsage(1200, 40))
	aud := &captureAuditor{}
	client, err := llm.NewFakeClient(aud, fe)
	if err != nil {
		t.Fatalf("NewFakeClient: %v", err)
	}
	return Engine{Gen: client, Labels: LangLabelResolver{Label: "bs-lm-1"}}, fe, aud
}

// TestDiarizeGroupsSpeakers: a valid recorded grouping decodes to the exact idx ->
// speaker_key map, and the single call is audited 'ok' with the right scope.
func TestDiarizeGroupsSpeakers(t *testing.T) {
	// Two-speaker interview: host (S1) opens, guest (S2) replies, host (S1) closes.
	out := `{"assignments":[{"segment_idx":0,"speaker_key":"S1"},{"segment_idx":1,"speaker_key":"S2"},{"segment_idx":2,"speaker_key":"S1"}]}`
	eng, fe, aud := newEngine(t, out)

	got, err := eng.Diarize(context.Background(), "fa", 7, 42, fixtureSegments())
	if err != nil {
		t.Fatalf("Diarize: %v", err)
	}
	want := map[int]string{0: "S1", 1: "S2", 2: "S1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("grouping = %v, want %v", got, want)
	}
	if len(fe.Calls()) != 1 {
		t.Errorf("engine calls = %d, want 1 (no retry on a valid grouping)", len(fe.Calls()))
	}
	if len(aud.rows) != 1 || aud.rows[0].Status != "ok" {
		t.Fatalf("audit rows = %+v, want one ok row", aud.rows)
	}
	if aud.rows[0].Model != "bs-lm-test" || aud.rows[0].PromptVersion != "v1" {
		t.Errorf("audit model/version = %q/%q", aud.rows[0].Model, aud.rows[0].PromptVersion)
	}
	if aud.rows[0].OrgID != 7 || aud.rows[0].EpisodeID != 42 {
		t.Errorf("audit scope = org %d ep %d, want org 7 ep 42", aud.rows[0].OrgID, aud.rows[0].EpisodeID)
	}
}

// TestDiarizeRequestIsTextAnchored proves the model request carries ONLY {idx,
// text} — no timestamp field, no words array, and none of the segment timing
// VALUES — while preserving the exact text (including the U+200C ZWNJ). This is
// the text-anchoring invariant asserted on what actually crossed the seam.
func TestDiarizeRequestIsTextAnchored(t *testing.T) {
	out := `{"assignments":[{"segment_idx":0,"speaker_key":"S1"},{"segment_idx":1,"speaker_key":"S1"},{"segment_idx":2,"speaker_key":"S1"}]}`
	eng, fe, _ := newEngine(t, out)

	if _, err := eng.Diarize(context.Background(), "fa", 1, 1, fixtureSegments()); err != nil {
		t.Fatalf("Diarize: %v", err)
	}
	calls := fe.Calls()
	if len(calls) != 1 || len(calls[0].Parts) != 1 {
		t.Fatalf("calls = %+v, want one call with one part", calls)
	}
	sent := calls[0].Parts[0]

	// No timing field names may appear in what was sent.
	for _, banned := range []string{"start_ms", "end_ms", "words", "conf"} {
		if strings.Contains(sent, banned) {
			t.Errorf("request carried timing field %q (not text-anchored): %s", banned, sent)
		}
	}
	// No segment timing VALUE may appear either (belt: a timestamp smuggled as a
	// bare number). 520, 1660, 2600, 4600 … are timings from the fixture.
	for _, ts := range []string{"2200", "2600", "4600", "1660", "5400"} {
		if strings.Contains(sent, ts) {
			t.Errorf("request carried a timing value %q (not text-anchored): %s", ts, sent)
		}
	}
	// The exact text IS present, ZWNJ and all (the model anchors to it).
	if !strings.Contains(sent, "خیلی خوش"+zwnj+"حالم") {
		t.Errorf("request lost the verbatim segment text (with ZWNJ): %s", sent)
	}
	// Structurally: the decoded payload is exactly idx+text per segment.
	var payload struct {
		Segments []map[string]json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal([]byte(sent), &payload); err != nil {
		t.Fatalf("request payload is not the expected shape: %v", err)
	}
	for i, seg := range payload.Segments {
		if len(seg) != 2 {
			t.Errorf("segment %d sent %d fields, want exactly 2 (idx, text)", i, len(seg))
		}
		if _, ok := seg["idx"]; !ok {
			t.Errorf("segment %d missing idx", i)
		}
		if _, ok := seg["text"]; !ok {
			t.Errorf("segment %d missing text", i)
		}
	}
}

// TestDiarizeLeavesSegmentsUntouched proves the diarizer never mutates the input
// transcript (verbatim invariant at the boundary): text, words, and timings of
// the segments passed in are identical afterwards, and the result is only labels.
func TestDiarizeLeavesSegmentsUntouched(t *testing.T) {
	out := `{"assignments":[{"segment_idx":0,"speaker_key":"S1"},{"segment_idx":1,"speaker_key":"S2"},{"segment_idx":2,"speaker_key":"S2"}]}`
	eng, _, _ := newEngine(t, out)

	segs := fixtureSegments()
	before := fixtureSegments() // an independent identical copy
	if _, err := eng.Diarize(context.Background(), "fa", 1, 1, segs); err != nil {
		t.Fatalf("Diarize: %v", err)
	}
	if !reflect.DeepEqual(segs, before) {
		t.Errorf("Diarize mutated the input segments:\n got %+v\nwant %+v", segs, before)
	}
}

// TestDiarizeInvalidOutputRetriesThenFails: an output that decodes but fails the
// grouping validation (unknown idx / gap / overlap / malformed label) drives the
// /internal/llm one-retry-then-hard-fail path. Two 'invalid' audit rows are
// written and the returned error is the neutral ErrInvalidOutput.
func TestDiarizeInvalidOutputRetriesThenFails(t *testing.T) {
	cases := map[string]string{
		// idx 2 unknown-free but idx 5 does not exist (fixture is 0,1,2).
		"unknown idx": `{"assignments":[{"segment_idx":0,"speaker_key":"S1"},{"segment_idx":1,"speaker_key":"S1"},{"segment_idx":5,"speaker_key":"S2"}]}`,
		// idx 2 omitted -> a gap.
		"gap": `{"assignments":[{"segment_idx":0,"speaker_key":"S1"},{"segment_idx":1,"speaker_key":"S1"}]}`,
		// idx 0 assigned twice -> an overlap.
		"overlap": `{"assignments":[{"segment_idx":0,"speaker_key":"S1"},{"segment_idx":0,"speaker_key":"S2"},{"segment_idx":1,"speaker_key":"S1"},{"segment_idx":2,"speaker_key":"S1"}]}`,
		// a label that is not S<n>.
		"malformed label": `{"assignments":[{"segment_idx":0,"speaker_key":"HOST"},{"segment_idx":1,"speaker_key":"S1"},{"segment_idx":2,"speaker_key":"S1"}]}`,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			eng, fe, aud := newEngine(t, out)

			got, err := eng.Diarize(context.Background(), "fa", 3, 9, fixtureSegments())
			if !errors.Is(err, llm.ErrInvalidOutput) {
				t.Fatalf("err = %v, want ErrInvalidOutput", err)
			}
			if got != nil {
				t.Errorf("grouping = %v, want nil on failure", got)
			}
			assertNeutralErr(t, err)
			if len(fe.Calls()) != 2 {
				t.Errorf("engine calls = %d, want exactly 2 (one retry)", len(fe.Calls()))
			}
			if len(aud.rows) != 2 {
				t.Fatalf("audit rows = %d, want 2", len(aud.rows))
			}
			for i, r := range aud.rows {
				if r.Status != "invalid" {
					t.Errorf("audit row %d status = %q, want invalid", i, r.Status)
				}
			}
		})
	}
}

// TestDiarizeUnknownLanguageErrors: an unregistered language is an explicit error
// (never a silent default), and no LLM call is made.
func TestDiarizeUnknownLanguageErrors(t *testing.T) {
	eng, fe, aud := newEngine(t, `{"assignments":[]}`)
	if _, err := eng.Diarize(context.Background(), "zz", 1, 1, fixtureSegments()); err == nil {
		t.Fatal("Diarize(zz) = nil error, want unknown-language error")
	}
	if len(fe.Calls()) != 0 {
		t.Errorf("engine calls = %d, want 0 (label resolution failed first)", len(fe.Calls()))
	}
	if len(aud.rows) != 0 {
		t.Errorf("audit rows = %d, want 0 (no call made)", len(aud.rows))
	}
}

// TestBuildRequestOnlyIdxAndText is a direct unit check that the request builder
// serializes only idx+text, in idx order, regardless of input order.
func TestBuildRequestOnlyIdxAndText(t *testing.T) {
	segs := []asr.Segment{
		{Idx: 2, StartMs: 100, EndMs: 200, Text: "c", Words: []asr.Word{{Text: "c", StartMs: 100, EndMs: 200, Conf: 1}}},
		{Idx: 0, StartMs: 0, EndMs: 50, Text: "a", Words: []asr.Word{{Text: "a"}}},
		{Idx: 1, StartMs: 60, EndMs: 90, Text: "b"},
	}
	parts, idxSet, err := buildRequest(segs)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	want := `{"segments":[{"idx":0,"text":"a"},{"idx":1,"text":"b"},{"idx":2,"text":"c"}]}`
	if parts != want {
		t.Errorf("request = %s, want %s", parts, want)
	}
	if len(idxSet) != 3 || !idxSet[0] || !idxSet[1] || !idxSet[2] {
		t.Errorf("idxSet = %v, want {0,1,2}", idxSet)
	}
}

// TestValidateAssignments exercises the grouping validator directly across the
// accept + reject shapes.
func TestValidateAssignments(t *testing.T) {
	idxSet := map[int]bool{0: true, 1: true, 2: true}
	ok := []assignment{{0, "S1"}, {1, "S2"}, {2, "S1"}}
	if err := validateAssignments(idxSet, ok); err != nil {
		t.Errorf("valid grouping rejected: %v", err)
	}
	bad := map[string][]assignment{
		"empty":     {},
		"unknown":   {{0, "S1"}, {1, "S1"}, {9, "S2"}},
		"gap":       {{0, "S1"}, {1, "S1"}},
		"overlap":   {{0, "S1"}, {0, "S2"}, {1, "S1"}, {2, "S1"}},
		"bad label": {{0, "s1"}, {1, "S1"}, {2, "S1"}},
		"S0 label":  {{0, "S0"}, {1, "S1"}, {2, "S1"}},
	}
	for name, assigns := range bad {
		if err := validateAssignments(idxSet, assigns); err == nil {
			t.Errorf("%s: validateAssignments = nil, want error", name)
		}
	}
}

// TestLangLabelResolver covers the resolver: fa resolves to the bound label, the
// primary-subtag fallback resolves fa-IR, an unknown language errors, and an
// empty label errors.
func TestLangLabelResolver(t *testing.T) {
	r := LangLabelResolver{Label: "bs-lm-1"}
	got, err := r.LabelFor(context.Background(), "fa")
	if err != nil || got != "bs-lm-1" {
		t.Errorf("LabelFor(fa) = %q,%v, want bs-lm-1,nil", got, err)
	}
	if _, err := r.LabelFor(context.Background(), "fa-IR"); err != nil {
		t.Errorf("LabelFor(fa-IR) = %v, want the fa label (primary-subtag fallback)", err)
	}
	if _, err := r.LabelFor(context.Background(), "zz"); err == nil {
		t.Error("LabelFor(zz) = nil, want unknown-language error")
	}
	if _, err := (LangLabelResolver{}).LabelFor(context.Background(), "fa"); err == nil {
		t.Error("LabelFor with empty label = nil, want error")
	}
}
