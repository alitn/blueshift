// Command demoseed prepares the deterministic sample episode that `make demo`
// serves at boot. It is a dev/demo-only tool (never deployed): it applies the
// dev/demo user identities, generates a fixed 2s master with ffmpeg into the
// local blob store at the episode's org-owned key, and inserts the episode row
// (Persian title with a ZWNJ, status 'uploaded'). The caller then runs the REAL
// worker ingest over that episode to render the proxy/audio and flip it 'ready'
// — demoseed never fabricates the proxy or the duration itself.
//
// It prints the episode's public id (the ep_… form) on stdout so the demo
// orchestration can invoke the worker with it; everything else goes to stderr.
//
// Usage:
//
//	demoseed -devseed fixtures/dev-seed.sql
//
// Environment: DATABASE_URL (required), BLOB_DIR (required — the local blob
// store root the app and worker share).
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"blueshift/internal/blob"
	"blueshift/internal/ids"
)

// The sample episode is fully deterministic so the committed visual baselines
// and the E2E flow are stable across runs and machines. Only the org's public
// id is random (assigned by migration 0002's uuidv7 default); demoseed resolves
// it at runtime and builds the storage key from it.
const (
	// sampleEpisodeUUID is the fixed episode public id (canonical uuid text). A
	// constant id keeps the ep_… string, the storage key, and the seeded-Library
	// baseline identical on every boot.
	sampleEpisodeUUID = "01890a1b-2c3d-7e4f-8a5b-000000000001"

	// sampleTitle is Persian and contains a ZWNJ (U+200C) between "گفت" and
	// "وگوی". It exercises RTL rendering and the verbatim ZWNJ-preservation
	// invariant end to end. Do not "normalise" this string.
	sampleTitle = "گفت\u200cوگوی نمونه"

	// sampleSourceFilename is the master's original name; it is also the search
	// term the visual spec uses to isolate this row from any E2E-uploaded rows.
	sampleSourceFilename = "sample-interview.mp4"

	sampleLanguage = "fa"

	// sampleCreatedAt is fixed so the Library "UPLOADED" column never drifts
	// day-to-day and the visual baseline stays stable indefinitely.
	sampleCreatedAt = "2025-01-15T09:30:00Z"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "demoseed: fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	devSeedPath := flag.String("devseed", "fixtures/dev-seed.sql", "path to the dev/demo user seed SQL")
	flag.Parse()

	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	blobDir := strings.TrimSpace(os.Getenv("BLOB_DIR"))
	if blobDir == "" {
		return fmt.Errorf("BLOB_DIR is required")
	}

	epUUID, err := parseUUID(sampleEpisodeUUID)
	if err != nil {
		return fmt.Errorf("parse sample episode uuid: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339, sampleCreatedAt)
	if err != nil {
		return fmt.Errorf("parse sample created_at: %w", err)
	}

	ctx := context.Background()

	// Simple query mode lets a single Exec run the multi-statement dev-seed file
	// and encodes parameters client-side for the guarded insert below.
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open pool: %w", err)
	}
	defer pool.Close()

	// 1. Dev/demo user identities (idempotent). Applying it here means `make
	//    demo` needs no psql client on the host.
	seedSQL, err := os.ReadFile(*devSeedPath) //nolint:gosec // dev tool, operator-supplied path.
	if err != nil {
		return fmt.Errorf("read dev-seed %q: %w", *devSeedPath, err)
	}
	if _, err := pool.Exec(ctx, string(seedSQL)); err != nil {
		return fmt.Errorf("apply dev-seed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "demoseed: applied %s\n", *devSeedPath)

	// 2. Resolve the pilot org (its public id is random) and its default show.
	var orgID int64
	var orgUUID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id, public_id FROM orgs WHERE name = 'Blueshift Pilot'`,
	).Scan(&orgID, &orgUUID); err != nil {
		return fmt.Errorf("resolve pilot org (did migrations run?): %w", err)
	}
	var showID int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM shows WHERE org_id = $1 AND deleted_at IS NULL ORDER BY id LIMIT 1`,
		orgID,
	).Scan(&showID); err != nil {
		return fmt.Errorf("resolve default show: %w", err)
	}

	orgEncoded := ids.Encode(ids.Org, orgUUID.Bytes)
	epEncoded := ids.Encode(ids.Episode, epUUID)

	// 3. Build the org-owned master key and generate a deterministic master at
	//    it (idempotent: skip if it already exists on a persistent demo DB).
	masterKey, err := blob.MasterKey(orgEncoded, epEncoded, sampleSourceFilename)
	if err != nil {
		return fmt.Errorf("build master key: %w", err)
	}
	masterPath := filepath.Join(blobDir, filepath.FromSlash(masterKey))
	if err := generateMaster(ctx, masterPath); err != nil {
		return fmt.Errorf("generate sample master: %w", err)
	}
	info, err := os.Stat(masterPath)
	if err != nil {
		return fmt.Errorf("stat sample master: %w", err)
	}

	// 4. Insert the episode as 'uploaded' with the master already present, so the
	//    worker's ingest claim (uploaded -> processing -> ready) drives it exactly
	//    as a real upload would. Idempotent on the fixed public id.
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (
			public_id, org_id, show_id, title, source_filename, language,
			status, master_object_key, master_size_bytes, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, 'uploaded', $7, $8, $9, $9
		)
		ON CONFLICT (public_id) DO NOTHING`,
		pgtype.UUID{Bytes: epUUID, Valid: true},
		orgID, showID, sampleTitle, sampleSourceFilename, sampleLanguage,
		masterKey, info.Size(), createdAt,
	); err != nil {
		return fmt.Errorf("insert sample episode: %w", err)
	}

	fmt.Fprintf(os.Stderr, "demoseed: sample episode %s master=%s (%d bytes)\n", epEncoded, masterKey, info.Size())
	// The only stdout line: the encoded episode id for the worker invocation.
	fmt.Println(epEncoded)
	return nil
}

// generateMaster writes a fixed 2s H.264+AAC master at path using ffmpeg
// directly (testsrc2 + a 440 Hz sine, per the task ruling). It is skipped if the
// file already exists so re-seeding a persistent demo database is a no-op.
func generateMaster(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(os.Stderr, "demoseed: master already present, skipping ffmpeg: %s\n", path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir masters: %w", err)
	}
	// Deterministic synthetic source: a 720p test pattern and a steady tone.
	args := []string{
		"-nostdin", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=2:size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest",
		path,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// parseUUID decodes a canonical 8-4-4-4-12 hyphenated UUID into its 16 bytes.
func parseUUID(s string) ([16]byte, error) {
	var out [16]byte
	clean := strings.ReplaceAll(s, "-", "")
	if len(clean) != 32 {
		return out, fmt.Errorf("uuid: want 32 hex digits, got %d", len(clean))
	}
	if _, err := hex.Decode(out[:], []byte(clean)); err != nil {
		return out, fmt.Errorf("uuid: %w", err)
	}
	return out, nil
}
