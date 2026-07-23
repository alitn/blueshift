// Package asr is the single seam through which the app turns an audio object into
// a word-timed transcript. Everything downstream (segmentation, diarization,
// caption burn-in, the editor) consumes a Transcript from here; nothing talks to
// a speech provider directly. Three properties are enforced at this boundary:
//
//   - Vendor neutrality. Callers select an engine by a neutral label ("bs-asr-1")
//     that config binds to a concrete provider at runtime. Provider and model
//     names live ONLY in the concrete engine files in this package (which the
//     vendor-leak gate does not scan) and in config/deploy; they never appear in
//     a Transcript field a caller reads as data, in a returned error, or in any
//     other client-visible surface. The registry here maps the neutral label to
//     an engine and returns an explicit error for an unknown label — it mirrors
//     the /internal/lang and /internal/llm registries.
//
//   - Millisecond-integer timing. Word and segment boundaries cross this seam as
//     integer milliseconds (the schema convention, `start_ms int` / `end_ms int`
//     on `segments`), never floats-of-seconds. Each concrete engine converts its
//     provider's timing representation to ms ints inside this package, so no
//     float-seconds value ever escapes.
//
//   - The verbatim invariant. Word text and its timings are copied from the
//     engine's output, never generated or measured by anything above this seam
//     (CLAUDE.md). Validate enforces the structural invariants that make the
//     copied timing usable downstream — words are non-overlapping, monotonic, and
//     bounded by their segment — so a malformed engine result is rejected at the
//     boundary instead of corrupting captions later.
//
// The ASR boundary is deliberately speaker-agnostic: a Segment is one contiguous
// utterance, and speaker attribution is a separate later stage. Callers pass an
// audio object reference (a storage key), not bytes: each engine fetches the
// audio itself via storage, keeping large media out of the request path.
package asr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Sentinel errors returned across this package's boundary. None names a provider.
var (
	// ErrUnknownEngine means a neutral engine label is not registered. It mirrors
	// llm.ErrUnknownEngine and lang.ErrUnknownLanguage: an unregistered label is
	// an explicit failure, never a silent default.
	ErrUnknownEngine = errors.New("asr: unknown engine")

	// ErrInvalidTranscript means a Transcript failed Validate: its words or
	// segments overlap, run non-monotonically, or fall outside their segment's
	// bounds. The wrapped message states which invariant broke and where.
	ErrInvalidTranscript = errors.New("asr: invalid transcript")
)

// Engine is one speech-recognition backend bound to a neutral label. Concrete
// implementations live in this package (the fake in fake.go for tests/demo; the
// provider-backed engine in a later task) — the only place a provider name may
// appear. An Engine is safe for concurrent use.
type Engine interface {
	// Label returns the neutral engine label this engine answers to (e.g.
	// "bs-asr-1"). It is the key the registry maps and the value stamped onto
	// every Transcript the engine produces; it never carries a provider name.
	Label() string

	// Transcribe transcribes the audio object referenced by req.AudioKey and
	// returns an ordered, speaker-agnostic Transcript. The engine fetches the
	// audio itself via storage; callers never stream bytes through this method.
	// A returned error is always neutral (raw provider causes are logged
	// server-side only). A successful Transcript satisfies Validate.
	Transcribe(ctx context.Context, req TranscribeRequest) (Transcript, error)
}

// TranscribeRequest is one transcription job.
type TranscribeRequest struct {
	// AudioKey is the storage object key of the audio to transcribe, org- and
	// episode-prefixed per the blob layout ("{org}/{episode}/..."). The engine
	// reads the bytes from storage using this key.
	AudioKey string

	// Language is the BCP-47 content language tag of the audio (e.g. "fa"). It is
	// the same tag carried on episodes.language and drives language-specific
	// engine behaviour resolved through /internal/lang.
	Language string

	// BiasTerms are optional recognition-bias hints, typically glossary_terms for
	// Language (names, jargon). They only nudge recognition toward known spellings;
	// they never inject or rewrite output — the verbatim invariant holds.
	BiasTerms []string

	// Options are neutral, engine-agnostic tuning key/values. Keys are abstract
	// slots supplied from config (resolved per language via /internal/lang), never
	// provider- or model-specific names. An engine ignores keys it does not use.
	Options map[string]string
}

// Word is one recognised token with its timing copied verbatim from the engine.
// StartMs/EndMs are integer milliseconds (never floats-of-seconds) forming the
// half-open span [StartMs, EndMs). Conf is the engine's confidence in [0,1].
type Word struct {
	Text    string  `json:"text"`
	StartMs int     `json:"start_ms"`
	EndMs   int     `json:"end_ms"`
	Conf    float64 `json:"conf"`
}

// Segment is one contiguous, speaker-agnostic utterance. Idx is its ordinal in
// the transcript (0-based, strictly increasing). StartMs/EndMs bound the segment
// in integer milliseconds. Text is the engine's verbatim transcript for the
// utterance. Words are the token-level breakdown, ordered and contained within
// [StartMs, EndMs).
type Segment struct {
	Idx     int    `json:"idx"`
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	Text    string `json:"text"`
	Words   []Word `json:"words"`
}

// Transcript is the ordered result of transcribing one audio object. Engine is
// the neutral label that produced it; Language echoes the requested BCP-47 tag.
// Raw is the engine's raw metadata blob, retained ONLY for the internal audit
// (llm_calls-style) and never surfaced to clients — it may name a provider.
type Transcript struct {
	Engine   string          `json:"engine"`
	Language string          `json:"language"`
	Segments []Segment       `json:"segments"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

// Validate enforces the structural invariants every Transcript must satisfy
// before anything downstream (segmentation, captions, the editor) relies on its
// timing. It is used by callers at the boundary AND by the engines here on their
// own output. The checks, in order per element:
//
//   - Segments: non-negative timing; EndMs >= StartMs; Idx strictly increasing;
//     each segment starts at or after the previous segment ended (segments are
//     ordered and non-overlapping).
//   - Words within a segment: non-negative timing; EndMs >= StartMs (monotonic
//     within the word); contained in the segment's [StartMs, EndMs]; starting no
//     earlier than the previous word did (monotonic) and no earlier than the
//     previous word ended (non-overlapping).
//
// A violation returns an error wrapping ErrInvalidTranscript that names the
// offending element and the invariant it broke.
func (t Transcript) Validate() error {
	prevSegEnd := 0
	prevIdx := -1
	for i, seg := range t.Segments {
		switch {
		case seg.StartMs < 0 || seg.EndMs < 0:
			return fmt.Errorf("%w: segment %d has negative timing [%d,%d]", ErrInvalidTranscript, i, seg.StartMs, seg.EndMs)
		case seg.EndMs < seg.StartMs:
			return fmt.Errorf("%w: segment %d is non-monotonic: end %d before start %d", ErrInvalidTranscript, i, seg.EndMs, seg.StartMs)
		case seg.Idx <= prevIdx:
			return fmt.Errorf("%w: segment %d has non-increasing idx %d (previous %d)", ErrInvalidTranscript, i, seg.Idx, prevIdx)
		case i > 0 && seg.StartMs < prevSegEnd:
			return fmt.Errorf("%w: segment %d overlaps previous: start %d before previous end %d", ErrInvalidTranscript, i, seg.StartMs, prevSegEnd)
		}
		if err := validateWords(i, seg); err != nil {
			return err
		}
		prevSegEnd = seg.EndMs
		prevIdx = seg.Idx
	}
	return nil
}

// validateWords checks the word-level invariants for one segment. It is split out
// so Validate reads as segment-then-word and so the word checks have a single
// home. prevStart/prevEnd track the previous word to separate the two failure
// classes: a start earlier than the previous START is non-monotonic; a start
// earlier than the previous END (but not its start) is an overlap.
func validateWords(segIdx int, seg Segment) error {
	prevStart := 0
	prevEnd := 0
	for j, w := range seg.Words {
		switch {
		case w.StartMs < 0 || w.EndMs < 0:
			return fmt.Errorf("%w: segment %d word %d has negative timing [%d,%d]", ErrInvalidTranscript, segIdx, j, w.StartMs, w.EndMs)
		case w.EndMs < w.StartMs:
			return fmt.Errorf("%w: segment %d word %d is non-monotonic: end %d before start %d", ErrInvalidTranscript, segIdx, j, w.EndMs, w.StartMs)
		case w.StartMs < seg.StartMs || w.EndMs > seg.EndMs:
			return fmt.Errorf("%w: segment %d word %d spans [%d,%d] outside segment bounds [%d,%d]", ErrInvalidTranscript, segIdx, j, w.StartMs, w.EndMs, seg.StartMs, seg.EndMs)
		case j > 0 && w.StartMs < prevStart:
			return fmt.Errorf("%w: segment %d word %d is non-monotonic: start %d before previous word start %d", ErrInvalidTranscript, segIdx, j, w.StartMs, prevStart)
		case j > 0 && w.StartMs < prevEnd:
			return fmt.Errorf("%w: segment %d word %d overlaps previous: start %d before previous word end %d", ErrInvalidTranscript, segIdx, j, w.StartMs, prevEnd)
		}
		prevStart = w.StartMs
		prevEnd = w.EndMs
	}
	return nil
}

// Registry maps neutral engine labels to their Engine implementations. It is
// built once from configured engines and is read-only thereafter, so it needs no
// locking and is safe for concurrent use — the same immutable-after-construction
// stance the llm client's engine map takes. Config decides which label backs a
// language's `asr` engine slot (declared by /internal/lang); the registry only
// resolves that label to code.
type Registry struct {
	engines map[string]Engine
}

// NewRegistry builds a Registry from the given engines, keyed by each engine's
// Label. It fails fast on a nil engine, an empty label, a duplicate label, or an
// empty set — all startup misconfigurations that must not resolve ambiguously at
// request time.
func NewRegistry(engines ...Engine) (*Registry, error) {
	reg := make(map[string]Engine, len(engines))
	for _, e := range engines {
		if e == nil {
			return nil, errors.New("asr: nil engine")
		}
		label := e.Label()
		if label == "" {
			return nil, errors.New("asr: engine label is required")
		}
		if _, dup := reg[label]; dup {
			return nil, fmt.Errorf("asr: duplicate engine label %q", label)
		}
		reg[label] = e
	}
	if len(reg) == 0 {
		return nil, errors.New("asr: at least one engine must be registered")
	}
	return &Registry{engines: reg}, nil
}

// Get resolves a neutral label to its Engine, returning an error wrapping
// ErrUnknownEngine for an unregistered label.
func (r *Registry) Get(label string) (Engine, error) {
	if e, ok := r.engines[label]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownEngine, label)
}

// Labels returns the registered neutral labels in sorted order. Intended for
// diagnostics and tests, not hot-path use.
func (r *Registry) Labels() []string {
	out := make([]string, 0, len(r.engines))
	for l := range r.engines {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}
