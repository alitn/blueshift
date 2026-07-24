package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/ids"
	"blueshift/internal/store/db"
)

// deleteHarness is the shared setup for the soft-delete tests: an open store on
// the scratch DB plus the seed org/show ids and small helpers.
type deleteHarness struct {
	st     *Store
	ctx    context.Context
	orgID  int64
	showID int64
	orgPub pgtype.UUID
}

func newDeleteHarness(t *testing.T) *deleteHarness {
	t.Helper()
	dsn := requireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)
	applyDevSeed(t, st, ctx)

	h := &deleteHarness{st: st, ctx: ctx}
	if err := st.Pool().QueryRow(ctx,
		`SELECT id, public_id FROM orgs WHERE name = 'Blueshift Pilot'`).Scan(&h.orgID, &h.orgPub); err != nil {
		t.Fatalf("find seed org: %v", err)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT id FROM shows WHERE org_id = $1 ORDER BY id LIMIT 1`, h.orgID).Scan(&h.showID); err != nil {
		t.Fatalf("find show: %v", err)
	}
	return h
}

// insert seeds an episode; key controls the master-key column (the orphan gate).
func (h *deleteHarness) insert(t *testing.T, key pgtype.Text) db.Episode {
	t.Helper()
	ep, err := h.st.InsertEpisode(h.ctx, db.InsertEpisodeParams{
		OrgID: h.orgID, ShowID: h.showID, Title: "Delete", SourceFilename: "d.mp4",
		Language: "fa", MasterObjectKey: key,
	})
	if err != nil {
		t.Fatalf("InsertEpisode: %v", err)
	}
	deleteEpisodeOnCleanup(t, h.st, ep.ID)
	return ep
}

// softDelete runs the query directly and returns the affected-row count.
func (h *deleteHarness) softDelete(t *testing.T, pub pgtype.UUID, org int64) int64 {
	t.Helper()
	n, err := h.st.SoftDeleteEpisode(h.ctx, db.SoftDeleteEpisodeParams{PublicID: pub, OrgID: org})
	if err != nil {
		t.Fatalf("SoftDeleteEpisode: %v", err)
	}
	return n
}

// raw reads status + deleted_at directly (bypassing the filtered read queries).
func (h *deleteHarness) raw(t *testing.T, id int64) (status string, deletedAt pgtype.Timestamptz) {
	t.Helper()
	if err := h.st.Pool().QueryRow(h.ctx,
		`SELECT status, deleted_at FROM episodes WHERE id = $1`, id).Scan(&status, &deletedAt); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	return status, deletedAt
}

// TestSoftDeleteEpisode verifies the delete query against a real Postgres:
// org-scoped, idempotent (repeat matches again without moving deleted_at), and
// the row survives — only stamped.
func TestSoftDeleteEpisode(t *testing.T) {
	h := newDeleteHarness(t)

	ep := h.insert(t, pgtype.Text{String: "k/masters/d.mp4", Valid: true})

	// Cross-org first: a foreign org id matches no row and changes nothing.
	if n := h.softDelete(t, ep.PublicID, h.orgID+100000); n != 0 {
		t.Fatalf("cross-org delete rows = %d, want 0", n)
	}
	if _, del := h.raw(t, ep.ID); del.Valid {
		t.Fatal("cross-org delete stamped deleted_at")
	}

	// First delete: one row, deleted_at set, row kept.
	if n := h.softDelete(t, ep.PublicID, h.orgID); n != 1 {
		t.Fatalf("delete rows = %d, want 1", n)
	}
	_, first := h.raw(t, ep.ID)
	if !first.Valid {
		t.Fatal("deleted_at not stamped")
	}

	// Second delete: still matches (the handler's repeat 204) and deleted_at is
	// unchanged — the original tombstone is never re-stamped.
	if n := h.softDelete(t, ep.PublicID, h.orgID); n != 1 {
		t.Fatalf("repeat delete rows = %d, want 1 (idempotent)", n)
	}
	_, second := h.raw(t, ep.ID)
	if !second.Time.Equal(first.Time) {
		t.Errorf("repeat delete moved deleted_at: %v -> %v", first.Time, second.Time)
	}
}

// TestSoftDeleteStoreMethod exercises the org-scoped Store wrapper: found
// semantics for real, repeated, malformed, and unknown ids.
func TestSoftDeleteStoreMethod(t *testing.T) {
	h := newDeleteHarness(t)
	orgPub := uuidString(h.orgPub)

	ep := h.insert(t, pgtype.Text{})
	epID := ids.Encode(ids.Episode, ep.PublicID.Bytes)

	found, err := h.st.DeleteEpisode(h.ctx, orgPub, epID)
	if err != nil || !found {
		t.Fatalf("DeleteEpisode = (%v, %v), want (true, nil)", found, err)
	}
	// Repeat: still found (idempotent 204).
	found, err = h.st.DeleteEpisode(h.ctx, orgPub, epID)
	if err != nil || !found {
		t.Fatalf("repeat DeleteEpisode = (%v, %v), want (true, nil)", found, err)
	}
	// Malformed and unknown ids: found=false, no error (the handler's 404).
	for _, bad := range []string{"not-an-id", "ep_00000000000000000000000000"} {
		found, err = h.st.DeleteEpisode(h.ctx, orgPub, bad)
		if err != nil || found {
			t.Errorf("DeleteEpisode(%q) = (%v, %v), want (false, nil)", bad, found, err)
		}
	}
}

// TestDeletedEpisodeInvisibleToReads: every filtered read drops the deleted row.
func TestDeletedEpisodeInvisibleToReads(t *testing.T) {
	h := newDeleteHarness(t)

	ep := h.insert(t, pgtype.Text{String: "k/masters/d.mp4", Valid: true})
	if n := h.softDelete(t, ep.PublicID, h.orgID); n != 1 {
		t.Fatalf("delete rows = %d, want 1", n)
	}

	// Get: no row.
	if _, err := h.st.GetEpisodeByPublicID(h.ctx, db.GetEpisodeByPublicIDParams{
		PublicID: ep.PublicID, OrgID: h.orgID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetEpisodeByPublicID after delete err = %v, want ErrNoRows", err)
	}
	// List: excluded.
	list, err := h.st.ListEpisodesByOrg(h.ctx, h.orgID)
	if err != nil {
		t.Fatalf("ListEpisodesByOrg: %v", err)
	}
	for _, row := range list {
		if row.ID == ep.ID {
			t.Error("deleted episode still listed")
		}
	}
	// Status probe (worker WARN annotation): no row.
	if _, err := h.st.GetEpisodeStatusByPublicID(h.ctx, ep.PublicID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetEpisodeStatusByPublicID after delete err = %v, want ErrNoRows", err)
	}
	// Store-level API port reads: found=false / empty, never another outcome.
	orgPub := uuidString(h.orgPub)
	epID := ids.Encode(ids.Episode, ep.PublicID.Bytes)
	if _, found, err := h.st.GetEpisode(h.ctx, orgPub, epID); err != nil || found {
		t.Errorf("GetEpisode after delete = (found=%v, err=%v), want (false, nil)", found, err)
	}
	segs, err := h.st.EpisodeTranscript(h.ctx, orgPub, epID)
	if err != nil || len(segs) != 0 {
		t.Errorf("EpisodeTranscript after delete = (%d segs, %v), want (0, nil)", len(segs), err)
	}
}

// TestDeletedEpisodeUnwritable: the write/CAS paths (upload-complete, retry,
// status set) match no row once deleted_at is stamped.
func TestDeletedEpisodeUnwritable(t *testing.T) {
	h := newDeleteHarness(t)

	ep := h.insert(t, pgtype.Text{})
	if n := h.softDelete(t, ep.PublicID, h.orgID); n != 1 {
		t.Fatalf("delete rows = %d, want 1", n)
	}

	if _, err := h.st.Queries.SetEpisodeMasterKey(h.ctx, db.SetEpisodeMasterKeyParams{
		PublicID: ep.PublicID, OrgID: h.orgID,
		MasterObjectKey: pgtype.Text{String: "k/masters/late.mp4", Valid: true},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("SetEpisodeMasterKey after delete err = %v, want ErrNoRows", err)
	}
	if _, err := h.st.UpdateEpisodeStatus(h.ctx, db.UpdateEpisodeStatusParams{
		PublicID: ep.PublicID, OrgID: h.orgID, Status: "processing",
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("UpdateEpisodeStatus after delete err = %v, want ErrNoRows", err)
	}
	// Retry: force the raw row to 'failed' (bypassing the filtered setter), then
	// prove the retry CAS still refuses it.
	if _, err := h.st.Pool().Exec(h.ctx,
		`UPDATE episodes SET status = 'failed' WHERE id = $1`, ep.ID); err != nil {
		t.Fatalf("force failed: %v", err)
	}
	if _, err := h.st.RetryFailedEpisode(h.ctx, db.RetryFailedEpisodeParams{
		PublicID: ep.PublicID, OrgID: h.orgID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("RetryFailedEpisode after delete err = %v, want ErrNoRows", err)
	}
}

// TestDeletedEpisodeUnclaimableAndUnbillable: the pipeline can neither claim,
// advance, finalize, nor bill a deleted episode — deleting mid-flight starves
// the stage chain without any provider call (cost-safety invariant).
func TestDeletedEpisodeUnclaimableAndUnbillable(t *testing.T) {
	h := newDeleteHarness(t)

	// Entry-stage claim on a deleted 'uploaded' row: no match.
	entry := h.insert(t, pgtype.Text{String: "k/masters/d.mp4", Valid: true})
	if n := h.softDelete(t, entry.PublicID, h.orgID); n != 1 {
		t.Fatalf("delete rows = %d, want 1", n)
	}
	if _, err := h.st.ClaimEpisodeForStage(h.ctx, db.ClaimEpisodeForStageParams{
		PublicID: entry.PublicID,
		Stage:    pgtype.Text{String: "ingest", Valid: true},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("entry claim after delete err = %v, want ErrNoRows", err)
	}
	if st, _ := h.raw(t, entry.ID); st != "uploaded" {
		t.Errorf("refused claim mutated status to %q", st)
	}

	// Continuation claim + finalizers + billable gate on a deleted mid-flight
	// row ('processing' at stage ingest, stamped via the raw pool to model a
	// delete racing an in-flight run).
	mid := h.insert(t, pgtype.Text{String: "k/masters/d.mp4", Valid: true})
	if _, err := h.st.Pool().Exec(h.ctx,
		`UPDATE episodes SET status = 'processing', current_stage = 'ingest', claimed_at = now() WHERE id = $1`,
		mid.ID); err != nil {
		t.Fatalf("force processing: %v", err)
	}
	if n := h.softDelete(t, mid.PublicID, h.orgID); n != 1 {
		t.Fatalf("delete rows = %d, want 1", n)
	}
	if _, err := h.st.ClaimEpisodeForStage(h.ctx, db.ClaimEpisodeForStageParams{
		PublicID:  mid.PublicID,
		Stage:     pgtype.Text{String: "transcribe", Valid: true},
		PrevStage: pgtype.Text{String: "ingest", Valid: true},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("continuation claim after delete err = %v, want ErrNoRows", err)
	}
	if _, err := h.st.AdvanceEpisodeStage(h.ctx, db.AdvanceEpisodeStageParams{
		PublicID: mid.PublicID, OrgID: h.orgID,
		CurrentStage: pgtype.Text{String: "ingest", Valid: true},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("AdvanceEpisodeStage after delete err = %v, want ErrNoRows", err)
	}
	if _, err := h.st.MarkEpisodeReady(h.ctx, db.MarkEpisodeReadyParams{
		PublicID: mid.PublicID, OrgID: h.orgID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("MarkEpisodeReady after delete err = %v, want ErrNoRows", err)
	}
	if _, err := h.st.MarkEpisodeFailed(h.ctx, db.MarkEpisodeFailedParams{
		PublicID: mid.PublicID, OrgID: h.orgID,
		ErrorID: pgtype.Text{String: "deadbeefdeadbeef", Valid: true},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("MarkEpisodeFailed after delete err = %v, want ErrNoRows", err)
	}
	// Billable gate: no increment ever lands on a deleted row (unbillable), both
	// at the query and at the BeginBillableAttempt port.
	if _, err := h.st.IncrementEpisodeProcessAttemptsBelowCap(h.ctx, db.IncrementEpisodeProcessAttemptsBelowCapParams{
		PublicID: mid.PublicID, OrgID: h.orgID, MaxAttempts: 100,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("IncrementEpisodeProcessAttemptsBelowCap after delete err = %v, want ErrNoRows", err)
	}
	orgEnc := ids.Encode(ids.Org, h.orgPub.Bytes)
	epEnc := ids.Encode(ids.Episode, mid.PublicID.Bytes)
	if _, allowed, err := h.st.BeginBillableAttempt(h.ctx, orgEnc, epEnc, 100); err != nil || allowed {
		t.Errorf("BeginBillableAttempt after delete = (allowed=%v, err=%v), want (false, nil)", allowed, err)
	}
	var attempts int32
	if err := h.st.Pool().QueryRow(h.ctx,
		`SELECT process_attempts FROM episodes WHERE id = $1`, mid.ID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 0 {
		t.Errorf("process_attempts = %d after refused billable gate, want 0", attempts)
	}
}

// TestSweepsLeaveDeletedEpisodesAlone: neither system sweep may touch a deleted
// row — the stale-claim sweep must not resurrect it into 'failed' (which the
// retry path could then re-drive), and the abandoned-upload sweep must not
// hard-delete the tombstone.
func TestSweepsLeaveDeletedEpisodesAlone(t *testing.T) {
	h := newDeleteHarness(t)

	// Deleted, stale 'processing' row: the stuck sweep skips it.
	stuck := h.insert(t, pgtype.Text{String: "k/masters/d.mp4", Valid: true})
	if _, err := h.st.Pool().Exec(h.ctx,
		`UPDATE episodes SET status = 'processing', claimed_at = now() - interval '7 hours' WHERE id = $1`,
		stuck.ID); err != nil {
		t.Fatalf("force stale processing: %v", err)
	}
	if n := h.softDelete(t, stuck.PublicID, h.orgID); n != 1 {
		t.Fatalf("delete rows = %d, want 1", n)
	}
	if _, err := h.st.SweepStuckProcessingEpisodes(h.ctx, 5*time.Hour); err != nil {
		t.Fatalf("SweepStuckProcessingEpisodes: %v", err)
	}
	if st, del := h.raw(t, stuck.ID); st != "processing" || !del.Valid {
		t.Errorf("deleted stuck row after sweep = (status=%q, deleted=%v), want processing + tombstone kept", st, del.Valid)
	}

	// Deleted, old orphan (no master key): the abandoned sweep keeps the row.
	orphan := h.insert(t, pgtype.Text{})
	if _, err := h.st.Pool().Exec(h.ctx,
		`UPDATE episodes SET created_at = now() - interval '7 hours' WHERE id = $1`, orphan.ID); err != nil {
		t.Fatalf("age orphan: %v", err)
	}
	if n := h.softDelete(t, orphan.PublicID, h.orgID); n != 1 {
		t.Fatalf("delete rows = %d, want 1", n)
	}
	if _, err := h.st.SweepAbandonedEpisodes(h.ctx, 6*time.Hour); err != nil {
		t.Fatalf("SweepAbandonedEpisodes: %v", err)
	}
	var kept bool
	if err := h.st.Pool().QueryRow(h.ctx,
		`SELECT EXISTS(SELECT 1 FROM episodes WHERE id = $1)`, orphan.ID).Scan(&kept); err != nil {
		t.Fatalf("exists: %v", err)
	}
	if !kept {
		t.Error("abandoned sweep hard-deleted a soft-deleted row; the tombstone must be kept")
	}

	// The create-time orphan rollback also refuses a deleted row.
	if n, err := h.st.Queries.DeleteOrphanEpisode(h.ctx, db.DeleteOrphanEpisodeParams{
		PublicID: orphan.PublicID, OrgID: h.orgID,
	}); err != nil || n != 0 {
		t.Errorf("DeleteOrphanEpisode on deleted row = (%d, %v), want (0, nil)", n, err)
	}
}
