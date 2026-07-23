package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/store/db"
)

// migrateURL rewrites a standard postgres DSN to the scheme golang-migrate's
// pgx/v5 driver registers ("pgx5").
func migrateURL(dsn string) string {
	if i := strings.Index(dsn, "://"); i >= 0 {
		return "pgx5" + dsn[i:]
	}
	return dsn
}

// requireDB returns TEST_DATABASE_URL or skips the test with a logged reason.
// These tests run under `make demo`/CI where a scratch Postgres is provisioned;
// locally they no-op so `make check` is green without a database.
func requireDB(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("skip: TEST_DATABASE_URL not set (DB-backed store test needs a scratch Postgres)")
	}
	return dsn
}

// applyMigrations runs every up migration against dsn. A clean re-run reports
// migrate.ErrNoChange, which is fine.
func applyMigrations(t *testing.T, dsn string) {
	t.Helper()
	m, err := migrate.New("file://../../migrations", migrateURL(dsn))
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	defer func() {
		if serr, derr := m.Close(); serr != nil || derr != nil {
			t.Logf("migrate close: source=%v db=%v", serr, derr)
		}
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}
}

// applyDevSeed loads the dev/demo user identities. Migration 0002 no longer
// seeds users (they are dev-only), so DB-backed tests that need a user must
// apply this fixture themselves. It is idempotent, so re-running is a no-op.
func applyDevSeed(t *testing.T, st *Store, ctx context.Context) {
	t.Helper()
	seed, err := os.ReadFile("../../fixtures/dev-seed.sql")
	if err != nil {
		t.Fatalf("read dev-seed: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, string(seed)); err != nil {
		t.Fatalf("apply dev-seed: %v", err)
	}
}

func TestMigrationsAndQueries(t *testing.T) {
	dsn := requireDB(t)
	applyMigrations(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := st.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// Dev users are seeded by the fixture, not by migration 0002.
	applyDevSeed(t, st, ctx)

	// Seed org lookup.
	var orgID int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT id FROM orgs WHERE name = 'Blueshift Pilot'`).Scan(&orgID); err != nil {
		t.Fatalf("find seed org: %v", err)
	}

	org, err := st.GetOrg(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrg: %v", err)
	}
	if org.Name != "Blueshift Pilot" {
		t.Errorf("org.Name = %q", org.Name)
	}

	// Membership role for the seeded approver.
	var approverID int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT id FROM users WHERE email = 'dev-approver@blueshift.local'`).Scan(&approverID); err != nil {
		t.Fatalf("find approver: %v", err)
	}
	role, err := st.GetMembershipRole(ctx, db.GetMembershipRoleParams{OrgID: orgID, UserID: approverID})
	if err != nil {
		t.Fatalf("GetMembershipRole: %v", err)
	}
	if role != "approver" {
		t.Errorf("role = %q, want approver", role)
	}

	// Config: global key resolves; org fallback works.
	val, err := st.GetConfig(ctx, db.GetConfigParams{
		Key:   "allow_self_approval",
		OrgID: pgtype.Int8{Int64: orgID, Valid: true},
	})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if string(val) != "true" {
		t.Errorf("allow_self_approval = %q, want true", string(val))
	}

	// Show for the org (needed to insert an episode).
	var showID int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT id FROM shows WHERE org_id = $1 ORDER BY id LIMIT 1`, orgID).Scan(&showID); err != nil {
		t.Fatalf("find show: %v", err)
	}

	// Episode insert -> get by public_id -> list -> update status.
	ep, err := st.InsertEpisode(ctx, db.InsertEpisodeParams{
		OrgID:           orgID,
		ShowID:          showID,
		Title:           "Smoke Episode",
		SourceFilename:  "smoke.mp4",
		Language:        "fa",
		MasterObjectKey: pgtype.Text{String: "k/masters/smoke.mp4", Valid: true},
	})
	if err != nil {
		t.Fatalf("InsertEpisode: %v", err)
	}
	if ep.Status != "uploaded" {
		t.Errorf("new episode status = %q, want uploaded", ep.Status)
	}

	got, err := st.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{
		PublicID: ep.PublicID,
		OrgID:    orgID,
	})
	if err != nil {
		t.Fatalf("GetEpisodeByPublicID: %v", err)
	}
	if got.ID != ep.ID {
		t.Errorf("got episode id %d, want %d", got.ID, ep.ID)
	}

	list, err := st.ListEpisodesByOrg(ctx, orgID)
	if err != nil {
		t.Fatalf("ListEpisodesByOrg: %v", err)
	}
	if len(list) == 0 {
		t.Error("ListEpisodesByOrg returned no rows")
	}

	updated, err := st.UpdateEpisodeStatus(ctx, db.UpdateEpisodeStatusParams{
		PublicID: ep.PublicID,
		OrgID:    orgID,
		Status:   "processing",
	})
	if err != nil {
		t.Fatalf("UpdateEpisodeStatus: %v", err)
	}
	if updated.Status != "processing" {
		t.Errorf("updated status = %q, want processing", updated.Status)
	}
}

// TestDeleteOrphanEpisode verifies the compensating-rollback query's narrow
// gate: it removes only a fresh orphan (org-scoped, still 'uploaded', no master
// key) and leaves any keyed, advanced, or other-org row untouched.
func TestDeleteOrphanEpisode(t *testing.T) {
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
			OrgID: orgID, ShowID: showID, Title: "Orphan", SourceFilename: "o.mp4",
			Language: "fa", MasterObjectKey: key,
		})
		if err != nil {
			t.Fatalf("InsertEpisode: %v", err)
		}
		return ep
	}
	deleteOrphan := func(t *testing.T, pub pgtype.UUID, org int64) int64 {
		t.Helper()
		// Call the generated query explicitly: the org-scoped Store method of the
		// same name shadows the promoted db.Queries.DeleteOrphanEpisode.
		n, err := st.Queries.DeleteOrphanEpisode(ctx, db.DeleteOrphanEpisodeParams{PublicID: pub, OrgID: org})
		if err != nil {
			t.Fatalf("DeleteOrphanEpisode: %v", err)
		}
		return n
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

	// Fresh orphan (no key, still 'uploaded') -> removed.
	orphan := insert(t, pgtype.Text{})
	if n := deleteOrphan(t, orphan.PublicID, orgID); n != 1 {
		t.Fatalf("delete fresh orphan rows = %d, want 1", n)
	}
	if exists(t, orphan.PublicID) {
		t.Error("fresh orphan survived the rollback")
	}

	// Keyed row (upload started) -> untouched.
	keyed := insert(t, pgtype.Text{String: "k/masters/o.mp4", Valid: true})
	if n := deleteOrphan(t, keyed.PublicID, orgID); n != 0 {
		t.Errorf("delete keyed row rows = %d, want 0 (must not touch a started upload)", n)
	}
	if !exists(t, keyed.PublicID) {
		t.Error("keyed row was wrongly deleted")
	}

	// Advanced row (no longer 'uploaded') -> untouched.
	advanced := insert(t, pgtype.Text{})
	if _, err := st.UpdateEpisodeStatus(ctx, db.UpdateEpisodeStatusParams{
		PublicID: advanced.PublicID, OrgID: orgID, Status: "processing",
	}); err != nil {
		t.Fatalf("UpdateEpisodeStatus: %v", err)
	}
	if n := deleteOrphan(t, advanced.PublicID, orgID); n != 0 {
		t.Errorf("delete advanced row rows = %d, want 0", n)
	}
	if !exists(t, advanced.PublicID) {
		t.Error("advanced row was wrongly deleted")
	}

	// Org scoping: a fresh orphan is invisible to another org id.
	other := insert(t, pgtype.Text{})
	if n := deleteOrphan(t, other.PublicID, orgID+100000); n != 0 {
		t.Errorf("cross-org delete rows = %d, want 0", n)
	}
	if !exists(t, other.PublicID) {
		t.Error("cross-org delete removed the org's own orphan")
	}
}

// TestSeedIdempotent re-executes the seed migration's SQL directly and asserts
// row counts do not change.
func TestSeedIdempotent(t *testing.T) {
	dsn := requireDB(t)
	applyMigrations(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	seed, err := os.ReadFile("../../migrations/0002_seed.up.sql")
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	count := func(table string) int64 {
		var n int64
		if err := st.Pool().QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}

	before := map[string]int64{
		"orgs":        count("orgs"),
		"shows":       count("shows"),
		"users":       count("users"),
		"memberships": count("memberships"),
		"config":      count("config"),
	}

	// Re-run the seed (no args -> simple protocol -> multiple statements OK).
	if _, err := st.Pool().Exec(ctx, string(seed)); err != nil {
		t.Fatalf("re-exec seed: %v", err)
	}

	for table, want := range before {
		if got := count(table); got != want {
			t.Errorf("re-seed changed %s: %d -> %d", table, want, got)
		}
	}
}
