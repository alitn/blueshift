package asr

// speech.go is the first concrete Engine behind the neutral asr.Engine seam. It
// talks to the platform's Speech v2 REST API (batchRecognize). Provider and model
// names appear here BY DESIGN: this package is the boundary the vendor-leak gate
// deliberately does not scan, and nothing in this file's request/response types,
// returned errors, or returned Transcript *data* escapes to a caller naming a
// provider — every failure is collapsed into the neutral ErrEngineUnavailable
// (carrying only an opaque internal error id), and the raw provider envelope is
// retained solely in Transcript.Raw for the internal audit (never client-visible).
//
// Facts this file is built on (Architect research + live verification; the prod
// receipts of 2026-07-24 supersede the 2026-07-23 chirp_2 findings — cite before
// changing):
//
//   - The model is `chirp_3` and the choice is FORCED, not preferential: the
//     first real prod batch call on chirp_2 failed 403 "Permission denied for
//     project … on model chirp_2 locale fa-IR. It is no longer generally
//     available." (2026-07-24) — chirp_2's Persian is closed off for API
//     callers. chirp_3 + fa-IR + enableWordTimeOffsets was then live-verified on
//     the real prod audio (sync AND batchRecognize, location `us`): 641 words
//     WITH offsets on the 4-min file. The docs' feature table claiming chirp_3
//     lacks word timestamps is wrong/stale — overturned empirically. fa-IR on
//     chirp_3 is Preview status. Region, endpoint, and model stay config
//     (SpeechConfig), never constants, so the next engine shift is a config row,
//     not a code change.
//   - chirp_3's serving location is the `us` MULTIREGION (endpoint
//     `https://us-speech.googleapis.com`) — a provider serving location,
//     independent of the GCP compute region (us-central1). Deploy sets the
//     literal `us`, never the $REGION compute var.
//   - chirp_3 rejects `features.enable_word_confidence`: the second prod attempt
//     failed 400 "Recognizer does not support feature: word_level_confidence"
//     (2026-07-24). chirp_2 accepted the flag and returned zeros anyway, so
//     nothing is lost: the request sends ONLY enableWordTimeOffsets, and word
//     `confidence` parses defaulting to 0 when absent — we store what the
//     provider returns and never fabricate one.
//   - chirp_3 omits zero-value proto3 Durations on the wire: the FIRST word's
//     startOffset came back ABSENT on the prod probe. parseOffsetMs maps an
//     empty offset to 0 ms; the regression fixture
//     testdata/speech/batch_op_done_absent_offset.json pins that behaviour.
//   - batchRecognize (used here for long masters) takes GCS URIs that are read by
//     the per-product Speech service agent
//     `service-{projectnumber}@gcp-sa-speech.iam.gserviceaccount.com`, NOT the
//     caller — that agent must be granted read on the media bucket (see
//     docs/RUNBOOK.md; a missing grant surfaces as the per-file PermissionDenied
//     the error-mapping test replays). We request inlineResponseConfig so the
//     terminal operation carries results inline and no bucket *write* grant is
//     needed. The batch request/response wire types below follow the documented
//     Speech v2 batchRecognize schema, cross-checked against the Architect's live
//     chirp_3 batch probe (2026-07-24); the committed fixtures are
//     placeholder-scrubbed authored envelopes matching that verified shape (see
//     speech_test.go).
//
// Auth is a bearer token from the module's oauth2/ADC plumbing (adc.go); tests
// inject a static token via the tokenFunc seam so the transport is exercised
// offline with no credential. The v2 wire types below are unexported and never
// cross the package boundary.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrEngineUnavailable means a concrete engine could not reach or answer through
// its provider (transport failure, non-2xx, a per-file provider error, a poll
// timeout, or an unparseable response). It mirrors llm.ErrUnavailable: the raw
// cause is logged server-side only, and the returned error carries just an opaque
// internal error id correlating it to that log line. It names no provider.
var ErrEngineUnavailable = errors.New("asr: engine unavailable")

// tokenFunc returns a bearer access token for a request. It is the seam over the
// oauth2/ADC plumbing so the transport can be exercised offline with a static
// token. A nil tokenFunc means "send no Authorization header" (test servers).
type tokenFunc func(ctx context.Context) (string, error)

// Speech-v2 defaults. The phrase cap is a conservative self-imposed bound: the
// platform's model-adaptation docs limit inline phrase-set size, and 500 short
// glossary phrases sit comfortably under any documented inline limit while
// keeping the request small. It is configurable (SpeechConfig.PhraseCap).
const (
	defaultPhraseCap    = 500
	defaultPhraseBoost  = 10.0 // moderate; the platform accepts boost in [0,20].
	defaultPollInterval = 5 * time.Second
	defaultPollTimeout  = 30 * time.Minute
	defaultHTTPTimeout  = 60 * time.Second
	maxProviderBody     = 8 << 20 // batch inline results can be large; bound the read.
)

// SpeechConfig fully specifies one Speech-v2-backed engine. Region, model, and
// bucket are required; everything else defaults. Provider/model names live only
// in this config and in deploy — never in a client-visible surface.
type SpeechConfig struct {
	// Label is the neutral engine label this engine answers to (e.g. "bs-asr-1").
	Label string
	// Model is the concrete provider model (e.g. "chirp_3").
	Model string
	// Region is the provider SERVING LOCATION; it also selects the regional
	// endpoint when Endpoint is unset. Documented default/recommended: "us" — the
	// multiregion location chirp_3 serves fa-IR from (endpoint derives as
	// https://us-speech.googleapis.com), live-verified with word offsets on the
	// real prod audio (2026-07-24). It is independent of the GCP compute region:
	// deploy sets the literal "us", never the $REGION compute var (us-central1).
	Region string
	// Project is the GCP project id owning the recognizer and the bucket.
	Project string
	// Bucket is the media bucket the AudioKey lives in; the engine builds the
	// gs:// URI as gs://{Bucket}/{AudioKey}.
	Bucket string
	// LanguageCodes maps a request's BCP-47 content tag to the provider language
	// code (e.g. {"fa": "fa-IR"}). A request tag with no mapping is passed to the
	// provider verbatim. Keeping the map in config (not code) honours "language as
	// data": adding a language adds a row, not a branch.
	LanguageCodes map[string]string
	// PhraseCap bounds how many bias phrases are sent inline (default 500). Extra
	// terms are dropped deterministically (glossary order preserved).
	PhraseCap int
	// AdaptationEnabled turns glossary bias terms into an inline adaptation phrase
	// set. Defaults on. NOTE: adaptation support is model/version dependent and
	// was not part of the chirp_3 live probes; if the provider rejects the
	// adaptation block, set this false via config and the terms are simply omitted.
	AdaptationEnabled bool
	// Endpoint overrides the derived regional endpoint (host, no path), e.g. for
	// tests. Default: https://{Region}-speech.googleapis.com
	Endpoint string
	// Token is the bearer source; nil selects Application Default Credentials.
	Token tokenFunc
	// HTTPClient overrides the default client; nil uses one with a bounded timeout.
	HTTPClient *http.Client
	// PollInterval / PollTimeout bound the batch operation poll loop.
	PollInterval time.Duration
	PollTimeout  time.Duration
	// Logger records raw provider causes server-side (never returned). Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// SpeechEngine is a Speech-v2-backed Engine. It is immutable after construction
// and safe for concurrent use.
type SpeechEngine struct {
	cfg      SpeechConfig
	endpoint string // host, no trailing slash
	hc       *http.Client
	log      *slog.Logger
}

var _ Engine = (*SpeechEngine)(nil)

// NewSpeechEngine builds a SpeechEngine from cfg, failing fast on missing
// required fields. It applies defaults for everything optional and defaults the
// token source to ADC (adc.go) so production needs no explicit token.
func NewSpeechEngine(cfg SpeechConfig) (*SpeechEngine, error) {
	switch {
	case cfg.Label == "":
		return nil, errors.New("asr: engine label is required")
	case cfg.Model == "":
		return nil, errors.New("asr: engine model is required")
	case cfg.Region == "":
		return nil, errors.New("asr: engine region is required")
	case cfg.Project == "":
		return nil, errors.New("asr: engine project is required")
	case cfg.Bucket == "":
		return nil, errors.New("asr: engine bucket is required")
	}

	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s-speech.googleapis.com", cfg.Region)
	}
	if cfg.PhraseCap <= 0 {
		cfg.PhraseCap = defaultPhraseCap
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = defaultPollTimeout
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if cfg.Token == nil {
		cfg.Token = adcTokenFunc(speechCloudScope)
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &SpeechEngine{cfg: cfg, endpoint: endpoint, hc: hc, log: log}, nil
}

// Label returns the neutral engine label.
func (e *SpeechEngine) Label() string { return e.cfg.Label }

// Transcribe transcribes the audio object at req.AudioKey via one batchRecognize
// operation and returns a validated, speaker-agnostic Transcript. The engine
// constructs the gs:// URI from the configured bucket and the key; the provider
// reads the bytes from storage (via its service agent), so no audio streams
// through this call. Every failure is a neutral ErrEngineUnavailable carrying an
// opaque error id; the raw cause is logged, never returned.
//
// Long audio: this issues a SINGLE batchRecognize over the whole key. The
// documented batch constraint is 1 minute to 1 hour in general, but only up to
// ~20 minutes when word-level timestamps are enabled (the generic Speech batch
// limit) — and this product ingests 40-minute-plus interviews. So an upstream
// stage transcribing audio longer than that word-timestamp cap splits it into
// <=15-min chunks (a margin under 20 min), transcribes each chunk key with this
// engine, and combines the per-chunk results with StitchTranscripts (stitch.go) —
// the deterministic ms-offset merge lives there. This engine stays single-key
// because the chunk boundaries are cut with ffmpeg in the worker (a later task),
// not here.
func (e *SpeechEngine) Transcribe(ctx context.Context, req TranscribeRequest) (Transcript, error) {
	if err := ctx.Err(); err != nil {
		return Transcript{}, err
	}
	if req.AudioKey == "" {
		return Transcript{}, fmt.Errorf("%w: audio key is required", ErrEngineUnavailable)
	}

	reqBody := e.buildBatchRequest(req)
	body, err := json.Marshal(reqBody)
	if err != nil {
		return Transcript{}, e.fail(ctx, "marshal request", err)
	}

	op, err := e.submit(ctx, body)
	if err != nil {
		return Transcript{}, err // already neutralised + logged
	}
	done, rawDone, err := e.poll(ctx, op)
	if err != nil {
		return Transcript{}, err
	}

	segs, err := parseBatchResult(done, e.gsURI(req.AudioKey))
	if err != nil {
		return Transcript{}, e.fail(ctx, "parse result", err)
	}
	tr := Transcript{
		Engine:   e.cfg.Label,
		Language: req.Language,
		Segments: segs,
		Raw:      rawDone,
	}
	if err := tr.Validate(); err != nil {
		// The provider returned timing that violates the boundary invariants;
		// reject it rather than let malformed timing corrupt captions later.
		return Transcript{}, e.fail(ctx, "provider transcript invalid", err)
	}
	return tr, nil
}

// gsURI builds the storage URI the provider reads from.
func (e *SpeechEngine) gsURI(key string) string {
	return "gs://" + e.cfg.Bucket + "/" + key
}

// languageCode resolves a request BCP-47 tag to the provider language code,
// preferring the configured mapping and falling back to the tag verbatim.
func (e *SpeechEngine) languageCode(tag string) string {
	if c, ok := e.cfg.LanguageCodes[tag]; ok && c != "" {
		return c
	}
	return tag
}

// ----- request wire types -----

type batchRequest struct {
	Config                  batchConfig       `json:"config"`
	Files                   []batchFile       `json:"files"`
	RecognitionOutputConfig batchOutputConfig `json:"recognitionOutputConfig"`
}

type batchConfig struct {
	Model              string           `json:"model"`
	LanguageCodes      []string         `json:"languageCodes"`
	Features           batchFeatures    `json:"features"`
	AutoDecodingConfig struct{}         `json:"autoDecodingConfig"`
	Adaptation         *batchAdaptation `json:"adaptation,omitempty"`
}

// batchFeatures requests word time offsets and nothing else. enable_word_confidence
// is deliberately NOT a field here (not even sent as false): chirp_3 rejects the
// feature outright — the second prod attempt failed 400 "Recognizer does not
// support feature: word_level_confidence" (2026-07-24). chirp_2 accepted the flag
// and returned zeros anyway, so dropping it loses nothing: word `confidence`
// parses defaulting to 0 when absent (speechWord), stored, never fabricated.
type batchFeatures struct {
	EnableWordTimeOffsets bool `json:"enableWordTimeOffsets"`
}

type batchAdaptation struct {
	PhraseSets []batchPhraseSetRef `json:"phraseSets"`
}

type batchPhraseSetRef struct {
	InlinePhraseSet batchInlinePhraseSet `json:"inlinePhraseSet"`
}

type batchInlinePhraseSet struct {
	Phrases []batchPhrase `json:"phrases"`
}

type batchPhrase struct {
	Value string  `json:"value"`
	Boost float64 `json:"boost,omitempty"`
}

type batchFile struct {
	URI string `json:"uri"`
}

type batchOutputConfig struct {
	// InlineResponseConfig requests the results embedded in the operation, so no
	// GCS write grant is needed for the Speech service agent.
	InlineResponseConfig struct{} `json:"inlineResponseConfig"`
}

// buildBatchRequest assembles the batchRecognize body for one key.
func (e *SpeechEngine) buildBatchRequest(req TranscribeRequest) batchRequest {
	br := batchRequest{
		Config: batchConfig{
			Model:         e.cfg.Model,
			LanguageCodes: []string{e.languageCode(req.Language)},
			Features: batchFeatures{
				EnableWordTimeOffsets: true,
			},
		},
		Files: []batchFile{{URI: e.gsURI(req.AudioKey)}},
	}
	if e.cfg.AdaptationEnabled && len(req.BiasTerms) > 0 {
		br.Config.Adaptation = e.buildAdaptation(req.BiasTerms)
	}
	return br
}

// buildAdaptation turns bias terms into a single inline phrase set, capped at
// PhraseCap phrases (extra terms dropped, glossary order preserved) and skipping
// empties. Returns nil if nothing usable remains.
func (e *SpeechEngine) buildAdaptation(terms []string) *batchAdaptation {
	phrases := make([]batchPhrase, 0, len(terms))
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		phrases = append(phrases, batchPhrase{Value: t, Boost: defaultPhraseBoost})
		if len(phrases) >= e.cfg.PhraseCap {
			break
		}
	}
	if len(phrases) == 0 {
		return nil
	}
	return &batchAdaptation{PhraseSets: []batchPhraseSetRef{{InlinePhraseSet: batchInlinePhraseSet{Phrases: phrases}}}}
}

// ----- response wire types (LRO + BatchRecognizeResponse) -----

type operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Error    *operationError `json:"error,omitempty"`
	Response *batchResponse  `json:"response,omitempty"`
}

type operationError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type batchResponse struct {
	Results map[string]batchFileResult `json:"results"`
}

type batchFileResult struct {
	Error        *operationError    `json:"error,omitempty"`
	InlineResult *batchInlineResult `json:"inlineResult,omitempty"`
	// Transcript is the deprecated inline field some responses still populate
	// alongside inlineResult; parsed as a fallback.
	Transcript *batchResults `json:"transcript,omitempty"`
}

type batchInlineResult struct {
	Transcript *batchResults `json:"transcript,omitempty"`
}

type batchResults struct {
	Results []speechResult `json:"results"`
}

type speechResult struct {
	Alternatives    []speechAlternative `json:"alternatives"`
	ResultEndOffset json.RawMessage     `json:"resultEndOffset,omitempty"`
}

type speechAlternative struct {
	Transcript string       `json:"transcript"`
	Confidence float64      `json:"confidence"`
	Words      []speechWord `json:"words"`
}

type speechWord struct {
	Word        string          `json:"word"`
	StartOffset json.RawMessage `json:"startOffset,omitempty"`
	EndOffset   json.RawMessage `json:"endOffset,omitempty"`
	Confidence  float64         `json:"confidence"`
}

// submit POSTs the batchRecognize request and returns the operation name to poll.
// The terminal operation (not this submit response) is the authoritative envelope
// retained for the audit, so only the name is returned here. Errors are
// neutralised + logged.
func (e *SpeechEngine) submit(ctx context.Context, body []byte) (string, error) {
	endpoint := fmt.Sprintf("%s/v2/projects/%s/locations/%s/recognizers/_:batchRecognize",
		e.endpoint, e.cfg.Project, e.cfg.Region)
	raw, status, err := e.do(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return "", e.fail(ctx, "submit request", err)
	}
	if status != http.StatusOK {
		return "", e.failStatus(ctx, "submit", status, raw)
	}
	var op operation
	if err := json.Unmarshal(raw, &op); err != nil {
		return "", e.fail(ctx, "decode submit", err)
	}
	if op.Name == "" {
		return "", e.fail(ctx, "submit", errors.New("operation has no name"))
	}
	return op.Name, nil
}

// poll GETs the operation until done or the poll deadline, returning the terminal
// operation and its raw body. It respects context cancellation and the configured
// PollTimeout.
func (e *SpeechEngine) poll(ctx context.Context, name string) (operation, json.RawMessage, error) {
	deadline := time.Now().Add(e.cfg.PollTimeout)
	endpoint := e.endpoint + "/v2/" + name
	for {
		raw, status, err := e.do(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return operation{}, nil, e.fail(ctx, "poll request", err)
		}
		if status != http.StatusOK {
			return operation{}, nil, e.failStatus(ctx, "poll", status, raw)
		}
		var op operation
		if err := json.Unmarshal(raw, &op); err != nil {
			return operation{}, nil, e.fail(ctx, "decode poll", err)
		}
		if op.Done {
			if op.Error != nil {
				// Operation-level failure (whole batch), not a per-file error.
				return operation{}, nil, e.fail(ctx, "operation failed",
					fmt.Errorf("code %d: %s", op.Error.Code, op.Error.Message))
			}
			return op, raw, nil
		}
		if time.Now().After(deadline) {
			return operation{}, nil, e.fail(ctx, "poll", errors.New("operation did not complete before deadline"))
		}
		select {
		case <-ctx.Done():
			return operation{}, nil, ctx.Err()
		case <-time.After(e.cfg.PollInterval):
		}
	}
}

// do issues one request with bearer auth and returns the (bounded) body + status.
// Transport/URL detail is stripped so a returned error carries no endpoint.
func (e *SpeechEngine) do(ctx context.Context, method, endpoint string, body []byte) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		// A NewRequest error can embed the endpoint URL; drop it.
		return nil, 0, errors.New("build request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.cfg.Token != nil {
		tok, terr := e.cfg.Token(ctx)
		if terr != nil {
			return nil, 0, fmt.Errorf("acquire credentials: %w", terr)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := e.hc.Do(req)
	if err != nil {
		return nil, 0, transportCause(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	return raw, resp.StatusCode, nil
}

// parseBatchResult extracts the segments for the given file URI from a terminal
// batch operation. A per-file provider error is surfaced (neutralised by the
// caller). Offsets are converted to ms ints at this boundary.
func parseBatchResult(op operation, uri string) ([]Segment, error) {
	if op.Response == nil {
		return nil, errors.New("terminal operation has no response")
	}
	fr, ok := op.Response.Results[uri]
	if !ok {
		return nil, fmt.Errorf("no result for requested file")
	}
	if fr.Error != nil {
		return nil, fmt.Errorf("file error code %d: %s", fr.Error.Code, fr.Error.Message)
	}
	res := inlineResults(fr)
	if res == nil {
		return nil, errors.New("file result has no transcript")
	}
	return buildSegments(res.Results)
}

// inlineResults picks the transcript results, preferring inlineResult (the field
// inlineResponseConfig populates) and falling back to the deprecated transcript.
func inlineResults(fr batchFileResult) *batchResults {
	if fr.InlineResult != nil && fr.InlineResult.Transcript != nil {
		return fr.InlineResult.Transcript
	}
	return fr.Transcript
}

// buildSegments converts provider results into ordered Segments. Each result with
// words becomes one speaker-agnostic segment; empty results are skipped. Timing:
// segment start = first word start; segment end = max(last word end,
// resultEndOffset); word offsets are copied and only unit-converted. Rounding is
// nearest-millisecond (math.Round, ties away from zero); because rounding is
// monotonic non-decreasing, a provider stream that is monotonic and
// non-overlapping in seconds stays so in ms — Validate then passes, and a
// genuinely malformed provider stream is rejected there instead of being
// silently repaired (the verbatim invariant: we convert, never fabricate).
func buildSegments(results []speechResult) ([]Segment, error) {
	segs := make([]Segment, 0, len(results))
	for _, r := range results {
		if len(r.Alternatives) == 0 {
			continue
		}
		alt := r.Alternatives[0]
		if len(alt.Words) == 0 {
			continue // no word timing -> not usable for captions; skip.
		}
		words := make([]Word, 0, len(alt.Words))
		for _, w := range alt.Words {
			startMs, err := parseOffsetMs(w.StartOffset)
			if err != nil {
				return nil, err
			}
			endMs, err := parseOffsetMs(w.EndOffset)
			if err != nil {
				return nil, err
			}
			words = append(words, Word{Text: w.Word, StartMs: startMs, EndMs: endMs, Conf: w.Confidence})
		}
		segStart := words[0].StartMs
		segEnd := words[len(words)-1].EndMs
		if reo, err := parseOffsetMs(r.ResultEndOffset); err == nil && reo > segEnd {
			segEnd = reo
		}
		segs = append(segs, Segment{
			StartMs: segStart,
			EndMs:   segEnd,
			Text:    alt.Transcript,
			Words:   words,
		})
	}
	// Results are already time-ordered, but sort defensively so Idx is assigned in
	// time order regardless of provider ordering.
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].StartMs < segs[j].StartMs })
	for i := range segs {
		segs[i].Idx = i
	}
	return segs, nil
}

// parseOffsetMs converts a protobuf Duration (proto3-JSON string form "1.760s",
// or the {seconds,nanos} object form) to integer milliseconds, rounded to the
// nearest ms. An empty value is 0. See buildSegments for why rounding preserves
// monotonicity.
func parseOffsetMs(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	// proto3 JSON serialises Duration as a string like "1.760s".
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "s"))
		if s == "" {
			return 0, nil
		}
		sec, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return 0, fmt.Errorf("parse offset: %w", perr)
		}
		return int(math.Round(sec * 1000)), nil
	}
	// Fallback: {seconds, nanos} object (seconds may be a string per proto3 JSON).
	var o struct {
		Seconds json.Number `json:"seconds"`
		Nanos   int64       `json:"nanos"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return 0, fmt.Errorf("unrecognised offset form")
	}
	sec, _ := o.Seconds.Int64()
	return int(sec*1000 + int64(math.Round(float64(o.Nanos)/1e6))), nil
}

// fail logs the raw cause server-side with an opaque id and returns the neutral
// sentinel carrying that id. If ctx was cancelled, the context error is returned
// as-is (it is already neutral and lets callers distinguish cancellation).
func (e *SpeechEngine) fail(ctx context.Context, op string, cause error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	id := errorID()
	e.log.LogAttrs(ctx, slog.LevelError, "asr engine failure",
		slog.String("engine", e.cfg.Label),
		slog.String("op", op),
		slog.String("error_id", id),
		slog.String("cause", cause.Error()))
	return fmt.Errorf("%w [%s]", ErrEngineUnavailable, id)
}

// failStatus is fail for a non-2xx provider status: the status code is logged and
// carried (a code is not a provider name), the body is logged for diagnosis.
func (e *SpeechEngine) failStatus(ctx context.Context, op string, status int, body []byte) error {
	id := errorID()
	e.log.LogAttrs(ctx, slog.LevelError, "asr engine status",
		slog.String("engine", e.cfg.Label),
		slog.String("op", op),
		slog.Int("status", status),
		slog.String("error_id", id),
		slog.String("body", string(body)))
	return fmt.Errorf("%w: status %d [%s]", ErrEngineUnavailable, status, id)
}

// errorID returns a short opaque hex id correlating a caller-visible failure with
// the server log line that holds the raw cause. It names nothing.
func errorID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// transportCause unwraps a *url.Error to its underlying cause, dropping the
// request URL from the message (it can carry the endpoint or query credentials).
func transportCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
