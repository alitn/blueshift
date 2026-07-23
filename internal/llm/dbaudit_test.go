package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/store"
	"blueshift/internal/store/db"
)

// requireDB returns TEST_DATABASE_URL or skips. These tests run under CI/`make
// demo` where a scratch Postgres is provisioned; locally they no-op so `make
// check` is green without a database (mirrors the store package convention).
func requireDB(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("skip: TEST_DATABASE_URL not set (DB-backed audit test needs a scratch Postgres)")
	}
	return dsn
}

func migrateURL(dsn string) string {
	if i := strings.Index(dsn, "://"); i >= 0 {
		return "pgx5" + dsn[i:]
	}
	return dsn
}

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

// openStore migrates and opens the store, returning it plus the seed org id.
func openStore(t *testing.T, ctx context.Context) (*store.Store, int64) {
	t.Helper()
	dsn := requireDB(t)
	applyMigrations(t, dsn)
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(st.Close)
	var orgID int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT id FROM orgs WHERE name = 'Blueshift Pilot'`).Scan(&orgID); err != nil {
		t.Fatalf("find seed org: %v", err)
	}
	return st, orgID
}

// createEpisode inserts an episode under the org's default show and returns its
// internal id, so the audit's episode_id foreign key can be exercised for real.
func createEpisode(t *testing.T, st *store.Store, ctx context.Context, orgID int64) int64 {
	t.Helper()
	show, err := st.GetDefaultShowForOrg(ctx, orgID)
	if err != nil {
		t.Fatalf("GetDefaultShowForOrg: %v", err)
	}
	ep, err := st.InsertEpisode(ctx, db.InsertEpisodeParams{
		OrgID:          orgID,
		ShowID:         show.ID,
		Title:          "Audit Test Episode",
		SourceFilename: "audit.mp4",
		Language:       "fa",
	})
	if err != nil {
		t.Fatalf("InsertEpisode: %v", err)
	}
	return ep.ID
}

// auditedRow is one llm_calls row read back for assertions.
type auditedRow struct {
	model     string
	promptVer string
	inputHash string
	raw       []byte
	cost      pgtype.Int4
	latency   pgtype.Int4
	status    pgtype.Text
	orgID     int64
	episodeID pgtype.Int8
}

func latestRowByHash(t *testing.T, st *store.Store, ctx context.Context, hash string) auditedRow {
	t.Helper()
	var r auditedRow
	err := st.Pool().QueryRow(ctx,
		`SELECT model, prompt_version, input_hash, raw_response, cost_cents, latency_ms, status, org_id, episode_id
		   FROM llm_calls WHERE input_hash = $1 ORDER BY id DESC LIMIT 1`, hash).
		Scan(&r.model, &r.promptVer, &r.inputHash, &r.raw, &r.cost, &r.latency, &r.status, &r.orgID, &r.episodeID)
	if err != nil {
		t.Fatalf("read llm_calls row: %v", err)
	}
	return r
}

func countRowsByHash(t *testing.T, st *store.Store, ctx context.Context, hash string) int {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM llm_calls WHERE input_hash = $1`, hash).Scan(&n); err != nil {
		t.Fatalf("count llm_calls rows: %v", err)
	}
	return n
}

// TestDBAuditSuccessRow drives Client.Generate through the real store Auditor and
// asserts the persisted row: model, prompt_version, stable input_hash, verbatim
// raw_response (jsonb preserves the value), cost, latency, status, and scoping.
func TestDBAuditSuccessRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, orgID := openStore(t, ctx)

	fixture := loadFixture(t, "gemini_success.json")
	fe := &fakeEngine{lbl: "bs-lm-1", mdl: "test-model-x", steps: []fakeStep{{
		res: result{
			rawBody: fixture,
			output:  []byte(`{"answer":"Lisbon","count":2}`),
			usage:   usage{inputTokens: 1_200_000, outputTokens: 800_000},
		},
	}}}
	c := &Client{
		reg:   map[string]registered{"bs-lm-1": {eng: fe, price: &Price{InputPerMTokCents: 100, OutputPerMTokCents: 300}}},
		audit: NewDBAuditor(st),
		log:   discardLogger(),
		now:   steppedClock(5 * time.Millisecond),
	}

	episodeID := createEpisode(t, st, ctx, orgID)

	var out sampleOut
	req := baseRequest(&out)
	req.OrgID = orgID
	req.EpisodeID = episodeID // exercise the real episode_id foreign key

	if _, err := c.Generate(ctx, req); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	wantHash, err := hashInput(fe.model(), req)
	if err != nil {
		t.Fatalf("hashInput: %v", err)
	}
	row := latestRowByHash(t, st, ctx, wantHash)

	if row.model != "test-model-x" {
		t.Errorf("model = %q, want test-model-x", row.model)
	}
	if row.promptVer != "v1" {
		t.Errorf("prompt_version = %q, want v1", row.promptVer)
	}
	if row.inputHash != wantHash {
		t.Errorf("input_hash = %q, want %q", row.inputHash, wantHash)
	}
	if !row.status.Valid || row.status.String != statusOK {
		t.Errorf("status = %+v, want ok", row.status)
	}
	if !row.cost.Valid || row.cost.Int32 != 360 {
		t.Errorf("cost_cents = %+v, want 360", row.cost)
	}
	if !row.latency.Valid || row.latency.Int32 != 5 {
		t.Errorf("latency_ms = %+v, want 5", row.latency)
	}
	if row.orgID != orgID {
		t.Errorf("org_id = %d, want %d", row.orgID, orgID)
	}
	if !row.episodeID.Valid || row.episodeID.Int64 != episodeID {
		t.Errorf("episode_id = %+v, want %d", row.episodeID, episodeID)
	}

	// raw_response verbatim: jsonb normalizes whitespace/key-order but preserves
	// the value in full. Assert semantic equality and that provider-specific
	// fields survived (nothing was stripped or summarized).
	var stored, original any
	if err := json.Unmarshal(row.raw, &stored); err != nil {
		t.Fatalf("stored raw_response not JSON: %v", err)
	}
	if err := json.Unmarshal(fixture, &original); err != nil {
		t.Fatalf("fixture not JSON: %v", err)
	}
	if !reflect.DeepEqual(stored, original) {
		t.Errorf("raw_response not stored verbatim:\n got %v\nwant %v", stored, original)
	}
	if !strings.Contains(string(row.raw), "usageMetadata") || !strings.Contains(string(row.raw), "modelVersion") {
		t.Error("raw_response lost provider-specific fields; not verbatim")
	}
}

// TestDBAuditRetryThenFailRows: an invalid output on both attempts writes two
// 'invalid' rows sharing the input_hash, and the caller sees a neutral error.
func TestDBAuditRetryThenFailRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, orgID := openStore(t, ctx)

	bad := fakeStep{res: result{
		rawBody: []byte(`{"note":"still valid json envelope"}`),
		output:  []byte(`{"answer":"x","count":1,"surprise":true}`), // unknown field
		usage:   usage{inputTokens: 10, outputTokens: 5},
	}}
	fe := &fakeEngine{lbl: "bs-lm-1", mdl: "test-model-retry", steps: []fakeStep{bad, bad}}
	c := &Client{
		reg:   map[string]registered{"bs-lm-1": {eng: fe, price: &Price{InputPerMTokCents: 1, OutputPerMTokCents: 1}}},
		audit: NewDBAuditor(st),
		log:   discardLogger(),
		now:   steppedClock(3 * time.Millisecond),
	}

	var out sampleOut
	req := baseRequest(&out)
	req.OrgID = orgID
	req.EpisodeID = 0 // exercise the NULL episode_id path
	req.PromptVersion = "retry-v1"

	wantHash, herr := hashInput(fe.model(), req)
	if herr != nil {
		t.Fatalf("hashInput: %v", herr)
	}
	// llm_calls is append-only and not reset between runs, so assert the delta
	// this single Generate contributes, not the absolute count for the hash.
	before := countRowsByHash(t, st, ctx, wantHash)

	_, err := c.Generate(ctx, req)
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
	assertNeutral(t, err)

	if added := countRowsByHash(t, st, ctx, wantHash) - before; added != 2 {
		t.Fatalf("audit rows added by retry = %d, want 2", added)
	}
	row := latestRowByHash(t, st, ctx, wantHash)
	if !row.status.Valid || row.status.String != statusInvalid {
		t.Errorf("status = %+v, want invalid", row.status)
	}
}
