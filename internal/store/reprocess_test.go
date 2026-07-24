package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/api"
	"blueshift/internal/ids"
	"blueshift/internal/store/db"
)

// forceState drives an episode straight to a (status, current_stage, claimed_at,
// process_attempts) tuple via raw SQL, bypassing the filtered setters — so the
// reprocess CAS can be exercised from every source state (including ones the app
// only ever reaches through the pipeline) and the cleared columns are observable.
func (h *deleteHarness) forceState(t *testing.T, id int64, status, stage string, claimed bool, attempts int) {
	t.Helper()
	var stageArg any // nil -> SQL NULL; a string -> that stage
	if stage != "" {
		stageArg = stage
	}
	if _, err := h.st.Pool().Exec(h.ctx,
		`UPDATE episodes
		    SET status = $2,
		        current_stage = $3,
		        claimed_at = CASE WHEN $4 THEN now() ELSE NULL END,
		        process_attempts = $5
		  WHERE id = $1`,
		id, status, stageArg, claimed, attempts); err != nil {
		t.Fatalf("forceState(%s): %v", status, err)
	}
}

// reprocessState reads back the columns the reprocess reset touches (status,
// current_stage, claimed_at) plus the one it must NOT touch (process_attempts).
func (h *deleteHarness) reprocessState(t *testing.T, id int64) (status string, stage pgtype.Text, claimed pgtype.Timestamptz, attempts int) {
	t.Helper()
	if err := h.st.Pool().QueryRow(h.ctx,
		`SELECT status, current_stage, claimed_at, process_attempts FROM episodes WHERE id = $1`,
		id).Scan(&status, &stage, &claimed, &attempts); err != nil {
		t.Fatalf("reprocessState read: %v", err)
	}
	return status, stage, claimed, attempts
}

// TestReprocessEpisodeTransition is the DB-backed transition-legality proof for
// the reprocess CAS against a real Postgres:
//   - ready  -> uploaded (ok): status reset, current_stage + claimed_at cleared,
//     process_attempts UNCHANGED (cost-safety cap survives).
//   - failed -> uploaded (ok): same, and the prior error_id is cleared.
//   - processing / uploaded: the CAS matches nothing (reprocessed=false), leaving
//     the row exactly as it was — the handler maps this to 409.
//   - a foreign org id: the org-scoped WHERE matches no row (pgx.ErrNoRows).
func TestReprocessEpisodeTransition(t *testing.T) {
	h := newDeleteHarness(t)
	orgPub := uuidString(h.orgPub)

	// ready -> uploaded, with a stage/claim/attempt seeded so the reset is observable.
	ready := h.insert(t, pgtype.Text{String: "k/masters/r.mp4", Valid: true})
	h.forceState(t, ready.ID, "ready", "transcribe", true, 3)
	readyID := ids.Encode(ids.Episode, ready.PublicID.Bytes)

	row, ok, err := h.st.ReprocessEpisode(h.ctx, orgPub, readyID)
	if err != nil || !ok {
		t.Fatalf("ReprocessEpisode(ready) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if row.Status != "uploaded" {
		t.Errorf("returned status = %q, want uploaded", row.Status)
	}
	if row.CurrentStage != "" {
		t.Errorf("returned current_stage = %q, want cleared", row.CurrentStage)
	}
	status, stage, claimed, attempts := h.reprocessState(t, ready.ID)
	if status != "uploaded" {
		t.Errorf("db status = %q, want uploaded", status)
	}
	if stage.Valid {
		t.Errorf("db current_stage = %q, want NULL (cleared)", stage.String)
	}
	if claimed.Valid {
		t.Error("db claimed_at not cleared; the stale-claim sweeper must never see a reprocessed row")
	}
	if attempts != 3 {
		t.Errorf("process_attempts = %d, want 3 unchanged (the billable cap must survive reprocess)", attempts)
	}

	// failed -> uploaded, with a seeded error_id proven cleared.
	failed := h.insert(t, pgtype.Text{String: "k/masters/f.mp4", Valid: true})
	h.forceState(t, failed.ID, "failed", "diarize", false, 2)
	if _, err := h.st.Pool().Exec(h.ctx,
		`UPDATE episodes SET error_id = 'deadbeefdeadbeef' WHERE id = $1`, failed.ID); err != nil {
		t.Fatalf("seed error_id: %v", err)
	}
	failedID := ids.Encode(ids.Episode, failed.PublicID.Bytes)
	if _, ok, err := h.st.ReprocessEpisode(h.ctx, orgPub, failedID); err != nil || !ok {
		t.Fatalf("ReprocessEpisode(failed) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	var errID pgtype.Text
	if err := h.st.Pool().QueryRow(h.ctx,
		`SELECT error_id FROM episodes WHERE id = $1`, failed.ID).Scan(&errID); err != nil {
		t.Fatalf("read error_id: %v", err)
	}
	if errID.Valid {
		t.Errorf("error_id = %q, want cleared on reprocess", errID.String)
	}

	// processing / uploaded: illegal sources — the CAS refuses, the row is untouched.
	for _, st := range []string{"processing", "uploaded"} {
		ep := h.insert(t, pgtype.Text{String: "k/masters/x.mp4", Valid: true})
		h.forceState(t, ep.ID, st, "ingest", true, 1)
		epID := ids.Encode(ids.Episode, ep.PublicID.Bytes)
		row, ok, err := h.st.ReprocessEpisode(h.ctx, orgPub, epID)
		if err != nil {
			t.Fatalf("ReprocessEpisode(%s): %v", st, err)
		}
		if ok {
			t.Errorf("ReprocessEpisode(%s) reprocessed = true, want false (illegal source state)", st)
		}
		if row != (api.EpisodeRow{}) {
			t.Errorf("ReprocessEpisode(%s) returned a row on refusal: %+v", st, row)
		}
		if got, _, _, _ := h.reprocessState(t, ep.ID); got != st {
			t.Errorf("ReprocessEpisode(%s) changed status to %q, want untouched", st, got)
		}
	}

	// Org scoping: the CAS is gated on org_id, so a foreign org matches no row —
	// exercise the generated query directly (the Store wrapper resolves a real org
	// first; the SQL scope is what the isolation rests on).
	scoped := h.insert(t, pgtype.Text{String: "k/masters/s.mp4", Valid: true})
	h.forceState(t, scoped.ID, "ready", "", false, 0)
	if _, err := h.st.Queries.ReprocessEpisode(h.ctx, db.ReprocessEpisodeParams{
		PublicID: scoped.PublicID, OrgID: h.orgID + 100000,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("ReprocessEpisode with a foreign org err = %v, want ErrNoRows (org-scoped)", err)
	}
	if got, _, _, _ := h.reprocessState(t, scoped.ID); got != "ready" {
		t.Errorf("foreign-org reprocess changed status to %q, want ready (untouched)", got)
	}
}
