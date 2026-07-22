package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/ids"
	"blueshift/internal/store/db"
)

// TestPipelineClaimFinalize exercises the compare-and-set claim and the
// org-scoped finalizers against a real Postgres. It is skipped when
// TEST_DATABASE_URL is unset, like the other DB-backed store tests.
func TestPipelineClaimFinalize(t *testing.T) {
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
	if err := st.Pool().QueryRow(ctx, `SELECT id FROM orgs WHERE name = 'Blueshift Pilot'`).Scan(&orgID); err != nil {
		t.Fatalf("find org: %v", err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT id FROM shows WHERE org_id = $1 ORDER BY id LIMIT 1`, orgID).Scan(&showID); err != nil {
		t.Fatalf("find show: %v", err)
	}
	org, err := st.GetOrg(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrg: %v", err)
	}
	orgEncoded := ids.Encode(ids.Org, org.PublicID.Bytes)

	insert := func(masterKey string) db.Episode {
		ep, err := st.InsertEpisode(ctx, db.InsertEpisodeParams{
			OrgID: orgID, ShowID: showID, Title: "Ingest", SourceFilename: "m.mp4", Language: "fa",
			MasterObjectKey: pgtype.Text{String: masterKey, Valid: true},
		})
		if err != nil {
			t.Fatalf("InsertEpisode: %v", err)
		}
		return ep
	}

	// --- claim + mark ready ---
	ep := insert("k/masters/m.mp4")
	epEncoded := ids.Encode(ids.Episode, ep.PublicID.Bytes)

	claimed, ok, err := st.Claim(ctx, epEncoded)
	if err != nil || !ok {
		t.Fatalf("Claim = (%+v, %v, %v), want claimed", claimed, ok, err)
	}
	if claimed.OrgID != orgEncoded || claimed.PublicID != epEncoded {
		t.Errorf("claimed ids = %q/%q, want %q/%q", claimed.OrgID, claimed.PublicID, orgEncoded, epEncoded)
	}
	if claimed.MasterObjectKey != "k/masters/m.mp4" {
		t.Errorf("claimed master = %q", claimed.MasterObjectKey)
	}

	// A second claim is a no-op (already 'processing').
	if _, ok, _ := st.Claim(ctx, epEncoded); ok {
		t.Error("second Claim succeeded; want no-op")
	}

	if err := st.MarkReady(ctx, orgEncoded, epEncoded, "k/proxies/proxy-720p.mp4", 2000); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	got, err := st.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{PublicID: ep.PublicID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "ready" || got.ProxyObjectKey.String != "k/proxies/proxy-720p.mp4" || got.DurationMs.Int64 != 2000 {
		t.Errorf("after MarkReady: status=%q proxy=%q dur=%d", got.Status, got.ProxyObjectKey.String, got.DurationMs.Int64)
	}

	// --- cross-org finalize is a no-op ---
	ep2 := insert("k/masters/m2.mp4")
	ep2Encoded := ids.Encode(ids.Episode, ep2.PublicID.Bytes)
	if _, ok, err := st.Claim(ctx, ep2Encoded); err != nil || !ok {
		t.Fatalf("Claim ep2: ok=%v err=%v", ok, err)
	}
	// A different org's public id must not finalize ep2.
	otherOrg := ids.Encode(ids.Org, [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	if err := st.MarkFailed(ctx, otherOrg, ep2Encoded, "deadbeefdeadbeef"); err != nil {
		t.Fatalf("MarkFailed cross-org returned error: %v", err)
	}
	mid, _ := st.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{PublicID: ep2.PublicID, OrgID: orgID})
	if mid.Status != "processing" {
		t.Errorf("ep2 status after cross-org MarkFailed = %q, want processing (untouched)", mid.Status)
	}
	// The owning org finalizes it.
	if err := st.MarkFailed(ctx, orgEncoded, ep2Encoded, "deadbeefdeadbeef"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	fin, _ := st.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{PublicID: ep2.PublicID, OrgID: orgID})
	if fin.Status != "failed" || fin.ErrorID.String != "deadbeefdeadbeef" {
		t.Errorf("after MarkFailed: status=%q error_id=%q", fin.Status, fin.ErrorID.String)
	}
}
