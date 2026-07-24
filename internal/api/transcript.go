package api

import (
	"encoding/json"
	"net/http"

	"blueshift/internal/auth"
	"blueshift/internal/ids"
)

// TranscriptWord is the repo's view of one recognised token: verbatim text and
// integer-millisecond timing plus the engine confidence. It is the neutral port
// shape the handler renders as the positional wire tuple [text, start_ms, end_ms,
// conf]; nothing here names a provider.
type TranscriptWord struct {
	Text    string
	StartMs int
	EndMs   int
	Conf    float64
}

// TranscriptSegment is the repo's view of one transcript segment: its ordinal,
// span, verbatim text, the additive diarization speaker_key ("" until diarized),
// and the token-level words. Public/internal ids and storage keys never appear —
// the transcript is pure content.
type TranscriptSegment struct {
	Idx        int
	StartMs    int
	EndMs      int
	Text       string
	SpeakerKey string // "" until the diarize stage assigns one
	Words      []TranscriptWord
}

// wordTuple serializes a word as the positional JSON array the transcript wire
// contract (and the segments.words storage shape) uses: [text, start_ms, end_ms,
// conf]. A positional tuple keeps a full word transcript compact — a ~1 h
// interview is hundreds–low-thousands of segments, each with its words.
type wordTuple struct {
	Text    string
	StartMs int
	EndMs   int
	Conf    float64
}

// MarshalJSON renders the tuple as a heterogeneous array, not an object.
func (t wordTuple) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{t.Text, t.StartMs, t.EndMs, t.Conf})
}

// transcriptSegmentDTO is the neutral per-segment projection. SpeakerKey is a
// pointer so an un-diarized segment serializes as "speaker_key": null (the UI
// renders "awaiting speakers") rather than an empty string. Words is always an
// array — [] for a segment with no tokens, never null.
type transcriptSegmentDTO struct {
	Idx        int         `json:"idx"`
	StartMs    int         `json:"start_ms"`
	EndMs      int         `json:"end_ms"`
	Text       string      `json:"text"`
	SpeakerKey *string     `json:"speaker_key"`
	Words      []wordTuple `json:"words"`
}

// transcriptDTO is the neutral transcript envelope: the prefixed public episode
// id, the content language, and the idx-ordered segments. No internal id, no
// storage key, no provider name. Segments is always an array — [] for an episode
// whose transcript has not been produced yet (a 200, so the UI can render an
// "awaiting transcript" state), never null.
//
// M1 returns the full ordered set in one response. A ~1 h interview is
// hundreds–low-thousands of segments with word arrays, which is comfortably
// within one JSON payload; cursor pagination is deferred until a real payload
// proves too large (out of scope now — see tasks/m1-segments-api.md).
type transcriptDTO struct {
	EpisodeID string                 `json:"episode_id"`
	Language  string                 `json:"language"`
	Segments  []transcriptSegmentDTO `json:"segments"`
}

func transcriptDTOFrom(row EpisodeRow, segs []TranscriptSegment) transcriptDTO {
	out := transcriptDTO{
		EpisodeID: ids.Encode(ids.Episode, row.PublicID),
		Language:  row.Language,
		Segments:  make([]transcriptSegmentDTO, 0, len(segs)),
	}
	for _, s := range segs {
		seg := transcriptSegmentDTO{
			Idx:     s.Idx,
			StartMs: s.StartMs,
			EndMs:   s.EndMs,
			Text:    s.Text,
			Words:   make([]wordTuple, 0, len(s.Words)),
		}
		if s.SpeakerKey != "" {
			sk := s.SpeakerKey
			seg.SpeakerKey = &sk
		}
		for _, w := range s.Words {
			seg.Words = append(seg.Words, wordTuple(w))
		}
		out.Segments = append(out.Segments, seg)
	}
	return out
}

// episodeTranscript returns an episode's transcript (segments ordered by idx) for
// the transcript editor. It is auth-required and org-scoped: existence is
// established by the same GetEpisode gate the other episode sub-routes use, so an
// episode not visible to the principal's org is an indistinguishable 404 (never a
// cross-tenant read), and a visible episode with no segments yet is a 200 with an
// empty segments array (not an error).
func (h *handler) episodeTranscript(w http.ResponseWriter, r *http.Request) {
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

	segs, err := h.deps.Episodes.EpisodeTranscript(r.Context(), p.OrgPublicID, episodeID)
	if err != nil {
		h.unavailable(w, r, "get transcript failed", err)
		return
	}
	writeJSON(w, http.StatusOK, transcriptDTOFrom(row, segs))
}
