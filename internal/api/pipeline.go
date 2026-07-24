package api

// pipeline.go — GET /api/episodes/{id}/pipeline: the per-stage provenance view
// behind the Library's pipeline hover card. The DTO is deliberately lean and
// neutral: stage names are product terms, `engine` is the PUBLIC versioned
// Blueshift label (bs-asr-2, …), durations are DERIVED from the stored
// timestamps at read time, and the private engine detail (provider truth) has
// no field in the port type at all — it structurally cannot reach this file.
// The endpoint is fetched lazily on hover/focus and cached client-side per
// episode, so the Library poll payload is untouched.

import (
	"context"
	"net/http"
	"time"

	"blueshift/internal/auth"
)

// StageRun is the repo's view of one stage run's provenance record (the latest
// run per stage). FinishedAt is the zero time while the run is in flight;
// Outcome is "" until finalized ("ok"/"failed" after). EngineLabel is the
// public versioned neutral label. CostCents is nil when unknown. There is no
// engine-detail field by design: the private provider truth stays server-side.
type StageRun struct {
	Stage       string
	StartedAt   time.Time
	FinishedAt  time.Time
	Outcome     string
	EngineLabel string
	CostCents   *int
}

// StageRunReader is the org-scoped read port for stage-run provenance. An
// episode not visible to the org yields an empty slice (never another org's
// data); existence (404 vs empty) is established by GetEpisode in the handler.
// It is a separate, optional port (not part of EpisodeRepo) so a deployment —
// or a test fake — without provenance simply degrades to the status-derived
// view.
type StageRunReader interface {
	EpisodeStageRuns(ctx context.Context, orgPublicID, episodePublicID string) ([]StageRun, error)
}

// pipelineStageOrder is the canonical pipeline sequence, one entry per possible
// stage, mirroring the stage_runs/current_stage CHECK and the Library's five
// bars. Stage names are neutral product terms — never provider names.
var pipelineStageOrder = []string{"ingest", "transcribe", "diarize", "moments", "render"}

// pipelineStageDTO is one stage row of the hover card: neutral name, derived
// status, derived duration (finished runs only), the public engine label, and
// the cost when known. Absent values serialize as absent, never as zero.
type pipelineStageDTO struct {
	Name       string `json:"name"`
	Status     string `json:"status"` // done | active | failed | pending | unreached
	DurationMs *int64 `json:"duration_ms,omitempty"`
	Engine     string `json:"engine,omitempty"`
	CostCents  *int   `json:"cost_cents,omitempty"`
}

// pipelineDTO is the endpoint envelope. QueuedMs is upload -> first ingest
// start; TotalMs is the sum of the finished stages' derived durations. Both are
// absent for a legacy episode with no recorded runs (graceful degradation).
type pipelineDTO struct {
	Stages   []pipelineStageDTO `json:"stages"`
	QueuedMs *int64             `json:"queued_ms,omitempty"`
	TotalMs  *int64             `json:"total_ms,omitempty"`
}

// episodePipeline handles GET /api/episodes/{id}/pipeline. Auth-required and
// org-scoped through the same GetEpisode gate as the other episode sub-routes,
// so an episode not visible to the principal's org is an indistinguishable 404.
// A legacy episode with no stage_runs rows (processed before provenance landed)
// degrades gracefully to the status/current_stage-derived stage list with no
// durations — a 200, never an error.
func (h *handler) episodePipeline(w http.ResponseWriter, r *http.Request) {
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

	var runs []StageRun
	if h.deps.StageRuns != nil {
		runs, err = h.deps.StageRuns.EpisodeStageRuns(r.Context(), p.OrgPublicID, episodeID)
		if err != nil {
			h.unavailable(w, r, "read stage runs failed", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, pipelineDTOFrom(row, runs, h.deps.PipelineStages))
}

// pipelineDTOFrom derives the stage list. The displayed stages are the ACTIVE
// chain (config) unioned with any stages that actually ran (provenance beats
// configuration drift), in canonical order. Statuses come from the episode's
// status/current_stage exactly like the Library bars, then the recorded runs
// enrich them: a finished 'ok' run makes the stage done and carries its derived
// duration/engine/cost; a 'failed' run informs only the stage the episode
// actually failed at; an in-flight run decorates the active stage with its
// engine. Durations are derived from the stored timestamps here — never stored.
func pipelineDTOFrom(row EpisodeRow, runs []StageRun, activeChain []string) pipelineDTO {
	inChain := make(map[string]bool, len(activeChain))
	for _, s := range activeChain {
		inChain[s] = true
	}
	if len(activeChain) == 0 {
		// No configured chain: the default pipeline is ingest-only.
		inChain["ingest"] = true
	}
	byStage := make(map[string]StageRun, len(runs))
	for _, run := range runs {
		byStage[run.Stage] = run
	}

	curPos := stagePos(row.CurrentStage)
	out := pipelineDTO{Stages: make([]pipelineStageDTO, 0, len(pipelineStageOrder))}
	var totalMs int64
	haveTotal := false
	for pos, name := range pipelineStageOrder {
		run, hasRun := byStage[name]
		if !inChain[name] && !hasRun {
			continue
		}
		dto := pipelineStageDTO{Name: name, Status: baseStageStatus(row.Status, pos, curPos)}
		if hasRun {
			enrichStageDTO(&dto, run)
		}
		if dto.DurationMs != nil {
			totalMs += *dto.DurationMs
			haveTotal = true
		}
		out.Stages = append(out.Stages, dto)
	}

	// Queued time: upload -> the first stage's recorded start.
	if len(out.Stages) > 0 {
		if run, ok := byStage[out.Stages[0].Name]; ok && !row.CreatedAt.IsZero() {
			if q := run.StartedAt.Sub(row.CreatedAt).Milliseconds(); q >= 0 {
				out.QueuedMs = &q
			}
		}
	}
	if haveTotal {
		out.TotalMs = &totalMs
	}
	return out
}

// stagePos resolves a stage name to its canonical position. An absent or
// unknown current_stage maps to the first stage (a legacy/unclaimed row sits at
// ingest), matching the Library's bar mapping.
func stagePos(stage string) int {
	for i, s := range pipelineStageOrder {
		if s == stage {
			return i
		}
	}
	return 0
}

// baseStageStatus is the status/current_stage-derived stage state — the same
// ruling as the Library's five bars, so a legacy episode with no runs renders
// identically in the cell and the card.
func baseStageStatus(status string, pos, curPos int) string {
	switch status {
	case "processing":
		switch {
		case pos < curPos:
			return "done"
		case pos == curPos:
			return "active"
		}
	case statusReady:
		if pos <= curPos {
			return "done"
		}
	case statusFailed:
		switch {
		case pos < curPos:
			return "done"
		case pos == curPos:
			return "failed"
		}
	default: // uploaded (queued or awaiting upload)
		if pos == 0 {
			return "pending"
		}
	}
	return "unreached"
}

// enrichStageDTO folds the stage's latest recorded run into the derived row. A
// finished 'ok' run is authoritative (done + duration + engine + cost); a
// 'failed' run only decorates a stage the base derivation already shows failed
// (a superseded failure from an earlier pass must not repaint a retried
// pipeline); an in-flight run decorates the active stage with its engine.
func enrichStageDTO(dto *pipelineStageDTO, run StageRun) {
	switch run.Outcome {
	case "ok":
		dto.Status = "done"
		dto.Engine = run.EngineLabel
		dto.DurationMs = runDurationMs(run)
		dto.CostCents = run.CostCents
	case "failed":
		if dto.Status == "failed" {
			dto.Engine = run.EngineLabel
			dto.DurationMs = runDurationMs(run)
			dto.CostCents = run.CostCents
		}
	default: // in flight
		if dto.Status == "active" {
			dto.Engine = run.EngineLabel
		}
	}
}

// runDurationMs derives a finished run's duration from its timestamps (the
// stored record is timestamps only; duration is computed at read time). Nil
// while unfinished or on a clock anomaly — absent, never negative.
func runDurationMs(run StageRun) *int64 {
	if run.FinishedAt.IsZero() {
		return nil
	}
	d := run.FinishedAt.Sub(run.StartedAt).Milliseconds()
	if d < 0 {
		return nil
	}
	return &d
}
