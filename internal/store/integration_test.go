//go:build integration

// Seam tests for the persistence layer: they run real migrations and queries
// against a throwaway Postgres in Docker. Run with
//
//	go test -tags integration ./internal/store/...
package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMigrateAndOwnerRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Idempotent: a second run applies nothing and does not error.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second migrate should be a no-op: %v", err)
	}

	// First sign-in inserts the Owner.
	o, err := s.UpsertOwnerByGitHubID(ctx, 4242, "octocat")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if o.ID == 0 || o.GitHubUserID != 4242 || o.GitHubLogin != "octocat" {
		t.Fatalf("unexpected owner: %+v", o)
	}

	got, err := s.OwnerByID(ctx, o.ID)
	if err != nil || got != o {
		t.Fatalf("OwnerByID = (%+v, %v), want %+v", got, err, o)
	}

	// Re-sign-in after a GitHub rename: same id, updated login, no new row.
	o2, err := s.UpsertOwnerByGitHubID(ctx, 4242, "octocat-renamed")
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if o2.ID != o.ID {
		t.Errorf("rename created a new owner: id %d != %d", o2.ID, o.ID)
	}
	if o2.GitHubLogin != "octocat-renamed" {
		t.Errorf("login not refreshed on re-sign-in: %q", o2.GitHubLogin)
	}

	if _, err := s.OwnerByID(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("OwnerByID(missing) = %v, want ErrNotFound", err)
	}
}

func TestLoginSessionRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	owner, err := s.UpsertOwnerByGitHubID(ctx, 7, "dev")
	if err != nil {
		t.Fatalf("upsert owner: %v", err)
	}

	// A live session resolves to its owner.
	if err := s.CreateLoginSession(ctx, "hash-live", owner.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	got, err := s.OwnerByLoginSession(ctx, "hash-live")
	if err != nil || got.ID != owner.ID {
		t.Fatalf("OwnerByLoginSession = (%+v, %v), want owner %d", got, err, owner.ID)
	}

	// An expired session is treated as absent and is pruned.
	if err := s.CreateLoginSession(ctx, "hash-expired", owner.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	if _, err := s.OwnerByLoginSession(ctx, "hash-expired"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session should resolve to ErrNotFound, got %v", err)
	}
	n, err := s.DeleteExpiredLoginSessions(ctx)
	if err != nil || n != 1 {
		t.Errorf("DeleteExpiredLoginSessions = (%d, %v), want (1, nil)", n, err)
	}

	// Logout removes the live session; it then resolves to ErrNotFound.
	if err := s.DeleteLoginSession(ctx, "hash-live"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := s.OwnerByLoginSession(ctx, "hash-live"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted session should resolve to ErrNotFound, got %v", err)
	}
}

// A migration file edited after being applied must be rejected (append-only).
func TestMigrateRejectsModifiedMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Simulate a file whose contents changed after it was applied by corrupting the
	// recorded checksum; the next Migrate must refuse rather than silently proceed.
	if _, err := s.pool.Exec(ctx, "UPDATE schema_migrations SET checksum = 'tampered'"); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	err := s.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "modified after being applied") {
		t.Fatalf("Migrate after tamper = %v, want a modified-migration error", err)
	}
}

// startStore launches a throwaway Postgres, waits for it to accept connections,
// and returns an open Store (registered for cleanup). Skips if Docker is absent.
func startStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("docker not available; skipping store integration test")
	}

	out, err := exec.Command("docker", "run", "-d",
		"-e", "POSTGRES_USER=test", "-e", "POSTGRES_PASSWORD=test", "-e", "POSTGRES_DB=test",
		"-p", "127.0.0.1:0:5432", "postgres:17",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("start postgres: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _, _ = exec.Command("docker", "rm", "-f", id).CombinedOutput() })

	portOut, err := exec.Command("docker", "port", id, "5432/tcp").CombinedOutput()
	if err != nil {
		t.Fatalf("docker port: %v\n%s", err, portOut)
	}
	// `docker port` can emit several lines (v4 + v6); take the first and parse it
	// with net.SplitHostPort so an IPv6 form like "[::]:49153" doesn't misparse.
	hostPort := strings.TrimSpace(strings.SplitN(string(portOut), "\n", 2)[0])
	_, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("parse docker port output %q: %v", hostPort, err)
	}
	dsn := fmt.Sprintf("postgres://test:test@127.0.0.1:%s/test?sslmode=disable", port)

	// Postgres takes a few seconds to accept connections after the container starts.
	deadline := time.Now().Add(60 * time.Second)
	for {
		s, err := Open(ctx, dsn)
		if err == nil {
			t.Cleanup(s.Close)
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres never became ready: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
