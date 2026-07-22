package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"blueshift/internal/auth"
	"blueshift/internal/blob"
	"blueshift/internal/ids"
)

// maxCreateBody bounds the create-episode request body (small JSON).
const maxCreateBody = 1 << 16

// maxMasterBytes caps a declared master size at 40 GiB, matching the design copy
// for the upload dialog. Anything larger is rejected before an upload URL is
// minted.
const maxMasterBytes int64 = 40 << 30

// maxTitleLen bounds the episode title.
const maxTitleLen = 300

// defaultLanguage is the content language stamped on new episodes until org
// config drives it (schema default is 'fa' too). No 'fa' assumptions live
// outside lang/fa; this is just the seed value.
const defaultLanguage = "fa"

// allowedContentTypes is the closed set of master container MIME types (mp4 /
// mov / mxf). Neutral, container-only — nothing here names a provider.
var allowedContentTypes = map[string]struct{}{
	"video/mp4":       {},
	"video/quicktime": {},
	"application/mxf": {},
	"video/mxf":       {},
}

// NewEpisode is the create-episode input the repo needs. It carries only
// validated, org-neutral fields; the org comes from the session principal, never
// from here.
type NewEpisode struct {
	Title          string
	SourceFilename string
	Language       string
	SizeBytes      int64
}

// EpisodeRow is the repo's view of an episode. Public ids are raw 16-byte UUID
// values so the handler renders them through /internal/ids; internal database
// ids never appear.
type EpisodeRow struct {
	OrgPublicID    [16]byte
	PublicID       [16]byte
	Title          string
	SourceFilename string
	Language       string
	Status         string
	SizeBytes      int64 // declared master size; 0 = unknown
	MasterKey      string
	CreatedAt      time.Time
}

// EpisodeRepo is the org-scoped persistence port the episode handlers depend on.
// Every method takes the principal's org public id and scopes its work to that
// org; a caller can never name another org's rows. GetEpisode and
// SetEpisodeMasterKey report found=false (not an error) when the episode is not
// visible to the org, which the handlers turn into a 404.
type EpisodeRepo interface {
	CreateEpisode(ctx context.Context, orgPublicID string, in NewEpisode) (EpisodeRow, error)
	GetEpisode(ctx context.Context, orgPublicID, episodePublicID string) (EpisodeRow, bool, error)
	SetEpisodeMasterKey(ctx context.Context, orgPublicID, episodePublicID, key string) (EpisodeRow, bool, error)
}

// createEpisodeRequest is the POST /api/episodes body.
type createEpisodeRequest struct {
	Title          string `json:"title"`
	SourceFilename string `json:"source_filename"`
	SizeBytes      int64  `json:"size_bytes"`
	ContentType    string `json:"content_type"`
}

// episodeDTO is the neutral episode projection returned to clients: prefixed
// public id, no internal ids, no storage key.
type episodeDTO struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	SourceFilename string `json:"source_filename"`
	Language       string `json:"language"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
}

func episodeDTOFrom(row EpisodeRow) episodeDTO {
	return episodeDTO{
		ID:             ids.Encode(ids.Episode, row.PublicID),
		Title:          row.Title,
		SourceFilename: row.SourceFilename,
		Language:       row.Language,
		Status:         row.Status,
		CreatedAt:      row.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// createEpisodeResponse pairs the created episode with the client's upload
// instructions.
type createEpisodeResponse struct {
	Episode episodeDTO  `json:"episode"`
	Upload  blob.Upload `json:"upload"`
}

// createEpisode validates the request, creates the org-scoped episode row
// (status 'uploaded', no master key yet), builds the master storage key from the
// public ids, and returns a narrowly-scoped upload URL.
func (h *handler) createEpisode(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"})
		return
	}

	var req createEpisodeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxCreateBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_request"})
		return
	}
	title := strings.TrimSpace(req.Title)
	filename := strings.TrimSpace(req.SourceFilename)
	if title == "" || len(title) > maxTitleLen || filename == "" {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_request"})
		return
	}
	if req.SizeBytes <= 0 || req.SizeBytes > maxMasterBytes {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_size"})
		return
	}
	if _, ok := allowedContentTypes[strings.ToLower(strings.TrimSpace(req.ContentType))]; !ok {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "unsupported_type"})
		return
	}
	// The filename must reduce to a usable key component; reject up front so we
	// never create a row we cannot build an upload URL for.
	if _, err := blob.SanitizeFilename(filename); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_filename"})
		return
	}

	row, err := h.deps.Episodes.CreateEpisode(r.Context(), p.OrgPublicID, NewEpisode{
		Title:          title,
		SourceFilename: filename,
		Language:       defaultLanguage,
		SizeBytes:      req.SizeBytes,
	})
	if err != nil {
		h.unavailable(w, r, "create episode failed", err)
		return
	}

	key, err := blob.MasterKey(ids.Encode(ids.Org, row.OrgPublicID), ids.Encode(ids.Episode, row.PublicID), filename)
	if err != nil {
		h.unavailable(w, r, "master key build failed", err)
		return
	}
	up, err := h.deps.Blob.InitResumableUpload(r.Context(), key, req.ContentType, req.SizeBytes)
	if err != nil {
		h.unavailable(w, r, "init upload failed", err)
		return
	}

	writeJSON(w, http.StatusCreated, createEpisodeResponse{Episode: episodeDTOFrom(row), Upload: up})
}

// uploadComplete verifies the uploaded master exists and matches the declared
// size, then records the master object key. It leaves status at 'uploaded' (the
// worker advances it later). Missing or short object -> 409.
func (h *handler) uploadComplete(w http.ResponseWriter, r *http.Request) {
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

	key, err := blob.MasterKey(ids.Encode(ids.Org, row.OrgPublicID), ids.Encode(ids.Episode, row.PublicID), row.SourceFilename)
	if err != nil {
		h.unavailable(w, r, "master key build failed", err)
		return
	}
	size, err := h.deps.Blob.Stat(r.Context(), key)
	if errors.Is(err, blob.ErrNotFound) {
		writeJSON(w, http.StatusConflict, errBody{Error: "upload_incomplete"})
		return
	}
	if err != nil {
		h.unavailable(w, r, "stat object failed", err)
		return
	}
	if row.SizeBytes > 0 && size != row.SizeBytes {
		writeJSON(w, http.StatusConflict, errBody{Error: "upload_incomplete"})
		return
	}

	updated, found, err := h.deps.Episodes.SetEpisodeMasterKey(r.Context(), p.OrgPublicID, episodeID, key)
	if err != nil {
		h.unavailable(w, r, "record master key failed", err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}

	// Launch the ingest stage. This is best-effort: the master is safely
	// recorded, so a trigger failure is logged server-side (neutrally) and the
	// upload still reports success — the worker can be re-driven — rather than
	// telling the client their finished upload failed.
	if h.deps.Trigger != nil {
		if err := h.deps.Trigger.Trigger(r.Context(), episodeID, ingestStage); err != nil {
			id := errorID()
			h.deps.Logger.LogAttrs(r.Context(), slog.LevelError, "worker trigger failed",
				slog.String("error_id", id), slog.String("error", err.Error()))
		}
	}

	writeJSON(w, http.StatusOK, episodeDTOFrom(updated))
}

// ingestStage is the stage name the upload-complete trigger launches. Kept as a
// neutral local constant so this package does not import the pipeline registry.
const ingestStage = "ingest"

// unavailable logs the raw cause server-side with a correlation id and returns
// the neutral 503 envelope, matching the auth handlers.
func (h *handler) unavailable(w http.ResponseWriter, r *http.Request, msg string, err error) {
	id := errorID()
	h.deps.Logger.LogAttrs(r.Context(), slog.LevelError, msg,
		slog.String("error_id", id), slog.String("error", err.Error()))
	writeJSON(w, http.StatusServiceUnavailable, errIDBody{Error: "unavailable", ErrorID: id})
}
