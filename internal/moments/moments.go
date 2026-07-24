// Package moments is the LLM moment selector: it proposes an episode's most
// clip-worthy moments by asking a language model (through the /internal/llm
// seam) to pick and rank segment spans from the transcript. It is diarize's
// sibling — the same engine shape, the same seams, the same import-cycle
// placement (internal/store implements pipeline.Repo and internal/llm audits
// through store, so pipeline cannot import llm; the LLM wiring stays on this
// side of the neutral pipeline.MomentSelector seam). Unlike diarize this engine
// returns structs, not a builtin map, so the seam's value types live in
// internal/pipeline and this package imports them — the dependency arrow still
// points the safe way (pipeline never imports this package).
//
// Two invariants are enforced here:
//
//   - Verbatim quotes (CLAUDE.md — "LLMs decide, they never measure"). The model
//     may see segment times (it cites spans, it never invents times) but its
//     output references segment idxs ONLY, and quote_fa MUST be a verbatim
//     contiguous substring of the span's joined segment text. The output is
//     validated for 3..8 moments (clamped to the transcript size for tiny
//     transcripts), contiguous ranks from 1, valid non-overlapping idx spans,
//     the substring property, AND word alignment: the quote must locate within
//     the span's ASR word sequence (asr.LocateQuote, the same joinWords rule as
//     resegmentation), because the stage derives the moment's word-accurate
//     start_ms/end_ms from exactly that alignment. An invalid output takes the
//     /internal/llm one-retry-then-hard-fail path.
//
//   - Vendor-neutral. The engine is selected by a neutral label the lang
//     registry declares for the content language; no provider name appears in
//     the prompt, the schema, the returned proposals, or any error. Every call
//     is audited in llm_calls behind the llm seam.
package moments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"blueshift/internal/asr"
	"blueshift/internal/lang"
	"blueshift/internal/llm"
	"blueshift/internal/pipeline"
)

// promptID and promptVersion identify the moment-selection prompt template for
// the llm_calls audit. Bump the version on any change to the prompt or schema
// (a new version is a new audited contract).
const (
	promptID      = "moments.rank"
	promptVersion = "v1"
	// maxOutputTokens caps the selection output. At most eight small objects;
	// this is a generous ceiling that never truncates a real proposal set.
	maxOutputTokens = 8192
	// minMoments/maxMoments bound the proposal count (the SPEC's 3..8 window).
	// The lower bound clamps to the transcript size: a transcript of n < 3
	// segments can hold at most n non-overlapping spans, so demanding 3 would
	// make every tiny episode (e.g. the two-segment demo fixture) unprocessable.
	minMoments = 3
	maxMoments = 8
)

// systemPrompt instructs the model to select and rank clip-worthy moments as
// segment spans. It names no provider. The quote rule mirrors the validator:
// copied verbatim, contiguous, from the span's own text — the model selects, it
// never rewrites, translates, or re-times anything.
const systemPrompt = `You are a moment selector for a long-form interview transcript, picking the spans most worth cutting into short social clips.

You are given the transcript as an ordered list of segments, each with an integer "idx", its verbatim "text", its measured "start_ms"/"end_ms", and (when known) a "speaker_key". Choose between 3 and 8 moments (fewer only if the transcript has fewer segments than that), each a contiguous, non-overlapping span of segments given as "start_idx".."end_idx" (inclusive). Prefer self-contained, quotable, emotionally or informationally strong beats.

Rank the moments best-first with "rank" starting at 1 and counting up without gaps. For each moment write "rationale_en": one or two ENGLISH sentences on why it makes a strong clip, and "quote_fa": the single most quotable line of the span, COPIED VERBATIM as a contiguous substring of the span's segment text — do not translate, paraphrase, re-punctuate, or alter it in any way.

Reference segments ONLY by their idx values. Never output timestamps, never alter the transcript, and never invent text that is not in it.`

// outputSchema is the provider-agnostic JSON schema the output is constrained
// by (server-side) and strict-decoded against (locally): ranked moments with an
// integer span and the two text fields; no extra keys.
var outputSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["moments"],
  "properties": {
    "moments": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["rank", "start_idx", "end_idx", "rationale_en", "quote_fa"],
        "properties": {
          "rank": { "type": "integer" },
          "start_idx": { "type": "integer" },
          "end_idx": { "type": "integer" },
          "rationale_en": { "type": "string" },
          "quote_fa": { "type": "string" }
        }
      }
    }
  }
}`)

// requestSegment is one segment as sent to the model: idx, verbatim text, the
// ASR-measured times (context the model may cite spans by — it never invents
// times, and its OUTPUT references idxs only), and the diarization speaker_key
// when known.
type requestSegment struct {
	Idx        int    `json:"idx"`
	Text       string `json:"text"`
	StartMs    int    `json:"start_ms"`
	EndMs      int    `json:"end_ms"`
	SpeakerKey string `json:"speaker_key,omitempty"`
}

// requestPayload is the user turn: the ordered segments to select from.
type requestPayload struct {
	Segments []requestSegment `json:"segments"`
}

// proposal is one decoded moment from the model.
type proposal struct {
	Rank        int    `json:"rank"`
	StartIdx    int    `json:"start_idx"`
	EndIdx      int    `json:"end_idx"`
	RationaleEn string `json:"rationale_en"`
	QuoteFa     string `json:"quote_fa"`
}

// output is the model's decoded response.
type output struct {
	Moments []proposal `json:"moments"`
}

// Generator is the /internal/llm seam: one schema-constrained, audited
// generation with the one-retry-then-hard-fail loop. *llm.Client satisfies it.
type Generator interface {
	Generate(ctx context.Context, req llm.Request) (llm.Response, error)
}

// LabelResolver resolves the neutral LLM engine label for a content language.
// The concrete engine behind the label is bound at wiring time from config;
// provider choice never crosses this seam.
type LabelResolver interface {
	LabelFor(ctx context.Context, language string) (string, error)
}

// Engine is the LLM moment selector. It resolves the neutral engine label for
// the episode's language, sends the model the idx-ordered transcript, validates
// the proposal set (count window, contiguous ranks, valid non-overlapping
// spans, verbatim quotes), and returns the proposals rank-ordered. It satisfies
// pipeline.MomentSelector.
type Engine struct {
	// Gen is the audited, schema-constrained LLM seam (a *llm.Client in
	// production, a fake-backed Client in tests and the golden eval).
	Gen Generator
	// Labels resolves the neutral engine label for a content language.
	Labels LabelResolver
}

// The engine is the production MomentSelector the pipeline drives through the
// neutral seam — guard the contract at compile time.
var _ pipeline.MomentSelector = Engine{}

// SelectMoments proposes the episode's ranked moments from segs (idx-ordered).
// orgID and episodeID are the INTERNAL ids the llm_calls audit is scoped by. A
// neutral llm sentinel (llm.ErrInvalidOutput after one retry on an invalid
// proposal set, llm.ErrUnavailable on an unreachable engine) is returned
// unwrapped for the stage to treat as a failure — no provider text ever leaks.
func (e Engine) SelectMoments(ctx context.Context, language string, orgID, episodeID int64, segs []pipeline.MomentSegment) ([]pipeline.ProposedMoment, error) {
	if e.Gen == nil {
		return nil, errors.New("moments: no llm generator configured")
	}
	if e.Labels == nil {
		return nil, errors.New("moments: no engine label resolver configured")
	}
	if len(segs) == 0 {
		return nil, errors.New("moments: no segments to select from")
	}

	label, err := e.Labels.LabelFor(ctx, language)
	if err != nil {
		return nil, err
	}

	parts, ordered, err := buildRequest(segs)
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
		// Validate runs after a successful strict decode; a non-nil error is
		// treated exactly like a decode failure by the Client (retry once, then
		// hard fail). This is where the count window, contiguous ranks, span
		// validity/non-overlap, and the VERBATIM QUOTE substring property are
		// enforced against the real transcript.
		Validate: func() error { return validateMoments(ordered, out.Moments) },
	})
	if err != nil {
		return nil, err
	}

	props := make([]pipeline.ProposedMoment, 0, len(out.Moments))
	for _, m := range out.Moments {
		props = append(props, pipeline.ProposedMoment{
			Rank:        m.Rank,
			StartIdx:    m.StartIdx,
			EndIdx:      m.EndIdx,
			RationaleEn: m.RationaleEn,
			QuoteFa:     m.QuoteFa,
		})
	}
	sort.Slice(props, func(i, j int) bool { return props[i].Rank < props[j].Rank })
	return props, nil
}

// buildRequest serializes the segments to the user-turn payload and returns the
// JSON string plus the idx-ordered segment slice the output is validated
// against. Segments are emitted in idx order so the request is deterministic
// regardless of the caller's slice order; a duplicate idx is a hard error.
func buildRequest(segs []pipeline.MomentSegment) (string, []pipeline.MomentSegment, error) {
	ordered := make([]pipeline.MomentSegment, len(segs))
	copy(ordered, segs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Idx < ordered[j].Idx })

	payload := requestPayload{Segments: make([]requestSegment, 0, len(ordered))}
	seen := make(map[int]bool, len(ordered))
	for _, s := range ordered {
		if seen[s.Idx] {
			return "", nil, fmt.Errorf("moments: duplicate segment idx %d in transcript", s.Idx)
		}
		seen[s.Idx] = true
		payload.Segments = append(payload.Segments, requestSegment{
			Idx:        s.Idx,
			Text:       s.Text,
			StartMs:    s.StartMs,
			EndMs:      s.EndMs,
			SpeakerKey: s.SpeakerKey,
		})
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("moments: marshal request: %w", err)
	}
	return string(b), ordered, nil
}

// validateMoments checks the model's proposal set against the real transcript:
//
//   - count inside the 3..8 window, with the lower bound clamped to the segment
//     count (a transcript of n < 3 segments admits at most n non-overlapping
//     spans); an empty set is always invalid;
//   - ranks exactly 1..n, contiguous, no duplicate;
//   - every span well-formed (start_idx <= end_idx) over EXISTING segment idxs,
//     and no two spans overlapping;
//   - rationale_en non-blank;
//   - quote_fa non-blank and a VERBATIM CONTIGUOUS SUBSTRING of the span's
//     joined segment text (segments joined with a single space) — the verbatim
//     invariant: the model selects a quote, it never rewrites one;
//   - quote_fa ALIGNS to the span's word sequence (asr.LocateQuote under the
//     same joinWords rule): the stage derives the moment's word-accurate
//     start_ms/end_ms from that alignment, so a quote the word data cannot
//     locate — drifted word text, or a span without word timings — is invalid
//     OUTPUT here, where the retry can still fix it, never a stage crash later.
//
// It is the semantic gate that turns a plausible-but-wrong proposal set into
// the one-retry-then-fail path. Its error text is neutral (no provider names) —
// it is surfaced only in server logs. ordered is the idx-ordered transcript
// from buildRequest.
func validateMoments(ordered []pipeline.MomentSegment, props []proposal) error {
	if len(props) == 0 {
		return errors.New("moments: empty proposal set")
	}
	minWant := minMoments
	if len(ordered) < minMoments {
		minWant = len(ordered)
	}
	if len(props) < minWant || len(props) > maxMoments {
		return fmt.Errorf("moments: %d moments proposed, want %d..%d", len(props), minWant, maxMoments)
	}
	return validateProposalSet(ordered, props)
}

// validateProposalSet is the count-agnostic core shared by the stage validator
// (validateMoments, which additionally clamps the 3..8 window) and the compose
// validator (validateComposed, where an EMPTY set is a valid "no matches"
// answer): ranks exactly 1..n, spans well-formed/known/non-overlapping,
// rationale non-blank, and the quote verbatim + word-aligned. It accepts an
// empty set (nothing to check).
func validateProposalSet(ordered []pipeline.MomentSegment, props []proposal) error {
	// Ranks: exactly 1..n, no duplicate, no gap.
	ranks := make(map[int]bool, len(props))
	for _, p := range props {
		if p.Rank < 1 || p.Rank > len(props) {
			return fmt.Errorf("moments: rank %d outside 1..%d", p.Rank, len(props))
		}
		if ranks[p.Rank] {
			return fmt.Errorf("moments: duplicate rank %d", p.Rank)
		}
		ranks[p.Rank] = true
	}

	// Spans: well-formed, over existing idxs, non-overlapping.
	byIdx := make(map[int]pipeline.MomentSegment, len(ordered))
	for _, s := range ordered {
		byIdx[s.Idx] = s
	}
	spans := make([]proposal, len(props))
	copy(spans, props)
	sort.Slice(spans, func(i, j int) bool { return spans[i].StartIdx < spans[j].StartIdx })
	prevEnd := -1
	first := true
	for _, p := range spans {
		if p.StartIdx > p.EndIdx {
			return fmt.Errorf("moments: rank %d span %d..%d is inverted", p.Rank, p.StartIdx, p.EndIdx)
		}
		for i := p.StartIdx; i <= p.EndIdx; i++ {
			if _, ok := byIdx[i]; !ok {
				return fmt.Errorf("moments: rank %d span %d..%d covers unknown segment idx %d", p.Rank, p.StartIdx, p.EndIdx, i)
			}
		}
		if !first && p.StartIdx <= prevEnd {
			return fmt.Errorf("moments: rank %d span %d..%d overlaps another span", p.Rank, p.StartIdx, p.EndIdx)
		}
		first = false
		prevEnd = p.EndIdx

		if strings.TrimSpace(p.RationaleEn) == "" {
			return fmt.Errorf("moments: rank %d has a blank rationale", p.Rank)
		}
		// The verbatim-quote gate: quote_fa must appear, byte-for-byte, inside the
		// span's joined text. Join with a single space — the same separator a
		// reader of consecutive segments would use; a quote may cross a segment
		// boundary only through it.
		if strings.TrimSpace(p.QuoteFa) == "" {
			return fmt.Errorf("moments: rank %d has a blank quote", p.Rank)
		}
		texts := make([]string, 0, p.EndIdx-p.StartIdx+1)
		span := make([]asr.Segment, 0, p.EndIdx-p.StartIdx+1)
		for i := p.StartIdx; i <= p.EndIdx; i++ {
			texts = append(texts, byIdx[i].Text)
			span = append(span, byIdx[i].Segment)
		}
		if !strings.Contains(strings.Join(texts, " "), p.QuoteFa) {
			return fmt.Errorf("moments: rank %d quote is not a verbatim substring of its span text", p.Rank)
		}
		// Word alignment: the same lookup the stage performs to derive the
		// moment's word-accurate times must succeed here, so a misaligned quote
		// hits the retry path instead of failing the stage after a paid call.
		if _, _, err := asr.LocateQuote(span, p.QuoteFa); err != nil {
			return fmt.Errorf("moments: rank %d quote does not align to the span's word data: %w", p.Rank, err)
		}
	}
	return nil
}

// LangLabelResolver resolves the neutral LLM engine label for a content
// language from the lang registry: the language must be registered and must
// declare an llm engine slot (EngineLLM), then the wiring-bound neutral Label
// is returned. An unregistered language, or one that declares no llm slot, is
// an explicit error — never a silent default. It mirrors internal/diarize's
// resolver.
type LangLabelResolver struct {
	// Label is the neutral engine label bound to the llm slot (e.g. "bs-lm-1").
	// It never carries a provider name.
	Label string
}

var _ LabelResolver = LangLabelResolver{}

// LabelFor gates the language through the lang registry, verifies it declares
// an llm engine slot, and returns the bound neutral label.
func (r LangLabelResolver) LabelFor(_ context.Context, language string) (string, error) {
	l, err := lang.Get(language)
	if err != nil {
		return "", fmt.Errorf("moments: language %q not registered: %w", language, err)
	}
	if !declaresEngine(l, lang.EngineLLM) {
		return "", fmt.Errorf("moments: language %q declares no llm engine slot", language)
	}
	if r.Label == "" {
		return "", errors.New("moments: no llm engine label configured")
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
