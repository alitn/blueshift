package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"blueshift/internal/auth"
	"blueshift/internal/ids"
)

// Moment review statuses. They mirror the moments.status CHECK set; the store
// is the source of truth, these are the handler's validation guards so a bad
// client status is a 400 here and never reaches the database.
const (
	momentStatusProposed  = "proposed"
	momentStatusApproved  = "approved"
	momentStatusDismissed = "dismissed"
)

// maxStatusBody bounds the set-status request body (tiny JSON).
const maxStatusBody = 1 << 12

// EpisodeMoment is the repo's view of one ranked moment: the best-first rank
// (the moment's natural key within its episode), the inclusive segment-idx
// span, the quote-aligned ASR window, the validated texts, and the review
// status. StatusChangedAt is the zero time until a human first flips the
// status; it is repo material only and never serialized. No internal id, no
// provider detail — the moment is pure content.
type EpisodeMoment struct {
	Rank            int
	StartIdx        int
	EndIdx          int
	StartMs         int
	EndMs           int
	RationaleEn     string
	QuoteFa         string
	Status          string
	StatusChangedAt time.Time
}

// momentDTO is the neutral per-moment projection: rank, span, ASR-derived
// window, verbatim texts, and the review status. Exactly these keys — nothing
// about the engine that proposed the moment ever appears.
type momentDTO struct {
	Rank        int    `json:"rank"`
	StartIdx    int    `json:"start_idx"`
	EndIdx      int    `json:"end_idx"`
	StartMs     int    `json:"start_ms"`
	EndMs       int    `json:"end_ms"`
	RationaleEn string `json:"rationale_en"`
	QuoteFa     string `json:"quote_fa"`
	Status      string `json:"status"`
}

// momentsDTO is the neutral moments envelope: the prefixed public episode id
// and the rank-ordered moments. Moments is always an array — [] for an episode
// whose moments stage has not produced proposals yet (a 200, so the UI renders
// the "awaiting moments" state), never null.
type momentsDTO struct {
	EpisodeID string      `json:"episode_id"`
	Moments   []momentDTO `json:"moments"`
}

func momentDTOFrom(m EpisodeMoment) momentDTO {
	return momentDTO{
		Rank:        m.Rank,
		StartIdx:    m.StartIdx,
		EndIdx:      m.EndIdx,
		StartMs:     m.StartMs,
		EndMs:       m.EndMs,
		RationaleEn: m.RationaleEn,
		QuoteFa:     m.QuoteFa,
		Status:      m.Status,
	}
}

func momentsDTOFrom(row EpisodeRow, moments []EpisodeMoment) momentsDTO {
	out := momentsDTO{
		EpisodeID: ids.Encode(ids.Episode, row.PublicID),
		Moments:   make([]momentDTO, 0, len(moments)),
	}
	for _, m := range moments {
		out.Moments = append(out.Moments, momentDTOFrom(m))
	}
	return out
}

// episodeMoments returns an episode's ranked moments for the moment rail. It
// is auth-required and org-scoped exactly like the transcript read: existence
// is established by the same GetEpisode gate, so an episode not visible to the
// principal's org is an indistinguishable 404 (never a cross-tenant read), and
// a visible episode with no moments yet is a 200 with an empty array.
func (h *handler) episodeMoments(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"})
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

	moments, err := h.deps.Episodes.EpisodeMoments(r.Context(), p.OrgPublicID, episodeID)
	if err != nil {
		h.unavailable(w, r, "get moments failed", err)
		return
	}
	writeJSON(w, http.StatusOK, momentsDTOFrom(row, moments))
}

// setMomentStatusRequest is the POST .../moments/{rank}/status body.
type setMomentStatusRequest struct {
	Status string `json:"status"`
}

// setMomentStatus flips one moment's review status: proposed -> approved or
// dismissed, and approved/dismissed -> proposed (the undo). The moment is
// addressed by (episode, rank) — its stable natural key; no other addressing
// exists. Org-scoped like every episode sub-route (foreign/unknown episode is
// a 404). An unknown rank is a 404; an illegal transition (approved ->
// dismissed, or a same-status no-op) is a 409; success is a 200 with the
// updated moment. The store stamps status_changed_at on every flip — a fuller
// approvals audit trail (who, from-what) is a later task, deliberately not
// smuggled in here.
func (h *handler) setMomentStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"})
		return
	}
	episodeID := r.PathValue("id")

	// The rank path segment must be a positive integer; anything else names no
	// moment, which is indistinguishable from an unknown rank (404).
	rank, err := strconv.Atoi(r.PathValue("rank"))
	if err != nil || rank < 1 {
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}

	var req setMomentStatusRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxStatusBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_request"})
		return
	}
	switch req.Status {
	case momentStatusProposed, momentStatusApproved, momentStatusDismissed:
	default:
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_status"})
		return
	}

	_, found, err := h.deps.Episodes.GetEpisode(r.Context(), p.OrgPublicID, episodeID)
	if err != nil {
		h.unavailable(w, r, "get episode failed", err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}

	flipped, err := h.deps.Episodes.SetMomentStatus(r.Context(), p.OrgPublicID, episodeID, rank, req.Status)
	if err != nil {
		h.unavailable(w, r, "set moment status failed", err)
		return
	}

	// One rank-scoped read serves both outcomes: the 200 body on success, and
	// the 404-vs-409 split on refusal (the store's clean false does not say
	// which; an absent rank is a 404, a present one refused is an illegal
	// transition, 409).
	moments, err := h.deps.Episodes.EpisodeMoments(r.Context(), p.OrgPublicID, episodeID)
	if err != nil {
		h.unavailable(w, r, "get moments failed", err)
		return
	}
	var updated *EpisodeMoment
	for i := range moments {
		if moments[i].Rank == rank {
			updated = &moments[i]
			break
		}
	}
	if !flipped {
		if updated == nil {
			writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
			return
		}
		writeJSON(w, http.StatusConflict, errBody{Error: "invalid_transition"})
		return
	}
	if updated == nil {
		// Flipped, then the set was replaced out from under us (a reprocess).
		// The moment the caller flipped no longer exists.
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}
	writeJSON(w, http.StatusOK, momentDTOFrom(*updated))
}
