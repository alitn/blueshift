package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/store/db"
)

// TestSweepAbandonedEpisodes verifies the system-level TTL sweep's gate against
// a real Postgres: it removes only a long-abandoned orphan (status 'uploaded',
// no master key, older than the TTL) and leaves a young orphan, an old-but-keyed
// row, and an old advanced row untouched. Unlike DeleteOrphanEpisode this sweep
// is deliberately NOT org-scoped (system maintenance across all tenants); its
// safety comes from the orphan shape plus the age floor.
func TestSweepAbandonedEpisodes(t *testing.T) {
	dsn := requireDB(t)
	applyMigrations(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	applyDevSeed(t, st, ctx)

	var orgID, showID int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT id FROM orgs WHERE name = 'Blueshift Pilot'`).Scan(&orgID); err != nil {
		t.Fatalf("find seed org: %v", err)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT id FROM shows WHERE org_id = $1 ORDER BY id LIMIT 1`, orgID).Scan(&showID); err != nil {
		t.Fatalf("find show: %v", err)
	}

	insert := func(t *testing.T, key pgtype.Text) db.Episode {
		t.Helper()
		ep, err := st.InsertEpisode(ctx, db.InsertEpisodeParams{
			OrgID: orgID, ShowID: showID, Title: "Sweep", SourceFilename: "s.mp4",
			Language: "fa", MasterObjectKey: key,
		})
		if err != nil {
			t.Fatalf("InsertEpisode: %v", err)
		}
		return ep
	}
	// age backdates a row's created_at to now() - d using the DB clock, matching
	// the sweep's own now()-interval comparison (no app/DB clock coupling).
	age := func(t *testing.T, id int64, d time.Duration) {
		t.Helper()
		if _, err := st.Pool().Exec(ctx,
			`UPDATE episodes SET created_at = now() - make_interval(secs => $2) WHERE id = $1`,
			id, d.Seconds()); err != nil {
			t.Fatalf("age row: %v", err)
		}
	}
	setStatus := func(t *testing.T, ep db.Episode, status string) {
		t.Helper()
		if _, err := st.UpdateEpisodeStatus(ctx, db.UpdateEpisodeStatusParams{
			PublicID: ep.PublicID, OrgID: orgID, Status: status,
		}); err != nil {
			t.Fatalf("UpdateEpisodeStatus: %v", err)
		}
	}
	exists := func(t *testing.T, pub pgtype.UUID) bool {
		t.Helper()
		_, err := st.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{PublicID: pub, OrgID: orgID})
		if err == nil {
			return true
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return false
		}
		t.Fatalf("GetEpisodeByPublicID: %v", err)
		return false
	}

	// Case A — fresh-but-young orphan (no key, just created): survives.
	young := insert(t, pgtype.Text{})

	// Case B — old orphan (no key, backdated well past the TTL): deleted.
	oldOrphan := insert(t, pgtype.Text{})
	age(t, oldOrphan.ID, 7*time.Hour)

	// Case C — old but keyed (upload landed, then backdated): survives; the sweep
	// must never touch a row whose master arrived, however old.
	oldKeyed := insert(t, pgtype.Text{String: "k/masters/s.mp4", Valid: true})
	age(t, oldKeyed.ID, 7*time.Hour)

	// Case D — old and advanced (no longer 'uploaded'): survives.
	oldAdvanced := insert(t, pgtype.Text{})
	age(t, oldAdvanced.ID, 7*time.Hour)
	setStatus(t, oldAdvanced, "processing")

	n, err := st.SweepAbandonedEpisodes(ctx, 6*time.Hour)
	if err != nil {
		t.Fatalf("SweepAbandonedEpisodes: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d rows, want exactly 1 (only the old orphan)", n)
	}

	if !exists(t, young.PublicID) {
		t.Error("young orphan was swept (TTL floor not honored)")
	}
	if exists(t, oldOrphan.PublicID) {
		t.Error("old orphan survived the sweep (should be deleted)")
	}
	if !exists(t, oldKeyed.PublicID) {
		t.Error("old keyed row was swept (must never touch a landed master)")
	}
	if !exists(t, oldAdvanced.PublicID) {
		t.Error("old advanced row was swept (must only touch 'uploaded')")
	}

	// A second sweep now finds nothing.
	if n, err := st.SweepAbandonedEpisodes(ctx, 6*time.Hour); err != nil || n != 0 {
		t.Errorf("second sweep = (%d, %v), want (0, nil)", n, err)
	}
}
