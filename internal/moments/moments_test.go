package moments

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

// zwnj is the U+200C zero-width non-joiner; the request must carry the segment
// text byte-for-byte and the quote validation must compare against it verbatim.
const zwnj = "\u200c"

// wordsFor derives a deterministic word sequence for a segment fixture: the
// text split on its single spaces (ZWNJ-joined words stay one word), times
// spread evenly inside [startMs, endMs]. joinWords(wordsFor(text)) == text by
// construction, so quotes that pass the text-substring gate also word-align —
// exactly the resegmentation-produced shape the stage reads.
func wordsFor(text string, startMs, endMs int) []asr.Word {
	parts := strings.Split(text, " ")
	step := (endMs - startMs) / len(parts)
	words := make([]asr.Word, 0, len(parts))
	for i, p := range parts {
		ws := startMs + i*step
		we := ws + step*3/4
		if i == len(parts)-1 {
			we = endMs
		}
		words = append(words, asr.Word{Text: p, StartMs: ws, EndMs: we, Conf: 0.95})
	}
	return words
}

// fixtureSegments is a six-segment fa interview with speaker keys, ASR times,
// and word timings — big enough that the 3..8 window applies unclamped.
func fixtureSegments() []pipeline.MomentSegment {
	mk := func(idx, start, end int, text, key string) pipeline.MomentSegment {
		return pipeline.MomentSegment{
			Segment:    asr.Segment{Idx: idx, StartMs: start, EndMs: end, Text: text, Words: wordsFor(text, start, end)},
			SpeakerKey: key,
		}
	}
	return []pipeline.MomentSegment{
		mk(0, 0, 2200, "سلام به برنامه خوش آمدید", "S1"),
		mk(1, 2600, 4600, "خیلی خوش"+zwnj+"حالم که اینجا هستم", "S2"),
		mk(2, 4700, 8000, "از تجربه سال"+zwnj+"های اول کارتان بگویید", "S1"),
		mk(3, 8200, 14000, "سال اول همه چیز را از دست دادیم ولی دوباره شروع کردیم", "S2"),
		mk(4, 14200, 18000, "مهم"+zwnj+"ترین درس این بود که هیچ وقت تسلیم نشویم", "S2"),
		mk(5, 18200, 20000, "از حضور شما سپاسگزارم", "S1"),
	}
}

// validOutput is a well-formed three-moment proposal over fixtureSegments:
// contiguous ranks, non-overlapping spans, verbatim quotes.
const validOutput = `{"moments":[
  {"rank":1,"start_idx":3,"end_idx":4,"rationale_en":"The comeback story plus its lesson is the strongest self-contained arc.","quote_fa":"هیچ وقت تسلیم نشویم"},
  {"rank":2,"start_idx":1,"end_idx":1,"rationale_en":"A warm, quotable opening beat from the guest.","quote_fa":"خیلی خوش` + zwnj + `حالم که اینجا هستم"},
  {"rank":3,"start_idx":2,"end_idx":2,"rationale_en":"The host question frames the whole conversation.","quote_fa":"سال` + zwnj + `های اول"}
]}`

// newEngine wires a moments Engine around a fake-backed llm.Client returning
// the given recorded output, plus the real fa label resolver. It returns the
// engine, the fake engine (to inspect what was sent), and the auditor.
func newEngine(t *testing.T, output string) (Engine, *llm.FakeEngine, *captureAuditor) {
	t.Helper()
	fe := llm.NewFakeEngine("bs-lm-1", "bs-lm-test", []byte(output), llm.WithFakeUsage(2400, 200))
	aud := &captureAuditor{}
	client, err := llm.NewFakeClient(aud, fe)
	if err != nil {
		t.Fatalf("NewFakeClient: %v", err)
	}
	return Engine{Gen: client, Labels: LangLabelResolver{Label: "bs-lm-1"}}, fe, aud
}

// TestSelectMomentsMaxOutputTokenBudget pins the output-token budget the moment
// selection request wires (m1-llm-token-budget regression guard): thinking tokens
// are billed as output and counted against maxOutputTokens, so the budget must
// cover thinking + the proposal set (>= 32768), asserted on the value that
// crossed the seam.
func TestSelectMomentsMaxOutputTokenBudget(t *testing.T) {
	const floor = 32768
	if maxOutputTokens < floor {
		t.Fatalf("maxOutputTokens = %d, want >= %d (must budget thinking + answer)", maxOutputTokens, floor)
	}
	eng, fe, _ := newEngine(t, validOutput)
	if _, err := eng.SelectMoments(context.Background(), "fa", 1, 1, fixtureSegments()); err != nil {
		t.Fatalf("SelectMoments: %v", err)
	}
	calls := fe.Calls()
	if len(calls) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(calls))
	}
	if calls[0].MaxTokens < floor {
		t.Errorf("request MaxTokens = %d, want >= %d", calls[0].MaxTokens, floor)
	}
}

// TestSelectMomentsProposesRanked: a valid recorded proposal set decodes to the
// exact rank-ordered proposals, and the single call is audited 'ok' with the
// right scope.
func TestSelectMomentsProposesRanked(t *testing.T) {
	eng, fe, aud := newEngine(t, validOutput)

	got, err := eng.SelectMoments(context.Background(), "fa", 7, 42, fixtureSegments())
	if err != nil {
		t.Fatalf("SelectMoments: %v", err)
	}
	want := []pipeline.ProposedMoment{
		{Rank: 1, StartIdx: 3, EndIdx: 4, RationaleEn: "The comeback story plus its lesson is the strongest self-contained arc.", QuoteFa: "هیچ وقت تسلیم نشویم"},
		{Rank: 2, StartIdx: 1, EndIdx: 1, RationaleEn: "A warm, quotable opening beat from the guest.", QuoteFa: "خیلی خوش" + zwnj + "حالم که اینجا هستم"},
		{Rank: 3, StartIdx: 2, EndIdx: 2, RationaleEn: "The host question frames the whole conversation.", QuoteFa: "سال" + zwnj + "های اول"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("proposals = %+v, want %+v", got, want)
	}
	if len(fe.Calls()) != 1 {
		t.Errorf("engine calls = %d, want 1 (no retry on a valid proposal set)", len(fe.Calls()))
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

// TestSelectMomentsRankOrderIsStable proves the returned slice is rank-ordered
// even when the model emits ranks out of order.
func TestSelectMomentsRankOrderIsStable(t *testing.T) {
	shuffled := `{"moments":[
	  {"rank":3,"start_idx":2,"end_idx":2,"rationale_en":"Frames the conversation.","quote_fa":"سال` + zwnj + `های اول"},
	  {"rank":1,"start_idx":3,"end_idx":4,"rationale_en":"Strongest arc.","quote_fa":"هیچ وقت تسلیم نشویم"},
	  {"rank":2,"start_idx":1,"end_idx":1,"rationale_en":"Warm opening.","quote_fa":"اینجا هستم"}
	]}`
	eng, _, _ := newEngine(t, shuffled)
	got, err := eng.SelectMoments(context.Background(), "fa", 1, 1, fixtureSegments())
	if err != nil {
		t.Fatalf("SelectMoments: %v", err)
	}
	for i, p := range got {
		if p.Rank != i+1 {
			t.Errorf("proposal %d has rank %d, want %d (rank-ordered)", i, p.Rank, i+1)
		}
	}
}

// TestSelectMomentsRequestShape proves the model request carries exactly {idx,
// text, start_ms, end_ms, speaker_key?} per segment in idx order — times MAY
// cross this seam (the model cites spans; its OUTPUT references idxs only) —
// and preserves the exact text including the U+200C ZWNJ.
func TestSelectMomentsRequestShape(t *testing.T) {
	eng, fe, _ := newEngine(t, validOutput)

	segs := fixtureSegments()
	segs[5].SpeakerKey = "" // one un-diarized segment: speaker_key omitted, not ""
	if _, err := eng.SelectMoments(context.Background(), "fa", 1, 1, segs); err != nil {
		t.Fatalf("SelectMoments: %v", err)
	}
	calls := fe.Calls()
	if len(calls) != 1 || len(calls[0].Parts) != 1 {
		t.Fatalf("calls = %+v, want one call with one part", calls)
	}
	sent := calls[0].Parts[0]

	// The exact text is present, ZWNJ and all.
	if !strings.Contains(sent, "خیلی خوش"+zwnj+"حالم") {
		t.Errorf("request lost the verbatim segment text (with ZWNJ): %s", sent)
	}
	// The words array (per-word timings) never crosses; segment times do.
	if strings.Contains(sent, `"words"`) || strings.Contains(sent, `"conf"`) {
		t.Errorf("request carried the words array: %s", sent)
	}

	var payload struct {
		Segments []map[string]json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal([]byte(sent), &payload); err != nil {
		t.Fatalf("request payload is not the expected shape: %v", err)
	}
	if len(payload.Segments) != len(segs) {
		t.Fatalf("request carried %d segments, want %d", len(payload.Segments), len(segs))
	}
	for i, seg := range payload.Segments {
		for _, key := range []string{"idx", "text", "start_ms", "end_ms"} {
			if _, ok := seg[key]; !ok {
				t.Errorf("segment %d missing %q", i, key)
			}
		}
		want := 5
		if i == 5 {
			want = 4 // the un-diarized segment omits speaker_key entirely
		}
		if len(seg) != want {
			t.Errorf("segment %d sent %d fields, want %d", i, len(seg), want)
		}
	}
}

// TestSelectMomentsLeavesSegmentsUntouched proves the selector never mutates
// the input transcript (verbatim invariant at the boundary).
func TestSelectMomentsLeavesSegmentsUntouched(t *testing.T) {
	eng, _, _ := newEngine(t, validOutput)
	segs := fixtureSegments()
	before := fixtureSegments()
	if _, err := eng.SelectMoments(context.Background(), "fa", 1, 1, segs); err != nil {
		t.Fatalf("SelectMoments: %v", err)
	}
	if !reflect.DeepEqual(segs, before) {
		t.Errorf("SelectMoments mutated the input segments:\n got %+v\nwant %+v", segs, before)
	}
}

// TestSelectMomentsInvalidOutputRetriesThenFails: an output that decodes but
// fails the semantic validation drives the /internal/llm one-retry-then-hard-
// fail path. Two 'invalid' audit rows are written and the returned error is
// the neutral ErrInvalidOutput. The FIRST case is the load-bearing verbatim
// gate: a fixture whose quote is a fluent PARAPHRASE (not a substring) of its
// span must fail — the model may select a quote, never rewrite one.
func TestSelectMomentsInvalidOutputRetriesThenFails(t *testing.T) {
	prefix := `{"moments":[{"rank":1,"start_idx":3,"end_idx":4,"rationale_en":"Strong arc.","quote_fa":"هیچ وقت تسلیم نشویم"},{"rank":2,"start_idx":1,"end_idx":1,"rationale_en":"Warm opening.","quote_fa":"اینجا هستم"},`
	cases := map[string]string{
		// A paraphrased quote: plausible Persian, same meaning-ish, NOT a
		// contiguous substring of segment 2's text.
		"non-substring quote": prefix + `{"rank":3,"start_idx":2,"end_idx":2,"rationale_en":"Framing question.","quote_fa":"از تجربه` + zwnj + `های اولیه کار بگویید"}]}`,
		// A quote missing its ZWNJ is byte-different, therefore NOT verbatim.
		"quote drops the ZWNJ": prefix + `{"rank":3,"start_idx":2,"end_idx":2,"rationale_en":"Framing question.","quote_fa":"سالهای اول"}]}`,
		// Only two moments on a six-segment transcript (window is 3..8).
		"too few": `{"moments":[{"rank":1,"start_idx":3,"end_idx":4,"rationale_en":"Arc.","quote_fa":"هیچ وقت تسلیم نشویم"},{"rank":2,"start_idx":1,"end_idx":1,"rationale_en":"Opening.","quote_fa":"اینجا هستم"}]}`,
		// Rank 4 with no rank 3: not contiguous from 1.
		"rank gap": prefix + `{"rank":4,"start_idx":2,"end_idx":2,"rationale_en":"Framing question.","quote_fa":"سال` + zwnj + `های اول"}]}`,
		// Rank 1 twice.
		"duplicate rank": prefix + `{"rank":1,"start_idx":2,"end_idx":2,"rationale_en":"Framing question.","quote_fa":"سال` + zwnj + `های اول"}]}`,
		// Span 4..4 overlaps rank 1's 3..4.
		"overlapping spans": prefix + `{"rank":3,"start_idx":4,"end_idx":4,"rationale_en":"The lesson.","quote_fa":"تسلیم نشویم"}]}`,
		// Span cites idx 9, which does not exist.
		"unknown idx": prefix + `{"rank":3,"start_idx":9,"end_idx":9,"rationale_en":"Nothing there.","quote_fa":"سال` + zwnj + `های اول"}]}`,
		// start_idx > end_idx.
		"inverted span": prefix + `{"rank":3,"start_idx":2,"end_idx":0,"rationale_en":"Backwards.","quote_fa":"سال` + zwnj + `های اول"}]}`,
		// Blank rationale.
		"blank rationale": prefix + `{"rank":3,"start_idx":2,"end_idx":2,"rationale_en":" ","quote_fa":"سال` + zwnj + `های اول"}]}`,
		// Blank quote.
		"blank quote": prefix + `{"rank":3,"start_idx":2,"end_idx":2,"rationale_en":"Framing question.","quote_fa":""}]}`,
		// Empty set.
		"empty": `{"moments":[]}`,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			eng, fe, aud := newEngine(t, out)

			got, err := eng.SelectMoments(context.Background(), "fa", 3, 9, fixtureSegments())
			if !errors.Is(err, llm.ErrInvalidOutput) {
				t.Fatalf("err = %v, want ErrInvalidOutput", err)
			}
			if got != nil {
				t.Errorf("proposals = %v, want nil on failure", got)
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

// TestSelectMomentsMisalignedWordsRetriesThenFails is the word-alignment gate:
// a quote that IS a verbatim substring of the segment's TEXT but cannot be
// located in the segment's WORD data (drifted word text — text and words
// disagree) is invalid output. The stage derives the moment's word-accurate
// times from that alignment, so the failure must land on the /internal/llm
// one-retry-then-hard-fail path, never on the stage after the paid call.
func TestSelectMomentsMisalignedWordsRetriesThenFails(t *testing.T) {
	segs := fixtureSegments()[:3]
	// Segment 2's words no longer match its text: the text keeps the phrase the
	// model will quote, the word data says something else entirely.
	segs[2].Words = wordsFor("چیز کاملا متفاوت دیگری", segs[2].StartMs, segs[2].EndMs)

	out := `{"moments":[
	  {"rank":1,"start_idx":0,"end_idx":0,"rationale_en":"Greeting.","quote_fa":"خوش آمدید"},
	  {"rank":2,"start_idx":1,"end_idx":1,"rationale_en":"Reply.","quote_fa":"اینجا هستم"},
	  {"rank":3,"start_idx":2,"end_idx":2,"rationale_en":"Question.","quote_fa":"سال` + zwnj + `های اول"}
	]}`
	eng, fe, aud := newEngine(t, out)
	_, err := eng.SelectMoments(context.Background(), "fa", 1, 1, segs)
	if !errors.Is(err, llm.ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput (quote does not align to word data)", err)
	}
	assertNeutralErr(t, err)
	if len(fe.Calls()) != 2 || len(aud.rows) != 2 {
		t.Errorf("calls=%d audit=%d, want 2 and 2 (one retry)", len(fe.Calls()), len(aud.rows))
	}
}

// TestSelectMomentsTinyTranscriptClampsWindow codifies the clamp: a transcript
// of n < 3 segments admits at most n non-overlapping spans, so the demo-sized
// two-segment transcript accepts a two-moment proposal (and still rejects an
// empty one — covered above).
func TestSelectMomentsTinyTranscriptClampsWindow(t *testing.T) {
	out := `{"moments":[
	  {"rank":1,"start_idx":1,"end_idx":1,"rationale_en":"Guest reply.","quote_fa":"اینجا هستم"},
	  {"rank":2,"start_idx":0,"end_idx":0,"rationale_en":"Host greeting.","quote_fa":"خوش آمدید"}
	]}`
	eng, _, _ := newEngine(t, out)
	got, err := eng.SelectMoments(context.Background(), "fa", 1, 1, fixtureSegments()[:2])
	if err != nil {
		t.Fatalf("SelectMoments(two segments): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("proposals = %d, want 2 (window clamped to the transcript size)", len(got))
	}
}

// TestValidateMoments exercises the validator directly across accept shapes the
// end-to-end cases above do not cover — notably a quote that crosses a segment
// boundary through the single-space join.
func TestValidateMoments(t *testing.T) {
	segs := fixtureSegments()

	// A quote spanning the end of segment 3 and the start of segment 4, joined
	// by exactly one space, is verbatim for the 3..4 span.
	crossing := []proposal{
		{Rank: 1, StartIdx: 3, EndIdx: 4, RationaleEn: "Arc.", QuoteFa: "دوباره شروع کردیم مهم" + zwnj + "ترین درس"},
		{Rank: 2, StartIdx: 1, EndIdx: 1, RationaleEn: "Opening.", QuoteFa: "اینجا هستم"},
		{Rank: 3, StartIdx: 5, EndIdx: 5, RationaleEn: "Close.", QuoteFa: "سپاسگزارم"},
	}
	if err := validateMoments(segs, crossing); err != nil {
		t.Errorf("boundary-crossing verbatim quote rejected: %v", err)
	}

	// The same crossing quote with TWO spaces at the join is not verbatim.
	doubled := append([]proposal(nil), crossing...)
	doubled[0].QuoteFa = "دوباره شروع کردیم  مهم" + zwnj + "ترین درس"
	if err := validateMoments(segs, doubled); err == nil {
		t.Error("non-verbatim join accepted, want reject")
	}

	// A crossing quote whose span EXCLUDES the second segment is not a substring
	// of the (single-segment) span text.
	outside := append([]proposal(nil), crossing...)
	outside[0].EndIdx = 3
	if err := validateMoments(segs, outside); err == nil {
		t.Error("quote reaching outside its span accepted, want reject")
	}

	// Nine single-segment spans cannot fit six segments without overlap, but the
	// count gate (max 8) must fire first and cleanly.
	nine := make([]proposal, 9)
	for i := range nine {
		nine[i] = proposal{Rank: i + 1, StartIdx: i % 6, EndIdx: i % 6, RationaleEn: "x", QuoteFa: "سلام"}
	}
	if err := validateMoments(segs, nine); err == nil {
		t.Error("nine moments accepted, want reject (max 8)")
	}
}

// TestSelectMomentsUnknownLanguageErrors: an unregistered language is an
// explicit error (never a silent default), and no LLM call is made.
func TestSelectMomentsUnknownLanguageErrors(t *testing.T) {
	eng, fe, aud := newEngine(t, `{"moments":[]}`)
	if _, err := eng.SelectMoments(context.Background(), "zz", 1, 1, fixtureSegments()); err == nil {
		t.Fatal("SelectMoments(zz) = nil error, want unknown-language error")
	}
	if len(fe.Calls()) != 0 {
		t.Errorf("engine calls = %d, want 0 (label resolution failed first)", len(fe.Calls()))
	}
	if len(aud.rows) != 0 {
		t.Errorf("audit rows = %d, want 0 (no call made)", len(aud.rows))
	}
}

// TestBuildRequestOrdersByIdx is a direct unit check that the request builder
// serializes segments in idx order regardless of input order, and rejects a
// duplicate idx.
func TestBuildRequestOrdersByIdx(t *testing.T) {
	segs := []pipeline.MomentSegment{
		{Segment: asr.Segment{Idx: 2, StartMs: 100, EndMs: 200, Text: "c"}, SpeakerKey: "S1"},
		{Segment: asr.Segment{Idx: 0, StartMs: 0, EndMs: 50, Text: "a"}},
		{Segment: asr.Segment{Idx: 1, StartMs: 60, EndMs: 90, Text: "b"}, SpeakerKey: "S2"},
	}
	parts, ordered, err := buildRequest(segs)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	want := `{"segments":[{"idx":0,"text":"a","start_ms":0,"end_ms":50},{"idx":1,"text":"b","start_ms":60,"end_ms":90,"speaker_key":"S2"},{"idx":2,"text":"c","start_ms":100,"end_ms":200,"speaker_key":"S1"}]}`
	if parts != want {
		t.Errorf("request = %s, want %s", parts, want)
	}
	if len(ordered) != 3 || ordered[0].Idx != 0 || ordered[2].Idx != 2 {
		t.Errorf("ordered = %+v, want idx order 0,1,2", ordered)
	}

	dup := append(segs, pipeline.MomentSegment{Segment: asr.Segment{Idx: 1, Text: "b2"}})
	if _, _, err := buildRequest(dup); err == nil {
		t.Error("buildRequest(duplicate idx) = nil, want error")
	}
}

// TestLangLabelResolver covers the resolver: fa resolves to the bound label,
// the primary-subtag fallback resolves fa-IR, an unknown language errors, and
// an empty label errors.
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
