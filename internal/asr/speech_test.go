package asr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Fixture provenance (record/replay). These batch-operation envelopes are
// SCHEMA-FAITHFUL fixtures, not live captures: batchRecognize was not exercised
// from this offline environment (no ADC available), so each envelope is authored
// to the documented Speech v2 batchRecognize wire shape and to the Architect's
// live sync `recognize` verification (real 30s Persian audio: per-word
// startOffset/endOffset present, per-word confidence 0). Project ids and bucket
// are placeholders (PROJECT_ID / PROJECT_NUM / BUCKET) — no real ids in the repo.
//   - batch_submit.json             LRO submit response (operation name, no result)
//   - batch_op_running.json          in-progress poll (progressPercent, not done)
//   - batch_op_done_success.json     terminal op, inline transcript results
//   - batch_op_done_fileerror.json   terminal op, per-file PermissionDenied — the
//                                    exact error a missing Speech-service-agent
//                                    bucket-read grant produces (see docs/RUNBOOK.md)
// The success fixture's wire shape (proto3 Duration offset strings, per-word
// confidence 0, resultEndOffset, inlineResult.transcript.results[].alternatives[].
// words[]) matches the v2 API; its Persian text + ms mirror the committed
// fa_interview_open.json fixture so the fake and this engine stay consistent.

const (
	testKey    = "org_demo/ep_demo/proxies/audio.flac"
	testBucket = "BUCKET"
	testGSURI  = "gs://BUCKET/org_demo/ep_demo/proxies/audio.flac"
)

func loadSpeechFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "speech", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// speechSrv is a fixture server that answers the batchRecognize POST with submit,
// then answers the operation GET with runningPolls in-progress bodies followed by
// the terminal body. It records the last POST request body for shape assertions.
type speechSrv struct {
	*httptest.Server
	postBody atomic.Value // []byte
	gets     atomic.Int32
}

func newSpeechSrv(t *testing.T, submit, running, terminal []byte, runningPolls int) *speechSrv {
	t.Helper()
	s := &speechSrv{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchRecognize") {
			b, _ := io.ReadAll(r.Body)
			s.postBody.Store(b)
			_, _ = w.Write(submit)
			return
		}
		// operation GET
		n := int(s.gets.Add(1))
		if n <= runningPolls {
			_, _ = w.Write(running)
			return
		}
		_, _ = w.Write(terminal)
	}))
	t.Cleanup(s.Close)
	return s
}

func testEngine(t *testing.T, srv *speechSrv, mutate func(*SpeechConfig)) *SpeechEngine {
	t.Helper()
	cfg := SpeechConfig{
		Label:             "bs-asr-1",
		Model:             "chirp_2",
		Region:            "us-central1",
		Project:           "testproj",
		Bucket:            testBucket,
		LanguageCodes:     map[string]string{"fa": "fa-IR"},
		AdaptationEnabled: true,
		Endpoint:          srv.URL,
		Token:             func(context.Context) (string, error) { return "test-token", nil },
		HTTPClient:        srv.Client(),
		PollInterval:      time.Millisecond,
		PollTimeout:       5 * time.Second,
		Logger:            testLogger(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e, err := NewSpeechEngine(cfg)
	if err != nil {
		t.Fatalf("NewSpeechEngine: %v", err)
	}
	return e
}

func TestSpeechTranscribeSuccess(t *testing.T) {
	srv := newSpeechSrv(t,
		loadSpeechFixture(t, "batch_submit.json"),
		loadSpeechFixture(t, "batch_op_running.json"),
		loadSpeechFixture(t, "batch_op_done_success.json"),
		1) // one in-progress poll before done, exercising the poll loop
	e := testEngine(t, srv, nil)

	tr, err := e.Transcribe(context.Background(), TranscribeRequest{AudioKey: testKey, Language: "fa"})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	// Boundary contract.
	if err := tr.Validate(); err != nil {
		t.Fatalf("returned transcript failed Validate: %v", err)
	}
	if tr.Engine != "bs-asr-1" {
		t.Errorf("Engine = %q, want bs-asr-1", tr.Engine)
	}
	if tr.Language != "fa" {
		t.Errorf("Language = %q, want the echoed request tag fa", tr.Language)
	}
	if len(tr.Raw) == 0 {
		t.Error("Raw should carry the terminal provider envelope for the audit")
	}
	if len(tr.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(tr.Segments))
	}

	// ms-int conversion is exact (offset strings -> ms).
	first := tr.Segments[0].Words[0]
	if first.Text != "سلام" || first.StartMs != 0 || first.EndMs != 520 {
		t.Errorf("first word = %+v, want {سلام 0 520}", first)
	}
	if got := tr.Segments[0]; got.Idx != 0 || got.StartMs != 0 || got.EndMs != 2200 {
		t.Errorf("segment 0 = idx%d [%d,%d], want idx0 [0,2200]", got.Idx, got.StartMs, got.EndMs)
	}
	if got := tr.Segments[1]; got.Idx != 1 || got.StartMs != 2600 || got.EndMs != 4600 {
		t.Errorf("segment 1 = idx%d [%d,%d], want idx1 [2600,4600]", got.Idx, got.StartMs, got.EndMs)
	}
	// ZWNJ survives verbatim through parsing.
	var zwnj bool
	for _, wd := range tr.Segments[1].Words {
		if strings.ContainsRune(wd.Text, '‌') {
			zwnj = true
		}
	}
	if !zwnj {
		t.Error("ZWNJ (U+200C) not preserved through the engine")
	}
	// Verified: word confidence is 0 from this model; stored, not fabricated.
	if first.Conf != 0 {
		t.Errorf("word confidence = %v, want 0 (model returns no per-word confidence)", first.Conf)
	}

	// Request wire shape.
	var sent map[string]any
	if err := json.Unmarshal(srv.postBody.Load().([]byte), &sent); err != nil {
		t.Fatalf("submit body not JSON: %v", err)
	}
	cfgMap, _ := sent["config"].(map[string]any)
	if cfgMap["model"] != "chirp_2" {
		t.Errorf("config.model = %v, want chirp_2", cfgMap["model"])
	}
	if codes, _ := cfgMap["languageCodes"].([]any); len(codes) != 1 || codes[0] != "fa-IR" {
		t.Errorf("config.languageCodes = %v, want [fa-IR]", cfgMap["languageCodes"])
	}
	feats, _ := cfgMap["features"].(map[string]any)
	if feats["enableWordTimeOffsets"] != true {
		t.Errorf("features.enableWordTimeOffsets = %v, want true", feats["enableWordTimeOffsets"])
	}
	if _, ok := cfgMap["autoDecodingConfig"]; !ok {
		t.Error("config.autoDecodingConfig missing")
	}
	files, _ := sent["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files len = %d, want 1", len(files))
	}
	if f0, _ := files[0].(map[string]any); f0["uri"] != testGSURI {
		t.Errorf("file uri = %v, want %s", f0["uri"], testGSURI)
	}
	if oc, _ := sent["recognitionOutputConfig"].(map[string]any); oc == nil {
		t.Error("recognitionOutputConfig missing")
	} else if _, ok := oc["inlineResponseConfig"]; !ok {
		t.Error("recognitionOutputConfig.inlineResponseConfig missing (avoids the GCS-write grant)")
	}
}

func TestSpeechLanguageMapping(t *testing.T) {
	cases := []struct {
		tag  string
		want string
		m    map[string]string
	}{
		{"fa", "fa-IR", map[string]string{"fa": "fa-IR"}},
		{"fa-IR", "fa-IR", map[string]string{"fa": "fa-IR"}}, // explicit tag with no map entry passes verbatim
		{"en", "en", nil}, // unmapped -> verbatim
	}
	for _, c := range cases {
		srv := newSpeechSrv(t,
			loadSpeechFixture(t, "batch_submit.json"),
			loadSpeechFixture(t, "batch_op_running.json"),
			loadSpeechFixture(t, "batch_op_done_success.json"), 0)
		e := testEngine(t, srv, func(cfg *SpeechConfig) { cfg.LanguageCodes = c.m })
		_, _ = e.Transcribe(context.Background(), TranscribeRequest{AudioKey: testKey, Language: c.tag})
		var sent map[string]any
		_ = json.Unmarshal(srv.postBody.Load().([]byte), &sent)
		cfgMap, _ := sent["config"].(map[string]any)
		codes, _ := cfgMap["languageCodes"].([]any)
		if len(codes) != 1 || codes[0] != c.want {
			t.Errorf("tag %q -> languageCodes %v, want [%s]", c.tag, cfgMap["languageCodes"], c.want)
		}
	}
}

func TestSpeechAdaptationPayload(t *testing.T) {
	srv := newSpeechSrv(t,
		loadSpeechFixture(t, "batch_submit.json"),
		loadSpeechFixture(t, "batch_op_running.json"),
		loadSpeechFixture(t, "batch_op_done_success.json"), 0)
	e := testEngine(t, srv, func(cfg *SpeechConfig) { cfg.PhraseCap = 2 })

	_, _ = e.Transcribe(context.Background(), TranscribeRequest{
		AudioKey:  testKey,
		Language:  "fa",
		BiasTerms: []string{"تهران", "  ", "اصفهان", "شیراز"}, // one blank; 3 real, cap 2
	})
	var sent map[string]any
	_ = json.Unmarshal(srv.postBody.Load().([]byte), &sent)
	cfgMap, _ := sent["config"].(map[string]any)
	adaptation, ok := cfgMap["adaptation"].(map[string]any)
	if !ok {
		t.Fatalf("config.adaptation missing: %v", cfgMap["adaptation"])
	}
	sets, _ := adaptation["phraseSets"].([]any)
	if len(sets) != 1 {
		t.Fatalf("phraseSets len = %d, want 1", len(sets))
	}
	set0, _ := sets[0].(map[string]any)
	inline, _ := set0["inlinePhraseSet"].(map[string]any)
	phrases, _ := inline["phrases"].([]any)
	if len(phrases) != 2 {
		t.Fatalf("phrases len = %d, want 2 (PhraseCap truncation, blank skipped)", len(phrases))
	}
	p0, _ := phrases[0].(map[string]any)
	if p0["value"] != "تهران" {
		t.Errorf("phrase[0].value = %v, want تهران (glossary order preserved)", p0["value"])
	}
	if p0["boost"] == nil {
		t.Error("phrase[0].boost missing")
	}
}

func TestSpeechAdaptationDisabled(t *testing.T) {
	srv := newSpeechSrv(t,
		loadSpeechFixture(t, "batch_submit.json"),
		loadSpeechFixture(t, "batch_op_running.json"),
		loadSpeechFixture(t, "batch_op_done_success.json"), 0)
	e := testEngine(t, srv, func(cfg *SpeechConfig) { cfg.AdaptationEnabled = false })
	_, _ = e.Transcribe(context.Background(), TranscribeRequest{AudioKey: testKey, Language: "fa", BiasTerms: []string{"تهران"}})
	var sent map[string]any
	_ = json.Unmarshal(srv.postBody.Load().([]byte), &sent)
	cfgMap, _ := sent["config"].(map[string]any)
	if _, present := cfgMap["adaptation"]; present {
		t.Error("adaptation present although AdaptationEnabled=false")
	}
}

func TestSpeechOffsetToMs(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{`"0s"`, 0},
		{`"0.520s"`, 520},
		{`"1.760s"`, 1760},
		{`"12.3456s"`, 12346},  // round to nearest ms
		{`"12.3454s"`, 12345},  // round down
		{`""`, 0},              // empty string
		{`"1500s"`, 1_500_000}, // 25 min, no fractional part
		{`{"seconds":"1","nanos":760000000}`, 1760}, // object form
		{`{"nanos":500000000}`, 500},                // seconds omitted
	}
	for _, c := range cases {
		got, err := parseOffsetMs(json.RawMessage(c.raw))
		if err != nil {
			t.Errorf("parseOffsetMs(%s) error: %v", c.raw, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseOffsetMs(%s) = %d, want %d", c.raw, got, c.want)
		}
	}
	// Empty raw is 0, not an error.
	if got, err := parseOffsetMs(nil); err != nil || got != 0 {
		t.Errorf("parseOffsetMs(nil) = %d, %v; want 0, nil", got, err)
	}
}

func TestSpeechBuildSegmentsSkipsEmptyResults(t *testing.T) {
	// Two provider outcomes carry no usable word timing and must be skipped, not
	// turned into segments: a result with zero alternatives, and a result whose
	// alternative has a transcript but NO word offsets. The word-less case is the
	// load-bearing one — without the len(alt.Words)==0 guard, buildSegments would
	// index words[0]/words[len-1] on an empty slice and panic. A valid neighbour
	// must still yield exactly one segment, renumbered from Idx 0.
	results := []speechResult{
		{Alternatives: nil}, // zero alternatives -> skip
		{Alternatives: []speechAlternative{{
			Transcript: "بدون زمان", // an alternative/transcript but no word offsets -> skip (no panic)
			Words:      nil,
		}}},
		{Alternatives: []speechAlternative{{
			Transcript: "سلام دنیا",
			Words: []speechWord{
				{Word: "سلام", StartOffset: json.RawMessage(`"0s"`), EndOffset: json.RawMessage(`"0.500s"`)},
				{Word: "دنیا", StartOffset: json.RawMessage(`"0.560s"`), EndOffset: json.RawMessage(`"1.000s"`)},
			},
		}}, ResultEndOffset: json.RawMessage(`"1.000s"`)},
	}
	segs, err := buildSegments(results)
	if err != nil {
		t.Fatalf("buildSegments: %v", err)
	}
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1 (both empty results skipped, no zero-word segment)", len(segs))
	}
	got := segs[0]
	if got.Idx != 0 || got.Text != "سلام دنیا" || got.StartMs != 0 || got.EndMs != 1000 {
		t.Errorf("segment = idx%d %q [%d,%d], want idx0 \"سلام دنیا\" [0,1000]", got.Idx, got.Text, got.StartMs, got.EndMs)
	}
	if len(got.Words) != 2 {
		t.Errorf("words = %d, want 2", len(got.Words))
	}

	// A file result made entirely of empty results parses to a valid, empty
	// transcript through parseBatchResult — never a panic.
	op := operation{Response: &batchResponse{Results: map[string]batchFileResult{
		testGSURI: {InlineResult: &batchInlineResult{Transcript: &batchResults{Results: []speechResult{
			{Alternatives: nil},
			{Alternatives: []speechAlternative{{Transcript: "بدون زمان", Words: nil}}},
		}}}},
	}}}
	empty, err := parseBatchResult(op, testGSURI)
	if err != nil {
		t.Fatalf("parseBatchResult on all-empty results: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("segments = %d, want 0 for all-empty results", len(empty))
	}
	if err := (Transcript{Engine: "bs-asr-1", Language: "fa", Segments: empty}).Validate(); err != nil {
		t.Fatalf("empty transcript failed Validate: %v", err)
	}
}

func TestSpeechRecordedSuccessValidates(t *testing.T) {
	// Record/replay: the recorded terminal envelope parses to a valid transcript.
	var op operation
	if err := json.Unmarshal(loadSpeechFixture(t, "batch_op_done_success.json"), &op); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	segs, err := parseBatchResult(op, testGSURI)
	if err != nil {
		t.Fatalf("parseBatchResult: %v", err)
	}
	tr := Transcript{Engine: "bs-asr-1", Language: "fa", Segments: segs}
	if err := tr.Validate(); err != nil {
		t.Fatalf("recorded output failed Validate: %v", err)
	}
}

func TestSpeechFileErrorNeutral(t *testing.T) {
	srv := newSpeechSrv(t,
		loadSpeechFixture(t, "batch_submit.json"),
		loadSpeechFixture(t, "batch_op_running.json"),
		loadSpeechFixture(t, "batch_op_done_fileerror.json"), 0)
	e := testEngine(t, srv, nil)
	_, err := e.Transcribe(context.Background(), TranscribeRequest{AudioKey: testKey, Language: "fa"})
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("err = %v, want ErrEngineUnavailable", err)
	}
	assertNoLeak(t, "file-error return", err.Error())
}

func TestSpeechSubmitNon2xxNeutral(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"chirp_2 boom at speech.googleapis.com"}}`))
	}))
	defer srv.Close()
	e := testEngine(t, &speechSrv{Server: srv}, nil)
	_, err := e.Transcribe(context.Background(), TranscribeRequest{AudioKey: testKey, Language: "fa"})
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("err = %v, want ErrEngineUnavailable", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should carry the status code: %q", err.Error())
	}
	assertNoLeak(t, "non-2xx return", err.Error()) // provider body must not leak
}

func TestSpeechTransportErrorNeutral(t *testing.T) {
	base := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := base.Client()
	url := base.URL
	base.Close() // now unreachable
	e := testEngine(t, &speechSrv{Server: &httptest.Server{URL: url}}, func(cfg *SpeechConfig) {
		cfg.HTTPClient = client
		cfg.Endpoint = url
	})
	_, err := e.Transcribe(context.Background(), TranscribeRequest{AudioKey: testKey, Language: "fa"})
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("err = %v, want ErrEngineUnavailable", err)
	}
	assertNoLeak(t, "transport error", err.Error()) // the endpoint URL must not leak
}

func TestSpeechCredentialErrorNeutral(t *testing.T) {
	srv := newSpeechSrv(t,
		loadSpeechFixture(t, "batch_submit.json"), nil,
		loadSpeechFixture(t, "batch_op_done_success.json"), 0)
	e := testEngine(t, srv, func(cfg *SpeechConfig) {
		cfg.Token = func(context.Context) (string, error) { return "", errors.New("adc: metadata server unreachable") }
	})
	_, err := e.Transcribe(context.Background(), TranscribeRequest{AudioKey: testKey, Language: "fa"})
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("err = %v, want ErrEngineUnavailable", err)
	}
	assertNoLeak(t, "credential error", err.Error())
}

func TestSpeechPollTimeout(t *testing.T) {
	srv := newSpeechSrv(t,
		loadSpeechFixture(t, "batch_submit.json"),
		loadSpeechFixture(t, "batch_op_running.json"),
		loadSpeechFixture(t, "batch_op_done_success.json"),
		1_000_000) // never reaches done within the timeout
	e := testEngine(t, srv, func(cfg *SpeechConfig) {
		cfg.PollInterval = time.Millisecond
		cfg.PollTimeout = 20 * time.Millisecond
	})
	_, err := e.Transcribe(context.Background(), TranscribeRequest{AudioKey: testKey, Language: "fa"})
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("err = %v, want ErrEngineUnavailable on poll timeout", err)
	}
	assertNoLeak(t, "poll timeout", err.Error())
}

func TestSpeechContextCancelled(t *testing.T) {
	srv := newSpeechSrv(t,
		loadSpeechFixture(t, "batch_submit.json"),
		loadSpeechFixture(t, "batch_op_running.json"),
		loadSpeechFixture(t, "batch_op_done_success.json"), 0)
	e := testEngine(t, srv, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Transcribe(ctx, TranscribeRequest{AudioKey: testKey, Language: "fa"}); err == nil {
		t.Fatal("want error on cancelled context")
	}
}

func TestSpeechReturnedDataNeutral(t *testing.T) {
	srv := newSpeechSrv(t,
		loadSpeechFixture(t, "batch_submit.json"),
		loadSpeechFixture(t, "batch_op_running.json"),
		loadSpeechFixture(t, "batch_op_done_success.json"), 0)
	e := testEngine(t, srv, nil)
	tr, err := e.Transcribe(context.Background(), TranscribeRequest{AudioKey: testKey, Language: "fa"})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	// The caller-visible payload (engine label + segments) must name no provider.
	// Raw is deliberately excluded: it is the internal audit envelope and legitimately
	// carries the provider name.
	payload, _ := json.Marshal(struct {
		Engine   string    `json:"engine"`
		Language string    `json:"language"`
		Segments []Segment `json:"segments"`
	}{tr.Engine, tr.Language, tr.Segments})
	assertNoLeak(t, "returned transcript data", string(payload))
}

func TestNewSpeechEngineRejectsMisconfig(t *testing.T) {
	base := SpeechConfig{Label: "bs-asr-1", Model: "chirp_2", Region: "us-central1", Project: "p", Bucket: "b"}
	cases := []struct {
		name   string
		mutate func(*SpeechConfig)
	}{
		{"no label", func(c *SpeechConfig) { c.Label = "" }},
		{"no model", func(c *SpeechConfig) { c.Model = "" }},
		{"no region", func(c *SpeechConfig) { c.Region = "" }},
		{"no project", func(c *SpeechConfig) { c.Project = "" }},
		{"no bucket", func(c *SpeechConfig) { c.Bucket = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := base
			c.mutate(&cfg)
			if _, err := NewSpeechEngine(cfg); err == nil {
				t.Fatalf("NewSpeechEngine(%s) = nil err, want rejection", c.name)
			}
		})
	}
	// A minimal valid config succeeds and defaults the endpoint from the region.
	e, err := NewSpeechEngine(base)
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if !strings.Contains(e.endpoint, "us-central1-speech.googleapis.com") {
		t.Errorf("default endpoint = %q, want the regional speech host", e.endpoint)
	}
}

func TestSpeechRegistryIntegration(t *testing.T) {
	// The concrete engine registers under its neutral label like any Engine.
	e, err := NewSpeechEngine(SpeechConfig{Label: "bs-asr-1", Model: "chirp_2", Region: "us-central1", Project: "p", Bucket: "b"})
	if err != nil {
		t.Fatalf("NewSpeechEngine: %v", err)
	}
	reg, err := NewRegistry(e)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	got, err := reg.Get("bs-asr-1")
	if err != nil || got.Label() != "bs-asr-1" {
		t.Fatalf("Get(bs-asr-1) = %v, %v", got, err)
	}
}
