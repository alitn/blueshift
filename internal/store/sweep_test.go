package store

import (
	"context"
	"errors"
	"regexp"
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

// TestSweepStuckProcessingEpisodes verifies the stale-claim sweep's gate against
// a real Postgres: it force-fails only 'processing' rows whose claim is stale
// (claimed_at older than the TTL) or NULL (a legacy claim, e.g. the currently
// stuck prod rows), and never touches a fresh claim or any non-'processing' row.
// Swept rows get a neutral error_id and their claimed_at cleared, so the existing
// retry API/UI can rescue them.
func TestSweepStuckProcessingEpisodes(t *testing.T) {
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

	insert := func(t *testing.T) db.Episode {
		t.Helper()
		ep, err := st.InsertEpisode(ctx, db.InsertEpisodeParams{
			OrgID: orgID, ShowID: showID, Title: "Stuck", SourceFilename: "s.mp4",
			Language: "fa", MasterObjectKey: pgtype.Text{String: "k/masters/s.mp4", Valid: true},
		})
		if err != nil {
			t.Fatalf("InsertEpisode: %v", err)
		}
		return ep
	}
	// setState forces status and claimed_at directly (via the DB clock for the age
	// so there is no app/DB clock coupling). A negative claimedAgo leaves
	// claimed_at NULL, modelling a legacy claim taken before the column existed.
	setState := func(t *testing.T, id int64, status string, claimedAgo time.Duration, nullClaim bool) {
		t.Helper()
		if nullClaim {
			if _, err := st.Pool().Exec(ctx,
				`UPDATE episodes SET status = $2, claimed_at = NULL WHERE id = $1`, id, status); err != nil {
				t.Fatalf("setState null: %v", err)
			}
			return
		}
		if _, err := st.Pool().Exec(ctx,
			`UPDATE episodes SET status = $2, claimed_at = now() - make_interval(secs => $3) WHERE id = $1`,
			id, status, claimedAgo.Seconds()); err != nil {
			t.Fatalf("setState: %v", err)
		}
	}
	type snap struct {
		status     string
		claimNull  bool
		errorID    string
		errorIDSet bool
	}
	read := func(t *testing.T, id int64) snap {
		t.Helper()
		var s snap
		var errID pgtype.Text
		if err := st.Pool().QueryRow(ctx,
			`SELECT status, claimed_at IS NULL, error_id FROM episodes WHERE id = $1`, id).
			Scan(&s.status, &s.claimNull, &errID); err != nil {
			t.Fatalf("read: %v", err)
		}
		s.errorID, s.errorIDSet = errID.String, errID.Valid
		return s
	}

	// Case A — fresh processing (claimed just now): survives.
	freshProc := insert(t)
	setState(t, freshProc.ID, "processing", time.Minute, false)

	// Case B — stale processing (claimed well past the TTL): failed.
	staleProc := insert(t)
	setState(t, staleProc.ID, "processing", 7*time.Hour, false)

	// Case C — legacy processing (NULL claimed_at): failed. This is the shape of
	// the two currently-stuck prod rows.
	nullProc := insert(t)
	setState(t, nullProc.ID, "processing", 0, true)

	// Case D — old 'ready' (even with a stale claimed_at): survives. The sweep is
	// gated on 'processing' only.
	readyRow := insert(t)
	setState(t, readyRow.ID, "ready", 7*time.Hour, false)

	// Case E — old 'failed': survives (already terminal).
	failedRow := insert(t)
	setState(t, failedRow.ID, "failed", 7*time.Hour, false)

	// Case F — 'uploaded' (never claimed): survives; the stuck sweep must never
	// touch an unclaimed upload (that is the abandoned-upload sweep's job).
	uploadedRow := insert(t)

	// The sweep is system-level (all orgs) and the scratch DB carries 'processing'
	// residue from sibling tests, so assert on the rows this test created plus a
	// >= floor — never an exact global count (the abandoned-upload test relies on
	// its narrow gate for the same reason).
	n, err := st.SweepStuckProcessingEpisodes(ctx, 5*time.Hour)
	if err != nil {
		t.Fatalf("SweepStuckProcessingEpisodes: %v", err)
	}
	if n < 2 {
		t.Fatalf("swept %d rows, want >= 2 (at least this test's stale + null-claim rows)", n)
	}

	if s := read(t, freshProc.ID); s.status != "processing" || s.claimNull {
		t.Errorf("fresh processing = %+v, want status=processing claimed_at set (a live claim must never be reaped)", s)
	}
	for _, id := range []int64{staleProc.ID, nullProc.ID} {
		s := read(t, id)
		if s.status != "failed" {
			t.Errorf("stuck row %d status = %q, want failed", id, s.status)
		}
		if !s.claimNull {
			t.Errorf("stuck row %d claimed_at not cleared", id)
		}
		if !s.errorIDSet || !hexID.MatchString(s.errorID) {
			t.Errorf("stuck row %d error_id = %q (set=%v), want a neutral 16-hex id", id, s.errorID, s.errorIDSet)
		}
	}
	if s := read(t, readyRow.ID); s.status != "ready" {
		t.Errorf("ready row status = %q, want ready (untouched)", s.status)
	}
	if s := read(t, failedRow.ID); s.status != "failed" {
		t.Errorf("failed row status = %q, want failed (untouched)", s.status)
	}
	if s := read(t, uploadedRow.ID); s.status != "uploaded" {
		t.Errorf("uploaded row status = %q, want uploaded (untouched)", s.status)
	}

	// A second sweep leaves the fresh claim alone: repeated passes never reap a
	// live claim, and the just-failed rows are now terminal.
	if _, err := st.SweepStuckProcessingEpisodes(ctx, 5*time.Hour); err != nil {
		t.Fatalf("second stuck sweep: %v", err)
	}
	if s := read(t, freshProc.ID); s.status != "processing" || s.claimNull {
		t.Errorf("fresh processing after second sweep = %+v, want unchanged", s)
	}
}

// hexID matches the neutral 16-hex-char error id shape used across the pipeline
// and the stale-claim sweep.
var hexID = regexp.MustCompile(`^[0-9a-f]{16}$`)
