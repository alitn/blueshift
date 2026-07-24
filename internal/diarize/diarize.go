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
//     the province of ASR/ffmpeg. The output is validated to assign every existing
//     segment idx exactly once (no unknown idx, no gap, no overlap); an invalid
//     output takes the /internal/llm one-retry-then-hard-fail path.
//
//   - Vendor-neutral. The engine is selected by a neutral label the lang registry
//     declares for the content language; no provider name appears in the prompt,
//     the schema, the returned map, or any error. Every call is audited in
//     llm_calls behind the llm seam.
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
// version is a new audited contract).
const (
	promptID      = "diarize.turns"
	promptVersion = "v1"
	// maxOutputTokens caps the diarization output. One assignment per segment is
	// tiny; this is a generous ceiling that never truncates a real transcript.
	maxOutputTokens = 8192
)

// systemPrompt instructs the model to group segments into speaker turns anchored
// to the text. It names no provider and asks for grouping ONLY — the model must
// never alter, translate, or re-time the text (the verbatim invariant is enforced
// downstream regardless: this stage only ever writes speaker_key).
const systemPrompt = `You are a speaker diarizer for an interview transcript.

You are given the transcript as an ordered list of segments, each with an integer "idx" and its verbatim "text". Decide which segments are spoken by the same person and group consecutive segments into speaker turns.

Assign every segment an episode-local speaker label of the form "S1", "S2", "S3", ... . Use "S1" for the first speaker that appears, "S2" for the next distinct speaker, and so on. Base your decision ONLY on the text and its turn-taking cues (questions vs. answers, self-reference, address). Do not use, infer, or emit any timing information.

Return an assignment for EVERY segment idx exactly once — no idx omitted, none repeated, and no idx that was not given. Do not change, translate, summarize, or re-order the text; you are labelling speakers, nothing else.`

// outputSchema is the provider-agnostic JSON schema the output is constrained by
// (server-side) and strict-decoded against (locally). An assignment per segment,
// with an integer idx and a string label; no extra keys.
var outputSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["assignments"],
  "properties": {
    "assignments": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["segment_idx", "speaker_key"],
        "properties": {
          "segment_idx": { "type": "integer" },
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

// assignment is one decoded {segment_idx, speaker_key} pair from the model.
type assignment struct {
	SegmentIdx int    `json:"segment_idx"`
	SpeakerKey string `json:"speaker_key"`
}

// output is the model's decoded response.
type output struct {
	Assignments []assignment `json:"assignments"`
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
// the episode's language, sends the model the segments' idx+text, validates the
// grouping covers every segment exactly once, and returns idx -> speaker_key. It
// satisfies pipeline.Diarizer by duck typing (no pipeline import), so the diarize
// stage drives it through that neutral seam.
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

	parts, idxSet, err := buildRequest(segs)
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
		Temperature:   0,
		MaxTokens:     maxOutputTokens,
		OrgID:         orgID,
		EpisodeID:     episodeID,
		Out:           &out,
		// Validate runs after a successful strict decode; a non-nil error is treated
		// exactly like a decode failure by the Client (retry once, then hard fail).
		// This is where "assign every idx exactly once" is enforced against the real
		// segment set — an unknown idx, a gap, or an overlap all fail here.
		Validate: func() error { return validateAssignments(idxSet, out.Assignments) },
	})
	if err != nil {
		return nil, err
	}

	byIdx := make(map[int]string, len(out.Assignments))
	for _, a := range out.Assignments {
		byIdx[a.SegmentIdx] = a.SpeakerKey
	}
	return byIdx, nil
}

// buildRequest serializes the segments to the user-turn payload (idx + text ONLY)
// and returns the JSON string plus the set of idxs the output is validated
// against. Segments are emitted in idx order so the request is deterministic
// regardless of the caller's slice order.
func buildRequest(segs []asr.Segment) (string, map[int]bool, error) {
	ordered := make([]asr.Segment, len(segs))
	copy(ordered, segs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Idx < ordered[j].Idx })

	payload := requestPayload{Segments: make([]requestSegment, 0, len(ordered))}
	idxSet := make(map[int]bool, len(ordered))
	for _, s := range ordered {
		if idxSet[s.Idx] {
			return "", nil, fmt.Errorf("diarize: duplicate segment idx %d in transcript", s.Idx)
		}
		idxSet[s.Idx] = true
		// Only idx and text — never a timestamp or the words array.
		payload.Segments = append(payload.Segments, requestSegment{Idx: s.Idx, Text: s.Text})
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("diarize: marshal request: %w", err)
	}
	return string(b), idxSet, nil
}

// validateAssignments checks the model's grouping against the real segment set:
// every existing idx assigned exactly once (no gap, no overlap), no unknown idx,
// and a well-formed episode-local label. It is the semantic gate that turns a
// plausible-but-wrong grouping into the one-retry-then-fail path. Its error text
// is neutral (no provider names) — it is surfaced only in server logs.
func validateAssignments(idxSet map[int]bool, assigns []assignment) error {
	if len(assigns) == 0 {
		return errors.New("diarize: empty assignment")
	}
	seen := make(map[int]bool, len(assigns))
	for _, a := range assigns {
		if !idxSet[a.SegmentIdx] {
			return fmt.Errorf("diarize: assignment for unknown segment idx %d", a.SegmentIdx)
		}
		if seen[a.SegmentIdx] {
			return fmt.Errorf("diarize: segment idx %d assigned more than once", a.SegmentIdx)
		}
		if !speakerKeyRE.MatchString(a.SpeakerKey) {
			return fmt.Errorf("diarize: segment idx %d has malformed speaker label", a.SegmentIdx)
		}
		seen[a.SegmentIdx] = true
	}
	if len(seen) != len(idxSet) {
		return fmt.Errorf("diarize: grouping covers %d of %d segments (gap)", len(seen), len(idxSet))
	}
	return nil
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
