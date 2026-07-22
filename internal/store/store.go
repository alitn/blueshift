// Package store is the thin data-access layer: a pgx connection pool plus the
// sqlc-generated queries (embedded in internal/store/db). It owns no business
// logic — callers use the promoted *db.Queries methods and Ping for readiness.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"blueshift/internal/store/db"
)

// Store holds the connection pool and the generated query set. The embedded
// *db.Queries promotes every generated method onto *Store, so callers write
// st.GetOrg(...), st.InsertEpisode(...), etc.
type Store struct {
	pool *pgxpool.Pool
	*db.Queries
}

// Open parses the DSN and constructs a lazily-connecting pool. It does not
// dial: the first real connection happens on first use (or Ping), so the app
// can start before the database is reachable and surface that state via
// /readyz rather than crashing at boot.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}
	return &Store{pool: pool, Queries: db.New(pool)}, nil
}

// Ping acquires a connection and verifies the database answers. It is the
// readiness check wired into /readyz when DATABASE_URL is configured.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// Pool exposes the underlying pool for callers that need transactions or
// direct access. Kept minimal on purpose.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases all pooled connections.
func (s *Store) Close() { s.pool.Close() }
