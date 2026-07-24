package api

// compose.go is the free-prompt moment composition surface:
//
//	POST /api/episodes/{id}/moments/compose  {"prompt": "..."}
//	POST /api/episodes/{id}/moments/keep     {"start_idx", "end_idx", "rationale_en", "quote_fa"}
//
// Compose runs ONE audited engine call over the episode's own transcript and
// returns an EPHEMERAL moments-shaped list — nothing is persisted, and an
// empty list is a valid "no matches" answer. Keep persists one composed
// result as a real moment (approve-to-keep): the seam re-validates the quote
// verbatim against the CURRENT transcript and re-derives the word-accurate
// times server-side, so nothing the client sends is ever believed for
// timing or text placement (verbatim invariant).
//
// The user's prompt is treated strictly as DATA: the seam passes it inside an
// instruction frame that pins the output contract, and the same validation as
// the pipeline stage runs regardless of what the prompt says (prompt-injection
// posture). Every DTO and error here is neutral — the vendor-leak gate greps
// this package.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"blueshift/internal/auth"
	"blueshift/internal/ids"
)

const (
	// maxComposeBody bounds the compose/keep request bodies (small JSON; the
	// keep body carries a transcript quote, still tiny).
	maxComposeBody = 1 << 14
	// maxComposePromptChars caps the free prompt (in runes — Persian prompts
	// are multi-byte, and the cap is about human-scale intent, not bytes).
	maxComposePromptChars = 500
	// defaultComposePerMin is the per-org compose rate (token bucket): the
	// compose call is externally billable, so it is bounded structurally even
	// under a misbehaving client (repo standing rule: billable-service cost
	// safety).
	defaultComposePerMin = 6
)

// Sentinel errors the MomentComposer seam classifies into. They carry no
// provider or transcript detail — the handlers map them to neutral status
// codes and the raw causes stay in server logs.
var (
	// ErrNotTranscribed means the episode has no transcript segments yet, so
	// there is nothing to compose over (the 409 "not transcribed" refusal).
	ErrNotTranscribed = errors.New("api: episode has no transcript yet")
	// ErrInvalidComposedMoment means a keep request's span/quote no longer
	// matches the episode's current transcript (or never did) — a clean 409
	// refusal, never a persist of unverified text or times.
	ErrInvalidComposedMoment = errors.New("api: composed moment does not match the transcript")
)

// ComposedMoment is one EPHEMERAL compose result: moments-shaped (rank within
// the result set, inclusive segment span, quote-aligned word-accurate ASR
// window, English rationale, verbatim Persian quote) but never persisted. No
// status — it is not a reviewed row.
type ComposedMoment struct {
	Rank        int
	StartIdx    int
	EndIdx      int
	StartMs     int
	EndMs       int
	RationaleEn string
	QuoteFa     string
}

// ComposedMomentInput is what a keep request asserts: the span and texts of a
// previously composed result. Times are deliberately absent — the seam
// re-derives them from the ASR word data; the client never supplies a
// timestamp that survives.
type ComposedMomentInput struct {
	StartIdx    int
	EndIdx      int
	RationaleEn string
	QuoteFa     string
}

// MomentComposer is the free-prompt composition port. The concrete seam
// (internal/moments.Composer) runs the audited, schema-validated engine call
// with the user prompt framed as data, validates exactly like the pipeline
// stage (verbatim quotes, word alignment; an EMPTY result set is valid), and
// derives word-accurate times. found=false for an episode not visible to the
// org (the indistinguishable 404); ErrNotTranscribed when the episode has no
// segments yet; ErrInvalidComposedMoment when a keep's span/quote does not
// match the current transcript. Provider detail never crosses this port.
type MomentComposer interface {
	// ComposeMoments runs one prompt over the episode's transcript and returns
	// the ephemeral ranked results (possibly empty — a valid "no matches").
	ComposeMoments(ctx context.Context, orgPublicID, episodePublicID, prompt string) ([]ComposedMoment, bool, error)
	// KeepComposedMoment persists one composed result as a real moment at the
	// episode's next free rank (source='prompt', approved), returning the
	// persisted moment.
	KeepComposedMoment(ctx context.Context, orgPublicID, episodePublicID string, in ComposedMomentInput) (EpisodeMoment, bool, error)
}

// composeRequest is the POST .../moments/compose body.
type composeRequest struct {
	Prompt string `json:"prompt"`
}

// composedMomentDTO is the neutral per-result projection: exactly the
// moments-card fields, no status (ephemeral, unreviewed), nothing else.
type composedMomentDTO struct {
	Rank        int    `json:"rank"`
	StartIdx    int    `json:"start_idx"`
	EndIdx      int    `json:"end_idx"`
	StartMs     int    `json:"start_ms"`
	EndMs       int    `json:"end_ms"`
	RationaleEn string `json:"rationale_en"`
	QuoteFa     string `json:"quote_fa"`
}

// composeResponse is the neutral compose envelope. Moments is always an array
// — [] for a valid "no matches" — never null.
type composeResponse struct {
	EpisodeID string              `json:"episode_id"`
	Moments   []composedMomentDTO `json:"moments"`
}

// keepRequest is the POST .../moments/keep body: the span + texts of a
// composed result. No times — the server re-derives them.
type keepRequest struct {
	StartIdx    *int   `json:"start_idx"`
	EndIdx      *int   `json:"end_idx"`
	RationaleEn string `json:"rationale_en"`
	QuoteFa     string `json:"quote_fa"`
}

// composeMoments handles POST /api/episodes/{id}/moments/compose: one
// user-initiated, rate-limited, audited engine call over the episode's own
// transcript, returning an ephemeral moments-shaped result list. Auth and
// org-scoping mirror every episode sub-route (foreign/unknown episode is an
// indistinguishable 404). An untranscribed episode is a 409 with a neutral
// code; an engine failure is the neutral unavailable envelope — no provider
// detail, no transcript excerpt, ever.
func (h *handler) composeMoments(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"})
		return
	}
	// Per-org token bucket: compose is the one client-triggered billable call,
	// so it is bounded before any work happens (cost-safety standing rule).
	if !h.composeLimiter.allow(p.OrgPublicID) {
		writeJSON(w, http.StatusTooManyRequests, errBody{Error: "rate_limited"})
		return
	}

	var req composeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxComposeBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_request"})
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_request"})
		return
	}
	if utf8.RuneCountInString(prompt) > maxComposePromptChars {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "prompt_too_long"})
		return
	}

	episodeID := r.PathValue("id")
	row, found, err := h.deps.Episodes.GetEpisode(r.Context(), p.OrgPublicID, episodeID)
	if err != nil {
		h.unavailable(w, r, "get episode failed", err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}

	results, found, err := h.deps.Composer.ComposeMoments(r.Context(), p.OrgPublicID, episodeID, prompt)
	if err != nil {
		if errors.Is(err, ErrNotTranscribed) {
			writeJSON(w, http.StatusConflict, errBody{Error: "not_transcribed"})
			return
		}
		h.unavailable(w, r, "compose moments failed", err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}

	out := composeResponse{
		EpisodeID: ids.Encode(ids.Episode, row.PublicID),
		Moments:   make([]composedMomentDTO, 0, len(results)),
	}
	for _, m := range results {
		out.Moments = append(out.Moments, composedMomentDTO(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// keepComposedMoment handles POST /api/episodes/{id}/moments/keep: persist one
// composed result as a real moment (approve-to-keep). The seam re-validates
// the span/quote against the CURRENT transcript and re-derives the times —
// a stale or fabricated body is a clean 409 refusal, never a bad row. On
// success the persisted moment (next free rank, approved) is returned and from
// then on it behaves like any other moment.
func (h *handler) keepComposedMoment(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"})
		return
	}

	var req keepRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxComposeBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_request"})
		return
	}
	if req.StartIdx == nil || req.EndIdx == nil || *req.StartIdx < 0 || *req.EndIdx < *req.StartIdx ||
		strings.TrimSpace(req.RationaleEn) == "" || strings.TrimSpace(req.QuoteFa) == "" {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_request"})
		return
	}

	episodeID := r.PathValue("id")
	_, found, err := h.deps.Episodes.GetEpisode(r.Context(), p.OrgPublicID, episodeID)
	if err != nil {
		h.unavailable(w, r, "get episode failed", err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}

	m, found, err := h.deps.Composer.KeepComposedMoment(r.Context(), p.OrgPublicID, episodeID, ComposedMomentInput{
		StartIdx:    *req.StartIdx,
		EndIdx:      *req.EndIdx,
		RationaleEn: req.RationaleEn,
		QuoteFa:     req.QuoteFa,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidComposedMoment), errors.Is(err, ErrNotTranscribed):
			writeJSON(w, http.StatusConflict, errBody{Error: "invalid_moment"})
		default:
			h.unavailable(w, r, "keep composed moment failed", err)
		}
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}
	writeJSON(w, http.StatusOK, momentDTOFrom(m))
}
