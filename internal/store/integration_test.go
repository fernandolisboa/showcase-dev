//go:build integration

// Seam tests for the persistence layer: they run real migrations and queries
// against a throwaway Postgres in Docker. Run with
//
//	go test -tags integration ./internal/store/...
package store

import (
	"context"
	"encoding/json"
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

func TestUsernameRoundTripAndUniqueness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	a, _ := s.UpsertOwnerByGitHubID(ctx, 100, "alice")
	b, _ := s.UpsertOwnerByGitHubID(ctx, 200, "bob")

	if err := s.SetUsername(ctx, a.ID, "alice-dev"); err != nil {
		t.Fatalf("set username: %v", err)
	}
	got, err := s.OwnerByUsername(ctx, "alice-dev")
	if err != nil || got.ID != a.ID {
		t.Fatalf("OwnerByUsername = (%+v, %v), want owner %d", got, err, a.ID)
	}
	if reloaded, _ := s.OwnerByID(ctx, a.ID); reloaded.Username != "alice-dev" {
		t.Errorf("OwnerByID username = %q, want alice-dev", reloaded.Username)
	}

	// A second Owner cannot take the same username (the UNIQUE constraint, mapped
	// to ErrUsernameTaken — exercises the real 23505 path the fakes can't).
	if err := s.SetUsername(ctx, b.ID, "alice-dev"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("duplicate username = %v, want ErrUsernameTaken", err)
	}

	// An unknown username is ErrNotFound.
	if _, err := s.OwnerByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("OwnerByUsername(missing) = %v, want ErrNotFound", err)
	}
}

func TestProjectRoundTripAndOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	alice, _ := s.UpsertOwnerByGitHubID(ctx, 100, "alice")
	bob, _ := s.UpsertOwnerByGitHubID(ctx, 200, "bob")

	manifest := []byte(`{"Services":[{"Name":"web","Role":"ui"}]}`)
	p, err := s.CreateProject(ctx, alice.ID, "blog", manifest)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if p.ID == "" || p.OwnerID != alice.ID || p.Name != "blog" || p.Published {
		t.Fatalf("unexpected project: %+v", p)
	}
	// jsonb reformats whitespace/key order, so assert a semantic round-trip, not bytes.
	var back map[string]any
	if err := json.Unmarshal(p.Manifest, &back); err != nil {
		t.Fatalf("stored manifest is not valid JSON: %v (%s)", err, p.Manifest)
	}
	if _, ok := back["Services"]; !ok {
		t.Errorf("manifest lost its services: %s", p.Manifest)
	}

	// Owner-scoped read returns it; another Owner cannot (no IDOR).
	got, err := s.GetOwnerProject(ctx, alice.ID, p.ID)
	if err != nil || got.ID != p.ID {
		t.Fatalf("GetOwnerProject = (%+v, %v), want project %s", got, err, p.ID)
	}
	if _, err := s.GetOwnerProject(ctx, bob.ID, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("cross-owner GetOwnerProject = %v, want ErrProjectNotFound", err)
	}

	// Per-owner unique name: alice can't reuse "blog"; bob can.
	if _, err := s.CreateProject(ctx, alice.ID, "blog", manifest); !errors.Is(err, ErrProjectNameTaken) {
		t.Errorf("duplicate name (same owner) = %v, want ErrProjectNameTaken", err)
	}
	if _, err := s.CreateProject(ctx, bob.ID, "blog", manifest); err != nil {
		t.Errorf("same name, different owner should be allowed: %v", err)
	}

	// List is owner-scoped.
	aliceProjects, err := s.ListProjectsByOwner(ctx, alice.ID)
	if err != nil || len(aliceProjects) != 1 || aliceProjects[0].ID != p.ID {
		t.Fatalf("ListProjectsByOwner(alice) = (%+v, %v), want exactly Alice's one project", aliceProjects, err)
	}

	// An unknown (but well-formed) id is ErrProjectNotFound.
	if _, err := s.GetOwnerProject(ctx, alice.ID, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("GetOwnerProject(missing) = %v, want ErrProjectNotFound", err)
	}
	// A non-uuid id can never match the uuid PK: it is not-found, not a 500 (it also
	// must not reach pgx, which cannot encode it for the uuid param).
	if _, err := s.GetOwnerProject(ctx, alice.ID, "not-a-uuid"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("GetOwnerProject(non-uuid) = %v, want ErrProjectNotFound", err)
	}

	// The play path only resolves PUBLISHED Projects. Alice's "blog" is an unpublished
	// draft, so a Guest cannot play it yet; once published it resolves by id; a
	// non-uuid id is not-found (so the FallbackSource defers to the fixture).
	if _, err := s.GetPublishedProject(ctx, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("GetPublishedProject(draft) = %v, want ErrProjectNotFound", err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE projects SET published = true WHERE id = $1", p.ID); err != nil {
		t.Fatalf("publish: %v", err)
	}
	pub, err := s.GetPublishedProject(ctx, p.ID)
	if err != nil || pub.ID != p.ID || !pub.Published {
		t.Fatalf("GetPublishedProject(published) = (%+v, %v), want the published project", pub, err)
	}
	if _, err := s.GetPublishedProject(ctx, "fixture"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("GetPublishedProject(non-uuid) = %v, want ErrProjectNotFound", err)
	}

	// FK cascade: deleting the Owner removes their Projects.
	if _, err := s.pool.Exec(ctx, "DELETE FROM owners WHERE id = $1", alice.ID); err != nil {
		t.Fatalf("delete owner: %v", err)
	}
	if _, err := s.GetOwnerProject(ctx, alice.ID, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("project should be gone after the owner is deleted, got %v", err)
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
