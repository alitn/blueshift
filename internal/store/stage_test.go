package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/ids"
	"blueshift/internal/store/db"
)

// stageFixture spins up an org/show and an inserted 'uploaded' episode for the
// stage-machine tests, returning the encoded org/episode ids plus the internal
// episode id (for direct SQL pokes). It reuses the package's DB-backed harness.
type stageFixture struct {
	st         *Store
	ctx        context.Context
	orgEncoded string
	epEncoded  string
	epUUID     pgtype.UUID
	epID       int64
}

func newStageFixture(t *testing.T) stageFixture {
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
	ep, err := st.InsertEpisode(ctx, db.InsertEpisodeParams{
		OrgID: orgID, ShowID: showID, Title: "Stage", SourceFilename: "m.mp4", Language: "fa",
		MasterObjectKey: pgtype.Text{String: "k/masters/m.mp4", Valid: true},
	})
	if err != nil {
		t.Fatalf("InsertEpisode: %v", err)
	}
	deleteEpisodeOnCleanup(t, st, ep.ID)
	return stageFixture{
		st:         st,
		ctx:        ctx,
		orgEncoded: ids.Encode(ids.Org, org.PublicID.Bytes),
		epEncoded:  ids.Encode(ids.Episode, ep.PublicID.Bytes),
		epUUID:     ep.PublicID,
		epID:       ep.ID,
	}
}

type stageSnap struct {
	status    string
	stage     string // "" when current_stage is NULL
	claimNull bool
	proxy     string
	duration  int64
}

func (f stageFixture) read(t *testing.T) stageSnap {
	t.Helper()
	var s stageSnap
	var stage, proxy pgtype.Text
	var dur pgtype.Int8
	if err := f.st.Pool().QueryRow(f.ctx,
		`SELECT status, current_stage, claimed_at IS NULL, proxy_object_key, duration_ms
		   FROM episodes WHERE id = $1`, f.epID).
		Scan(&s.status, &stage, &s.claimNull, &proxy, &dur); err != nil {
		t.Fatalf("read episode: %v", err)
	}
	s.stage, s.proxy, s.duration = stage.String, proxy.String, dur.Int64
	return s
}

// TestStageMachineClaimAdvanceFinalize walks the full five-stage sequence against
// a real Postgres: an entry claim, an intermediate handoff (AdvanceStage), the
// continuation claims that advance current_stage one step at a time, and the
// terminal MarkReady. It proves the stage-aware claim stamps current_stage +
// claimed_at every step (the m1-pipeline-robustness "atomic claim+stamp"
// invariant, now per-stage) and that the handoff never clears claimed_at.
func TestStageMachineClaimAdvanceFinalize(t *testing.T) {
	f := newStageFixture(t)
	st, ctx := f.st, f.ctx

	// Entry claim: uploaded -> processing, current_stage = ingest, claimed_at set.
	if _, ok, err := st.Claim(ctx, f.epEncoded, "ingest", ""); err != nil || !ok {
		t.Fatalf("Claim(ingest, entry): ok=%v err=%v", ok, err)
	}
	if s := f.read(t); s.status != "processing" || s.stage != "ingest" || s.claimNull {
		t.Fatalf("after entry claim = %+v, want processing/ingest/claimed", s)
	}

	// A second entry claim is a no-op: an entry stage cannot be replayed once the
	// episode left 'uploaded' (loop-proof).
	if _, ok, err := st.Claim(ctx, f.epEncoded, "ingest", ""); err != nil || ok {
		t.Fatalf("replayed entry Claim(ingest) ok=%v err=%v, want no-op", ok, err)
	}

	// Intermediate handoff: records outputs, stays processing at ingest, re-arms
	// claimed_at (never left NULL, which the sweep would fail immediately).
	if err := st.AdvanceStage(ctx, f.orgEncoded, f.epEncoded, "ingest", "k/proxies/p.mp4", 1000); err != nil {
		t.Fatalf("AdvanceStage(ingest): %v", err)
	}
	if s := f.read(t); s.status != "processing" || s.stage != "ingest" || s.claimNull || s.proxy != "k/proxies/p.mp4" || s.duration != 1000 {
		t.Fatalf("after handoff = %+v, want processing/ingest/claimed + outputs recorded", s)
	}

	// Continuation claims advance current_stage one predecessor step at a time.
	steps := []struct{ stage, prev string }{
		{"transcribe", "ingest"},
		{"diarize", "transcribe"},
		{"moments", "diarize"},
		{"render", "moments"},
	}
	for i, s := range steps {
		if _, ok, err := st.Claim(ctx, f.epEncoded, s.stage, s.prev); err != nil || !ok {
			t.Fatalf("Claim(%s from %s): ok=%v err=%v", s.stage, s.prev, ok, err)
		}
		if got := f.read(t); got.status != "processing" || got.stage != s.stage || got.claimNull {
			t.Fatalf("after claim %s = %+v, want processing/%s/claimed", s.stage, got, s.stage)
		}
		// Hand off every non-terminal stage so the next predecessor guard matches.
		if i < len(steps)-1 {
			if err := st.AdvanceStage(ctx, f.orgEncoded, f.epEncoded, s.stage, "", 0); err != nil {
				t.Fatalf("AdvanceStage(%s): %v", s.stage, err)
			}
		}
	}

	// Terminal finalize from the last stage (render): ready, claimed_at cleared,
	// current_stage preserved so the row records which stage it reached.
	if err := st.MarkReady(ctx, f.orgEncoded, f.epEncoded, "k/proxies/final.mp4", 2000); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if s := f.read(t); s.status != "ready" || s.stage != "render" || !s.claimNull || s.proxy != "k/proxies/final.mp4" || s.duration != 2000 {
		t.Fatalf("after MarkReady = %+v, want ready/render/unclaimed + final outputs", s)
	}
}

// TestStageClaimGuardsRejectSkipReplayAndCrossOrg proves the continuation claim
// and the handoff can neither skip a stage, replay one, nor cross tenants — the
// guards that make auto-advance loop- and skip-proof and keep every write
// in-tenant.
func TestStageClaimGuardsRejectSkipReplayAndCrossOrg(t *testing.T) {
	f := newStageFixture(t)
	st, ctx := f.st, f.ctx

	if _, ok, err := st.Claim(ctx, f.epEncoded, "ingest", ""); err != nil || !ok {
		t.Fatalf("Claim(ingest): ok=%v err=%v", ok, err)
	}

	// Skip: claiming a stage whose predecessor is not the current stage no-ops.
	if _, ok, _ := st.Claim(ctx, f.epEncoded, "diarize", "transcribe"); ok {
		t.Error("Claim(diarize from transcribe) succeeded while at ingest; want no-op (no skipping)")
	}
	// Replay: re-running the entry claim while 'processing' no-ops.
	if _, ok, _ := st.Claim(ctx, f.epEncoded, "ingest", ""); ok {
		t.Error("Claim(ingest, entry) succeeded while processing; want no-op (no replay)")
	}
	if s := f.read(t); s.status != "processing" || s.stage != "ingest" {
		t.Errorf("episode state = %+v, want untouched processing/ingest", s)
	}

	// Cross-org handoff no-ops (foreign org resolves but matches no row for it).
	otherOrg := ids.Encode(ids.Org, [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	if err := st.AdvanceStage(ctx, otherOrg, f.epEncoded, "ingest", "k/x.mp4", 9); err != nil {
		t.Fatalf("cross-org AdvanceStage returned error: %v", err)
	}
	// Wrong-stage handoff no-ops (current_stage is ingest, not transcribe).
	if err := st.AdvanceStage(ctx, f.orgEncoded, f.epEncoded, "transcribe", "k/x.mp4", 9); err != nil {
		t.Fatalf("wrong-stage AdvanceStage returned error: %v", err)
	}
	if s := f.read(t); s.proxy != "" || s.duration != 0 || s.stage != "ingest" {
		t.Errorf("episode state after no-op handoffs = %+v, want no outputs, still ingest", s)
	}
}

// TestStageFailureAndSweepPreserveStage proves the failure paths record which
// stage the episode was on: MarkFailed and the stale-claim sweep both flip to
// 'failed' and clear claimed_at while leaving current_stage set, so the Library
// can label "FAILED — <STAGE>".
func TestStageFailureAndSweepPreserveStage(t *testing.T) {
	f := newStageFixture(t)
	st, ctx := f.st, f.ctx

	// MarkFailed on a claimed stage preserves current_stage.
	if _, ok, err := st.Claim(ctx, f.epEncoded, "ingest", ""); err != nil || !ok {
		t.Fatalf("Claim(ingest): ok=%v err=%v", ok, err)
	}
	if err := st.MarkFailed(ctx, f.orgEncoded, f.epEncoded, "deadbeefdeadbeef"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if s := f.read(t); s.status != "failed" || s.stage != "ingest" || !s.claimNull {
		t.Errorf("after MarkFailed = %+v, want failed/ingest/unclaimed (stage preserved)", s)
	}

	// The stale-claim sweep preserves current_stage too. Reset the row back to a
	// fresh 'uploaded' (as retry would), re-claim it, then backdate its claim past
	// the TTL and sweep.
	if _, err := st.Pool().Exec(ctx,
		`UPDATE episodes SET status = 'uploaded', claimed_at = NULL, error_id = NULL, current_stage = NULL WHERE id = $1`,
		f.epID); err != nil {
		t.Fatalf("reset episode: %v", err)
	}
	if _, ok, err := st.Claim(ctx, f.epEncoded, "ingest", ""); err != nil || !ok {
		t.Fatalf("re-Claim(ingest): ok=%v err=%v", ok, err)
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE episodes SET claimed_at = now() - make_interval(hours => 7) WHERE id = $1`, f.epID); err != nil {
		t.Fatalf("backdate claim: %v", err)
	}
	if _, err := st.SweepStuckProcessingEpisodes(ctx, 5*time.Hour); err != nil {
		t.Fatalf("SweepStuckProcessingEpisodes: %v", err)
	}
	if s := f.read(t); s.status != "failed" || s.stage != "ingest" || !s.claimNull {
		t.Errorf("after sweep = %+v, want failed/ingest/unclaimed (stage preserved)", s)
	}
}
