package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// advisoryLockKey is an arbitrary fixed key so that, if two control-plane
// processes start at once, only one runs migrations at a time — the other blocks
// on pg_advisory_lock and then sees the work already done, rather than both
// racing to apply the same files.
const advisoryLockKey int64 = 0x53686f7763617365 // "Showcase"

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version    bigint      PRIMARY KEY,
	name       text        NOT NULL,
	checksum   text        NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// migration is one embedded .sql file: its numeric version (the NNNN prefix),
// the SQL body, and a checksum used to detect a file edited after it was applied.
type migration struct {
	version  int64
	name     string
	sql      string
	checksum string
}

// Migrate applies every pending embedded migration in version order, exactly
// once, inside a per-migration transaction. Migrations are append-only: a file
// whose checksum no longer matches the recorded one is a hard error, so an
// already-applied migration can never be silently rewritten under a running DB.
func (s *Store) Migrate(ctx context.Context) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	// Serialize migrators across processes; release even if ctx is later cancelled.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	if _, err := conn.Exec(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	applied, err := loadApplied(ctx, conn)
	if err != nil {
		return err
	}

	// Refuse to run against a database migrated by a NEWER binary: an applied
	// version this binary doesn't know about means a rollback, and continuing
	// would run today's code against a future schema. Fail fast instead.
	var maxKnown int64
	if len(migs) > 0 {
		maxKnown = migs[len(migs)-1].version // migs is version-sorted ascending
	}
	for v := range applied {
		if v > maxKnown {
			return fmt.Errorf("database has migration %d applied which this binary does not know (max known %d); deploy a newer binary rather than rolling back", v, maxKnown)
		}
	}

	for _, m := range migs {
		if rec, ok := applied[m.version]; ok {
			if rec != m.checksum {
				return fmt.Errorf("migration %04d_%s was modified after being applied (checksum mismatch); migrations are append-only", m.version, m.name)
			}
			continue
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return fmt.Errorf("apply migration %04d_%s: %w", m.version, m.name, err)
		}
	}
	return nil
}

// applyOne runs the migration body and records it atomically, so a crash mid-file
// leaves neither a half-applied schema nor a bookkeeping row claiming success.
func applyOne(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
		m.version, m.name, m.checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// loadApplied reads the recorded (version -> checksum) of already-applied
// migrations.
func loadApplied(ctx context.Context, conn *pgxpool.Conn) (map[int64]string, error) {
	rows, err := conn.Query(ctx, "SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("load applied migrations: %w", err)
	}
	defer rows.Close()
	applied := map[int64]string{}
	for rows.Next() {
		var v int64
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, err
		}
		applied[v] = sum
	}
	return applied, rows.Err()
}

// loadMigrations parses every embedded migrations/NNNN_name.sql into a
// version-sorted slice, rejecting malformed names and duplicate versions so a
// packaging mistake can't silently skip or double-apply a migration.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var migs []migration
	seen := map[int64]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}
		if seen[version] {
			return nil, fmt.Errorf("duplicate migration version %d (%s)", version, e.Name())
		}
		seen[version] = true
		body, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(body)
		migs = append(migs, migration{
			version:  version,
			name:     name,
			sql:      string(body),
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}

// parseMigrationName splits "0001_owners.sql" into (1, "owners").
func parseMigrationName(filename string) (int64, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	prefix, name, ok := strings.Cut(base, "_")
	if !ok || prefix == "" || name == "" {
		return 0, "", fmt.Errorf("migration %q must be named NNNN_name.sql", filename)
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("migration %q has a non-numeric version prefix: %w", filename, err)
	}
	return version, name, nil
}
