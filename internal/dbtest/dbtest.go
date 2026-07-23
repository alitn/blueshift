// Package dbtest gives DB-backed Go tests a private, throwaway Postgres database
// so they never read or write the database named in TEST_DATABASE_URL (the
// shared dev/CI server). Residue left in that shared database has broken global
// asserts and blocked the commit gate; per-run scratch isolation removes the
// coupling entirely.
//
// A package with DB-backed tests wires this in from its TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(dbtest.RunMain(m)) }
//
// When TEST_DATABASE_URL is set, RunMain creates blueshift_test_<pid>_<rand> on
// the same server, migrates it in-process, exposes its DSN via DSN(), runs the
// tests, and drops the scratch database on a green run (keeping it, name logged,
// on a red run for post-mortem debugging). When TEST_DATABASE_URL is unset it
// just runs the tests, which skip individually via their requireDB helper — so
// `make check` stays green without a database.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
)

// dsn holds the scratch database DSN for the current run, or "" when
// TEST_DATABASE_URL was unset. It is set once by RunMain before any test runs
// and is read-only afterwards; read it via DSN().
var dsn string

// DSN returns the scratch database DSN for the current test run, or "" when no
// server was configured (TEST_DATABASE_URL unset). DB-backed tests skip on "".
func DSN() string { return dsn }

// RunMain wraps a package's TestMain (see the package doc). It returns the exit
// code the caller must pass to os.Exit.
func RunMain(m *testing.M) int {
	serverDSN := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if serverDSN == "" {
		// No server: DB-backed tests skip individually; non-DB tests still run.
		return m.Run()
	}

	name, scratch, err := create(serverDSN)
	if err != nil {
		// Fail loudly rather than silently falling back to the named database:
		// a set-but-unusable server is a misconfiguration the gate must surface
		// (e.g. the CI/local role lacks CREATEDB).
		log.Printf("dbtest: cannot provision scratch database on %s: %v", redact(serverDSN), err)
		return 1
	}

	if err := migrateUp(scratch); err != nil {
		log.Printf("dbtest: cannot migrate scratch database %q: %v", name, err)
		_ = drop(serverDSN, name) // best-effort; no tests ran
		return 1
	}

	dsn = scratch
	code := m.Run()

	if code == 0 {
		if err := drop(serverDSN, name); err != nil {
			log.Printf("dbtest: WARNING: could not drop scratch database %q: %v", name, err)
		}
	} else {
		log.Printf("dbtest: tests failed — keeping scratch database %q for debugging "+
			"(remove with: DROP DATABASE %s WITH (FORCE);)", name, pgx.Identifier{name}.Sanitize())
	}
	return code
}

// create makes an empty scratch database on the configured server and returns
// its name and DSN. It retries the transient template-contention error that can
// surface when sibling package binaries (from `go test ./...`) create databases
// concurrently.
func create(serverDSN string) (name, scratch string, err error) {
	name = scratchName()
	scratch, err = withDatabase(serverDSN, name)
	if err != nil {
		return "", "", err
	}
	stmt := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
	for attempt := 0; ; attempt++ {
		err = runStmt(serverDSN, stmt)
		if err == nil {
			return name, scratch, nil
		}
		// `source database "template1" is being accessed by other users` is
		// transient under concurrent CREATE DATABASE; back off and retry.
		if attempt < 5 && strings.Contains(err.Error(), "being accessed by other users") {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return "", "", fmt.Errorf("create database %q: %w", name, err)
	}
}

// drop removes the scratch database. WITH (FORCE) terminates any backend a test
// left connected (PG13+), so a leaked pool never blocks teardown; the database
// is ours, so forcing is safe.
func drop(serverDSN, name string) error {
	stmt := "DROP DATABASE IF EXISTS " + pgx.Identifier{name}.Sanitize() + " WITH (FORCE)"
	if err := runStmt(serverDSN, stmt); err != nil {
		return fmt.Errorf("drop database %q: %w", name, err)
	}
	return nil
}

// runStmt opens a short-lived connection to the server DSN and runs one
// statement. CREATE/DROP DATABASE cannot run inside a transaction, so a bare
// Exec on a single connection is used. The connection targets the configured
// server database only to bootstrap DDL that creates/removes another database;
// it never modifies the named database's contents.
func runStmt(serverDSN, stmt string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, serverDSN)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, stmt)
	return err
}

// migrateUp applies every up migration to the scratch database in-process, using
// the same golang-migrate file source the store harness has always used.
func migrateUp(scratch string) error {
	dir, err := migrationsDir()
	if err != nil {
		return err
	}
	m, err := migrate.New("file://"+dir, migrateURL(scratch))
	if err != nil {
		return fmt.Errorf("migrate.New: %w", err)
	}
	defer func() {
		if serr, derr := m.Close(); serr != nil || derr != nil {
			log.Printf("dbtest: migrate close: source=%v db=%v", serr, derr)
		}
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// scratchName is a unique, identifier-safe database name for this run. The pid
// keeps it human-traceable; the random suffix avoids collisions across the
// sibling package binaries `go test ./...` may run concurrently.
func scratchName() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("blueshift_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	}
	return fmt.Sprintf("blueshift_test_%d_%s", os.Getpid(), hex.EncodeToString(b))
}

// withDatabase returns serverDSN with its database (URL path) replaced by name.
// The project's DSNs are URL form; a keyword/value DSN is rejected with a clear
// error rather than silently mis-parsed.
func withDatabase(serverDSN, name string) (string, error) {
	if !strings.Contains(serverDSN, "://") {
		return "", errors.New("TEST_DATABASE_URL must be a URL-form DSN (postgres://...); got a keyword/value DSN")
	}
	u, err := url.Parse(serverDSN)
	if err != nil {
		return "", fmt.Errorf("parse TEST_DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// migrateURL rewrites a postgres:// DSN to the scheme golang-migrate's pgx/v5
// driver registers ("pgx5"). Mirrors the store harness's historic helper.
func migrateURL(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		return "pgx5" + raw[i:]
	}
	return raw
}

// redact returns serverDSN with any password masked, safe for logs.
func redact(serverDSN string) string {
	u, err := url.Parse(serverDSN)
	if err != nil {
		return "the configured server"
	}
	return u.Redacted()
}

// migrationsDir walks up from the test's working directory (its package dir) to
// the first ancestor holding a migrations/ directory, so the same harness works
// from internal/store, internal/llm, or any future DB-backed package with no
// hardcoded relative path.
func migrationsDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		candidate := filepath.Join(dir, "migrations")
		if fi, statErr := os.Stat(candidate); statErr == nil && fi.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("dbtest: could not locate a migrations/ directory above the test working dir")
		}
		dir = parent
	}
}
