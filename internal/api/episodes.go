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
	DurationMs     int64 // measured proxy duration; 0 = not yet measured
	MasterKey      string
	ProxyKey       string // set once ingest succeeds; empty until Ready
	CreatedAt      time.Time
}

// EpisodeRepo is the org-scoped persistence port the episode handlers depend on.
// Every method takes the principal's org public id and scopes its work to that
// org; a caller can never name another org's rows. GetEpisode and
// SetEpisodeMasterKey report found=false (not an error) when the episode is not
// visible to the org, which the handlers turn into a 404.
type EpisodeRepo interface {
	CreateEpisode(ctx context.Context, orgPublicID string, in NewEpisode) (EpisodeRow, error)
	// DeleteOrphanEpisode compensates a create that failed after the row was
	// inserted but before an upload URL could be minted, hard-deleting the
	// just-created row so a failed create leaves nothing behind. It is narrowly
	// gated (org-scoped, still 'uploaded', no master key) so it can only ever
	// remove a fresh orphan. It is a no-op (no error) when nothing matched.
	DeleteOrphanEpisode(ctx context.Context, orgPublicID, episodePublicID string) error
	GetEpisode(ctx context.Context, orgPublicID, episodePublicID string) (EpisodeRow, bool, error)
	SetEpisodeMasterKey(ctx context.Context, orgPublicID, episodePublicID, key string) (EpisodeRow, bool, error)
	// ListEpisodes returns the org's episodes newest-first, excluding
	// soft-deleted rows. It never sees another org's data (the query is
	// org-scoped by the resolved org id, not by any client input).
	ListEpisodes(ctx context.Context, orgPublicID string) ([]EpisodeRow, error)
	// RetryEpisode compare-and-sets a 'failed' episode back to 'uploaded' so the
	// ingest trigger can re-run it. retried=false (err=nil) when no failed row
	// matched the org+id — either it is not visible to the org or it is not in
	// 'failed' — so the handler can map a lost/invalid transition to 409.
	RetryEpisode(ctx context.Context, orgPublicID, episodePublicID string) (row EpisodeRow, retried bool, err error)
}

// createEpisodeRequest is the POST /api/episodes body.
type createEpisodeRequest struct {
	Title          string `json:"title"`
	SourceFilename string `json:"source_filename"`
	SizeBytes      int64  `json:"size_bytes"`
	ContentType    string `json:"content_type"`
}

// episodeDTO is the neutral episode projection returned to clients: prefixed
// public id, no internal ids, no storage key. DurationMs and SizeBytes are
// pointers so an unknown value serializes as absent (the UI renders "—") rather
// than a misleading zero. HasMaster reports whether the master upload actually
// landed: an episode still 'uploaded' with HasMaster=false is one whose client
// abandoned the upload, which the Library renders honestly as "awaiting upload"
// rather than "queued". It exposes only a boolean — never the storage key.
type episodeDTO struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	SourceFilename string `json:"source_filename"`
	Language       string `json:"language"`
	Status         string `json:"status"`
	HasMaster      bool   `json:"has_master"`
	DurationMs     *int64 `json:"duration_ms,omitempty"`
	SizeBytes      *int64 `json:"size_bytes,omitempty"`
	UploadedAt     string `json:"uploaded_at"`
}

func episodeDTOFrom(row EpisodeRow) episodeDTO {
	dto := episodeDTO{
		ID:             ids.Encode(ids.Episode, row.PublicID),
		Title:          row.Title,
		SourceFilename: row.SourceFilename,
		Language:       row.Language,
		Status:         row.Status,
		HasMaster:      row.MasterKey != "",
		UploadedAt:     row.CreatedAt.UTC().Format(time.RFC3339),
	}
	if row.DurationMs > 0 {
		d := row.DurationMs
		dto.DurationMs = &d
	}
	if row.SizeBytes > 0 {
		s := row.SizeBytes
		dto.SizeBytes = &s
	}
	return dto
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

	// From here on the row exists. Any failure before we return the upload URL
	// leaves an unreachable orphan (the client got a 503 and never learned the
	// id), so each such path rolls the row back before returning the 503 — a
	// failed create is invisible.
	episodeID := ids.Encode(ids.Episode, row.PublicID)
	key, err := blob.MasterKey(ids.Encode(ids.Org, row.OrgPublicID), episodeID, filename)
	if err != nil {
		h.rollbackOrphanEpisode(r.Context(), p.OrgPublicID, episodeID)
		h.unavailable(w, r, "master key build failed", err)
		return
	}
	up, err := h.deps.Blob.InitResumableUpload(r.Context(), key, req.ContentType, req.SizeBytes)
	if err != nil {
		h.rollbackOrphanEpisode(r.Context(), p.OrgPublicID, episodeID)
		h.unavailable(w, r, "init upload failed", err)
		return
	}

	writeJSON(w, http.StatusCreated, createEpisodeResponse{Episode: episodeDTOFrom(row), Upload: up})
}

// rollbackOrphanEpisode best-effort deletes a just-created episode row when the
// create failed before an upload URL was returned. A delete failure is logged
// (neutrally, with a correlation id) and swallowed: the create already returns a
// 503, and the store gate keeps this from touching any non-orphan row, so the
// worst case is a stray orphan that the M1 reaper sweeps — never a wrong delete.
func (h *handler) rollbackOrphanEpisode(ctx context.Context, orgPublicID, episodePublicID string) {
	if err := h.deps.Episodes.DeleteOrphanEpisode(ctx, orgPublicID, episodePublicID); err != nil {
		id := errorID()
		h.deps.Logger.LogAttrs(ctx, slog.LevelError, "orphan episode rollback failed",
			slog.String("error_id", id), slog.String("error", err.Error()))
	}
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

// proxyURLTTL bounds how long a minted proxy playback URL stays valid. Short by
// design: the URL is handed to a <video> element that starts playing at once.
const proxyURLTTL = 1 * time.Hour

// listEpisodes returns the principal's org-scoped episodes, newest first,
// projected to the neutral DTO. Soft-deleted rows are excluded by the store.
func (h *handler) listEpisodes(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"})
		return
	}
	rows, err := h.deps.Episodes.ListEpisodes(r.Context(), p.OrgPublicID)
	if err != nil {
		h.unavailable(w, r, "list episodes failed", err)
		return
	}
	out := make([]episodeDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, episodeDTOFrom(row))
	}
	writeJSON(w, http.StatusOK, listEpisodesResponse{Episodes: out})
}

// listEpisodesResponse wraps the list so the payload is an object (extensible
// with paging later) rather than a bare array.
type listEpisodesResponse struct {
	Episodes []episodeDTO `json:"episodes"`
}

// proxyResponse is the signed proxy-playback grant: a short-lived GET URL and
// its expiry. The URL is opaque to the client and reveals no storage layout.
type proxyResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// episodeProxy mints a short-lived signed GET URL for a Ready episode's proxy.
// It is org-scoped (an episode not visible to the org is a 404) and refuses any
// episode that is not Ready with a recorded proxy key — before Ready there is
// nothing to play, so that is a 404 too (a neutral "not available yet").
func (h *handler) episodeProxy(w http.ResponseWriter, r *http.Request) {
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
	if !found || row.Status != statusReady || row.ProxyKey == "" {
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}

	url, err := h.deps.Blob.SignedGetURL(r.Context(), row.ProxyKey, proxyURLTTL)
	if errors.Is(err, blob.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody{Error: "not_found"})
		return
	}
	if err != nil {
		h.unavailable(w, r, "sign proxy url failed", err)
		return
	}
	expires := h.now().Add(proxyURLTTL).UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, proxyResponse{URL: url, ExpiresAt: expires})
}

// retryEpisode re-drives a failed episode. It is allowed only from 'failed':
// an episode not visible to the org is a 404, one in any other state is a 409,
// and a successful compare-and-set resets it to 'uploaded' and fires the ingest
// trigger (best-effort, like upload-complete — the state change is already
// durable, so a trigger miss is logged and the worker can be re-driven).
func (h *handler) retryEpisode(w http.ResponseWriter, r *http.Request) {
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
	if row.Status != statusFailed {
		writeJSON(w, http.StatusConflict, errBody{Error: "invalid_state"})
		return
	}

	updated, retried, err := h.deps.Episodes.RetryEpisode(r.Context(), p.OrgPublicID, episodeID)
	if err != nil {
		h.unavailable(w, r, "retry episode failed", err)
		return
	}
	if !retried {
		// The row left 'failed' between our read and the compare-and-set.
		writeJSON(w, http.StatusConflict, errBody{Error: "invalid_state"})
		return
	}

	if h.deps.Trigger != nil {
		if err := h.deps.Trigger.Trigger(r.Context(), episodeID, ingestStage); err != nil {
			id := errorID()
			h.deps.Logger.LogAttrs(r.Context(), slog.LevelError, "worker retry trigger failed",
				slog.String("error_id", id), slog.String("error", err.Error()))
		}
	}
	writeJSON(w, http.StatusOK, episodeDTOFrom(updated))
}

// now returns the handler's clock, defaulting to time.Now when unset.
func (h *handler) now() time.Time {
	if h.deps.Now != nil {
		return h.deps.Now()
	}
	return time.Now()
}

// Episode status values this package reasons about. They mirror the DB CHECK
// constraint; the store is the source of truth, these are read-only guards.
const (
	statusReady  = "ready"
	statusFailed = "failed"
)

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
