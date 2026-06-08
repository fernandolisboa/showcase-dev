// Package store is the control-plane's persistence layer (ADR-0009: Postgres on
// the single VM). It owns the connection pool, the schema migrations, and the
// repositories for control-plane state (Owners, login sessions, and — in later
// #19 slices — Projects). This is distinct from the per-Session demo databases,
// which live inside the Session Stacks and never touch this store.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the control-plane database handle: a pgx connection pool plus the
// repository methods defined across this package's files.
type Store struct {
	pool *pgxpool.Pool
}

// Open parses the DSN, opens a pooled connection, and verifies it with a ping so
// a misconfigured DATABASE_URL fails at startup rather than on the first query.
// The caller owns the returned Store and must Close it.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool. Safe to call once at shutdown.
func (s *Store) Close() { s.pool.Close() }

// Ping reports whether the database is reachable — the control-plane readiness
// probe (/readyz). Liveness (/healthz) stays independent of the DB.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
