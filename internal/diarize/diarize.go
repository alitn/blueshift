// Package diarize is the text-anchored LLM diarizer: it groups an episode's
// transcript segments into speaker turns by asking a language model (through the
// /internal/llm seam) who is speaking, anchored to the ASR TEXT only. It returns
// an episode-local speaker label (S1, S2, ...) for every segment.
//
// It is a sibling of internal/asr: where asr provides the speech engine the
// transcribe stage drives, diarize provides the diarizer the diarize stage
// drives. It lives in its own package (not in internal/pipeline) for a concrete
// reason: internal/store imports internal/pipeline (it implements pipeline.Repo)
// and internal/llm imports internal/store (its audit adapter), so pipeline cannot
// import llm without forming a cycle. The pipeline stage therefore consumes this
// package only through the neutral pipeline.Diarizer seam (satisfied here by
// Engine), and the LLM wiring stays on this side of the seam.
//
// Two invariants are enforced here:
//
//   - Text-anchored (CLAUDE.md, "LLMs decide, they never measure"). The request
//     sends the model ONLY {idx, text} per segment — never a timestamp and never
//     the words array. The model anchors its grouping to the text; timings stay
//     the province of ASR/ffmpeg. The output — speaker turns as contiguous idx
//     RANGES — is validated to tile the segment idx space exactly 0..n-1 (sorted,
//     no gap, no overlap, no out-of-range idx); an invalid output takes the
//     /internal/llm one-retry-then-hard-fail path.
//
//   - Vendor-neutral. The engine is selected by a neutral label the lang registry
//     declares for the content language; no provider name appears in the prompt,
//     the schema, the returned map, or any error. Every call is audited in
//     llm_calls behind the llm seam.
//
// Why ranges, not a flat per-segment list: the first contract (one
// {segment_idx, speaker_key} pair per segment) asked the model to reproduce
// every idx exactly once. At full-episode scale (a real 44-min episode: 249
// segments) flash-class models reliably drop or duplicate a few idxs in that
// long mechanical list, so the strict validator correctly rejected the output
// twice and the stage hard-failed. Turn ranges are ~10x fewer output tokens at
// that scale and make total coverage structurally natural — a turn is where one
// speaker starts and stops, which is how diarization is actually expressed —
// so the same strict validation (exact tiling) holds at scale.
package diarize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"blueshift/internal/asr"
	"blueshift/internal/lang"
	"blueshift/internal/llm"
)

// promptID and promptVersion identify the diarization prompt template for the
// llm_calls audit. Bump the version on any change to the prompt or schema (a new
// version is a new audited contract). v2 = the turn-range output contract
// (v1 was the flat per-segment assignment list, retired for failing at
// full-episode scale).
const (
	promptID      = "diarize.turns"
	promptVersion = "v2"
	// maxOutputTokens caps the generation's output-token budget. This budget is
	// NOT answer-tokens-alone: on the prod model generation the model's internal
	// "thinking" tokens are billed as output and counted against this same cap
	// (verified 2026-07-24 against cloud.google.com/vertex-ai/generative-ai/docs/
	// thinking — "Thinking generates 'thoughts' as part of the Token Output" — and
	// ai.google.dev/gemini-api/docs/thinking — "response pricing is the sum of
	// output tokens and thinking tokens"). Thinking scales with INPUT size: at a
	// real 249-segment episode it runs ~5.6–7.9k tokens (wire-identical prod
	// replays, m1-llm-token-budget receipts), while the turn-range answer itself is
	// ~1.1k. The old 8192 cap budgeted for the answer alone, so a normal thinking
	// pass truncated the JSON mid-array (finishReason MAX_TOKENS) → strict decode
	// failed → the stage hard-failed. 32768 budgets thinking + answer together with
	// ≥20k headroom over the worst observed thinking, and sits well under the
	// model's documented 65,536 output-token ceiling (Gemini 3.5 Flash model card,
	// cloud.google.com/vertex-ai/generative-ai/docs/models/gemini/3-5-flash). Cost
	// stays bounded by the /internal/llm one-retry cap and the per-episode
	// attempt cap regardless (worst case ≈30¢/call, typical ≈8¢).
	//
	// Thinking is NOT bounded with a generation knob here, by design. gemini-3.5-
	// flash is a Gemini-3 model whose only thinking control is the COARSE
	// thinkingConfig.thinkingLevel (MINIMAL|LOW|MEDIUM|HIGH, default MEDIUM); the
	// fine-grained token thinkingBudget is a Gemini-2.5-and-earlier feature and
	// specifying it on a Gemini-3 model is a documented error (same docs). The
	// levels do not hard-cap thinking to a token count; the default (MEDIUM) is
	// what the probes ran at and what produced the observed-adequate ~6–8k, so
	// lowering to LOW risks starving thinking below observed need and MINIMAL risks
	// a 400 (it requires prior thought signatures our stateless single-turn call
	// has none of). The raised cap is therefore the structural budget; no other
	// generation knob changes.
	maxOutputTokens = 32768
)

// systemPrompt instructs the model to group segments into speaker turns anchored
// to the text, and to return the turns as contiguous idx ranges. It names no
// provider and asks for grouping ONLY — the model must never alter, translate,
// or re-time the text (the verbatim invariant is enforced downstream regardless:
// this stage only ever writes speaker_key).
const systemPrompt = `You are a speaker diarizer for an interview transcript.

You are given the transcript as an ordered list of segments, each with an integer "idx" and its verbatim "text". Decide who is speaking and divide the transcript into speaker turns: a turn is a run of consecutive segments spoken by the same person.

Return the turns as contiguous idx ranges: each turn is {"start_idx", "end_idx", "speaker_key"} and covers segments start_idx through end_idx inclusive. A turn may cover a single segment (start_idx equal to end_idx). The turns must be in transcript order, must not overlap, and together must cover every segment idx from the first to the last — no segment left out, none covered twice.

Assign each turn an episode-local speaker label of the form "S1", "S2", "S3", ... . Use "S1" for the first speaker that appears, "S2" for the next distinct speaker, and so on; reuse the same label whenever the same person speaks again. Base your decision ONLY on the text and its turn-taking cues (questions vs. answers, self-reference, address). Do not use, infer, or emit any timing information. Do not change, translate, summarize, or re-order the text; you are labelling speakers, nothing else.`

// outputSchema is the provider-agnostic JSON schema the output is constrained by
// (server-side) and strict-decoded against (locally). One turn per speaker run:
// an inclusive integer idx range and a string label; no extra keys.
var outputSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["turns"],
  "properties": {
    "turns": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["start_idx", "end_idx", "speaker_key"],
        "properties": {
          "start_idx": { "type": "integer" },
          "end_idx": { "type": "integer" },
          "speaker_key": { "type": "string" }
        }
      }
    }
  }
}`)

// speakerKeyRE is the episode-local label shape the validator accepts: "S" then a
// positive integer. It keeps the label space neutral and predictable (S1, S2, …)
// and rejects anything the model might otherwise invent.
var speakerKeyRE = regexp.MustCompile(`^S[1-9][0-9]*$`)

// requestSegment is one segment as sent to the model: idx + text ONLY. No
// start_ms/end_ms/words field exists on this type, so a timestamp can never be
// serialized into the request — text-anchoring is structural, not a convention.
type requestSegment struct {
	Idx  int    `json:"idx"`
	Text string `json:"text"`
}

// requestPayload is the user turn: the ordered segments to diarize.
type requestPayload struct {
	Segments []requestSegment `json:"segments"`
}

// turn is one decoded speaker turn from the model: a contiguous, inclusive
// segment idx range and its episode-local speaker label.
type turn struct {
	StartIdx   int    `json:"start_idx"`
	EndIdx     int    `json:"end_idx"`
	SpeakerKey string `json:"speaker_key"`
}

// output is the model's decoded response.
type output struct {
	Turns []turn `json:"turns"`
}

// Generator is the /internal/llm seam: one schema-constrained, audited generation
// with the one-retry-then-hard-fail loop. *llm.Client satisfies it.
type Generator interface {
	Generate(ctx context.Context, req llm.Request) (llm.Response, error)
}

// LabelResolver resolves the neutral LLM engine label for a content language. The
// concrete engine behind the label is bound at wiring time from config; provider
// choice never crosses this seam.
type LabelResolver interface {
	LabelFor(ctx context.Context, language string) (string, error)
}

// Engine is the text-anchored diarizer. It resolves the neutral engine label for
// the episode's language, sends the model the segments' idx+text, validates that
// the returned turn ranges tile the segment idx space exactly, and returns
// idx -> speaker_key covering every segment. It satisfies pipeline.Diarizer by
// duck typing (no pipeline import), so the diarize stage drives it through that
// neutral seam.
//
// SCALE FALLBACK (documented deliberately, NOT built — MVP bias): the range
// contract is proven at full-episode scale (~250 segments; see the eval scale
// golden). If it ever proves brittle at ~2h scale (~600+ segments), the next
// step is WINDOWED diarization with overlap continuity: diarize fixed-size idx
// windows (e.g. 200 segments) that overlap by a margin (e.g. 20 segments),
// validate each window with the same exact-tiling rule, then stitch windows by
// matching the speaker labels the overlapping segments received in both windows
// and relabelling globally. Do not build it until a real episode fails at range
// scale.
type Engine struct {
	// Gen is the audited, schema-constrained LLM seam (a *llm.Client in production,
	// a fake-backed Client in tests and the golden eval).
	Gen Generator
	// Labels resolves the neutral engine label for a content language.
	Labels LabelResolver
}

// Diarize groups segs (idx-ordered) into speaker turns and returns idx ->
// speaker_key covering every segment exactly once. orgID and episodeID are the
// INTERNAL ids the llm_calls audit is scoped by. A neutral llm sentinel
// (llm.ErrInvalidOutput after one retry on an invalid grouping, llm.ErrUnavailable
// on an unreachable engine) is returned unwrapped for the stage to treat as a
// failure — no provider text ever leaks.
func (e Engine) Diarize(ctx context.Context, language string, orgID, episodeID int64, segs []asr.Segment) (map[int]string, error) {
	if e.Gen == nil {
		return nil, errors.New("diarize: no llm generator configured")
	}
	if e.Labels == nil {
		return nil, errors.New("diarize: no engine label resolver configured")
	}
	if len(segs) == 0 {
		return nil, errors.New("diarize: no segments to diarize")
	}

	label, err := e.Labels.LabelFor(ctx, language)
	if err != nil {
		return nil, err
	}

	parts, n, err := buildRequest(segs)
	if err != nil {
		return nil, err
	}

	var out output
	_, err = e.Gen.Generate(ctx, llm.Request{
		Engine:        label,
		PromptID:      promptID,
		PromptVersion: promptVersion,
		System:        systemPrompt,
		Parts:         []string{parts},
		Schema:        outputSchema,
		// Temperature is deliberately left unset: on the wire it is dropped by
		// omitempty when zero, so a literal 0 would send nothing and run at the
		// engine default anyway (see llm.Request.Temperature). We do not claim a
		// pin we cannot send; all diarize traffic runs at the engine default.
		MaxTokens: maxOutputTokens,
		OrgID:     orgID,
		EpisodeID: episodeID,
		Out:       &out,
		// Validate runs after a successful strict decode; a non-nil error is treated
		// exactly like a decode failure by the Client (retry once, then hard fail).
		// This is where the exact-tiling contract is enforced against the real
		// segment count — an unsorted, overlapping, gapped, reversed, or
		// out-of-range turn all fail here.
		Validate: func() error { return validateTurns(n, out.Turns) },
	})
	if err != nil {
		return nil, err
	}

	return speakersFromTurns(out.Turns), nil
}

// buildRequest serializes the segments to the user-turn payload (idx + text ONLY)
// and returns the JSON string plus the segment count n the output tiling is
// validated against. Segments are emitted in idx order so the request is
// deterministic regardless of the caller's slice order. The transcript's idxs
// must be exactly 0..n-1 (the shape transcribe persists); a duplicate or
// non-contiguous idx is an internal error caught here, BEFORE any billable call.
func buildRequest(segs []asr.Segment) (string, int, error) {
	ordered := make([]asr.Segment, len(segs))
	copy(ordered, segs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Idx < ordered[j].Idx })

	payload := requestPayload{Segments: make([]requestSegment, 0, len(ordered))}
	for i, s := range ordered {
		if s.Idx != i {
			return "", 0, fmt.Errorf("diarize: transcript segment idxs are not contiguous 0..n-1 (found idx %d at position %d)", s.Idx, i)
		}
		// Only idx and text — never a timestamp or the words array.
		payload.Segments = append(payload.Segments, requestSegment{Idx: s.Idx, Text: s.Text})
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", 0, fmt.Errorf("diarize: marshal request: %w", err)
	}
	return string(b), len(ordered), nil
}

// validateTurns checks the model's turn ranges against the real segment count:
// the ranges must be sorted, non-overlapping, and tile EXACTLY 0..n-1 — no gap,
// no reversed range, no idx outside the transcript — with a well-formed
// episode-local label on every turn. It is the semantic gate that turns a
// plausible-but-wrong grouping into the one-retry-then-fail path. Its error text
// is neutral (no provider names) — it is surfaced only in server logs.
func validateTurns(n int, turns []turn) error {
	if len(turns) == 0 {
		return errors.New("diarize: empty turn list")
	}
	next := 0 // the idx the next turn must start at for an exact tiling
	for i, t := range turns {
		if !speakerKeyRE.MatchString(t.SpeakerKey) {
			return fmt.Errorf("diarize: turn %d has malformed speaker label", i)
		}
		if t.StartIdx > t.EndIdx {
			return fmt.Errorf("diarize: turn %d range %d..%d is reversed", i, t.StartIdx, t.EndIdx)
		}
		if t.StartIdx < 0 || t.EndIdx > n-1 {
			return fmt.Errorf("diarize: turn %d range %d..%d is outside segments 0..%d", i, t.StartIdx, t.EndIdx, n-1)
		}
		switch {
		case t.StartIdx < next:
			return fmt.Errorf("diarize: turn %d range %d..%d overlaps or is out of order (next uncovered idx is %d)", i, t.StartIdx, t.EndIdx, next)
		case t.StartIdx > next:
			return fmt.Errorf("diarize: gap before turn %d — segments %d..%d have no speaker", i, next, t.StartIdx-1)
		}
		next = t.EndIdx + 1
	}
	if next != n {
		return fmt.Errorf("diarize: turns cover segments 0..%d of 0..%d (gap at the end)", next-1, n-1)
	}
	return nil
}

// speakersFromTurns expands validated turn ranges to the per-segment
// idx -> speaker_key map the pipeline persists. Storage, DTOs, and the API are
// untouched by the range contract: downstream only ever sees a speaker_key per
// segment. Called only after validateTurns accepted the exact tiling, so the
// expansion covers every idx 0..n-1 exactly once.
func speakersFromTurns(turns []turn) map[int]string {
	byIdx := make(map[int]string)
	for _, t := range turns {
		for idx := t.StartIdx; idx <= t.EndIdx; idx++ {
			byIdx[idx] = t.SpeakerKey
		}
	}
	return byIdx
}

// LangLabelResolver resolves the neutral LLM engine label for a content language
// from the lang registry: the language must be registered and must declare an llm
// engine slot (EngineLLM), then the wiring-bound neutral Label is returned. An
// unregistered language, or one that declares no llm slot, is an explicit error —
// never a silent default. It mirrors the transcribe stage's ASR resolver.
type LangLabelResolver struct {
	// Label is the neutral engine label bound to the llm slot (e.g. "bs-lm-1"). It
	// never carries a provider name.
	Label string
}

var _ LabelResolver = LangLabelResolver{}

// LabelFor gates the language through the lang registry, verifies it declares an
// llm engine slot, and returns the bound neutral label.
func (r LangLabelResolver) LabelFor(_ context.Context, language string) (string, error) {
	l, err := lang.Get(language)
	if err != nil {
		return "", fmt.Errorf("diarize: language %q not registered: %w", language, err)
	}
	if !declaresEngine(l, lang.EngineLLM) {
		return "", fmt.Errorf("diarize: language %q declares no llm engine slot", language)
	}
	if r.Label == "" {
		return "", errors.New("diarize: no llm engine label configured")
	}
	return r.Label, nil
}

// declaresEngine reports whether l declares the given engine slot.
func declaresEngine(l lang.Language, key lang.EngineKey) bool {
	for _, k := range l.EngineKeys() {
		if k == key {
			return true
		}
	}
	return false
}
