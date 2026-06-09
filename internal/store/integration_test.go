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
	// Publishing flips published AND records the built commit, owner-scoped. A
	// cross-owner publish affects no row.
	const sha = "0123456789abcdef0123456789abcdef01234567"
	if err := s.PublishProject(ctx, bob.ID, p.ID, sha, nil); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("cross-owner PublishProject = %v, want ErrProjectNotFound", err)
	}
	if err := s.PublishProject(ctx, alice.ID, p.ID, sha, nil); err != nil {
		t.Fatalf("PublishProject: %v", err)
	}
	pub, err := s.GetPublishedProject(ctx, p.ID)
	if err != nil || pub.ID != p.ID || !pub.Published {
		t.Fatalf("GetPublishedProject(published) = (%+v, %v), want the published project", pub, err)
	}
	// The commit SHA round-trips through the play read path (the SpecFunc reads it).
	if pub.CommitSHA != sha {
		t.Errorf("published commit_sha = %q, want %q", pub.CommitSHA, sha)
	}
	if _, err := s.GetPublishedProject(ctx, "fixture"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("GetPublishedProject(non-uuid) = %v, want ErrProjectNotFound", err)
	}

	// Portfolio: the published Project appears under the Owner's username; an unknown
	// username lists nothing. (Alice's other Project stays a draft, so it must not
	// appear — but here Alice has only "blog", now published.)
	if err := s.SetUsername(ctx, alice.ID, "alice-dev"); err != nil {
		t.Fatalf("set username: %v", err)
	}
	pf, err := s.ListPublishedProjectsByUsername(ctx, "alice-dev")
	if err != nil || len(pf) != 1 || pf[0].ID != p.ID {
		t.Fatalf("ListPublishedProjectsByUsername(alice-dev) = (%+v, %v), want the one published project", pf, err)
	}
	if empty, err := s.ListPublishedProjectsByUsername(ctx, "nobody"); err != nil || len(empty) != 0 {
		t.Errorf("unknown username should list nothing, got (%v, %v)", empty, err)
	}

	// FK cascade: deleting the Owner removes their Projects.
	if _, err := s.pool.Exec(ctx, "DELETE FROM owners WHERE id = $1", alice.ID); err != nil {
		t.Fatalf("delete owner: %v", err)
	}
	if _, err := s.GetOwnerProject(ctx, alice.ID, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("project should be gone after the owner is deleted, got %v", err)
	}
}

// TestUpdateProject verifies the edit semantics (#51): a pure rename keeps a published
// Project live, but editing the manifest returns it to draft (published cleared, commit
// dropped), all owner-scoped, with the per-owner unique-name guard.
func TestUpdateProject(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	alice, _ := s.UpsertOwnerByGitHubID(ctx, 100, "alice")
	bob, _ := s.UpsertOwnerByGitHubID(ctx, 200, "bob")

	m1 := []byte(`{"Services":[{"Name":"web","Role":"ui"}]}`)
	p, err := s.CreateProject(ctx, alice.ID, "site", m1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const sha = "0123456789abcdef0123456789abcdef01234567"
	if err := s.PublishProject(ctx, alice.ID, p.ID, sha, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// A pure rename (manifest re-encoded but semantically identical) stays published at
	// the same commit — exercises the jsonb IS DISTINCT FROM "no change" path.
	renamed, err := s.UpdateProject(ctx, alice.ID, p.ID, "site-renamed", m1)
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.Name != "site-renamed" || !renamed.Published || renamed.CommitSHA != sha {
		t.Errorf("a pure rename should stay published at the same commit: %+v", renamed)
	}

	// Editing the manifest returns the Project to draft and drops the pinned commit, so a
	// Guest can no longer play the now-stale build.
	m2 := []byte(`{"Services":[{"Name":"web","Role":"ui"},{"Name":"api","Role":"api","PathPrefix":"/api"}]}`)
	edited, err := s.UpdateProject(ctx, alice.ID, p.ID, "site-renamed", m2)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Published || edited.CommitSHA != "" {
		t.Errorf("editing the manifest must unpublish and drop the commit: %+v", edited)
	}
	if _, err := s.GetPublishedProject(ctx, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("an edited Project must no longer be playable, got %v", err)
	}

	// Owner-scoped: bob cannot update alice's Project (no row affected → not found); a
	// non-uuid id is not-found, not a 500.
	if _, err := s.UpdateProject(ctx, bob.ID, p.ID, "x", m1); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("cross-owner update = %v, want ErrProjectNotFound", err)
	}
	if _, err := s.UpdateProject(ctx, alice.ID, "not-a-uuid", "x", m1); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("non-uuid update = %v, want ErrProjectNotFound", err)
	}

	// Per-owner unique name: renaming p onto another of alice's Projects collides.
	if _, err := s.CreateProject(ctx, alice.ID, "other", m1); err != nil {
		t.Fatalf("create other: %v", err)
	}
	if _, err := s.UpdateProject(ctx, alice.ID, p.ID, "other", m1); !errors.Is(err, ErrProjectNameTaken) {
		t.Errorf("rename onto an existing name = %v, want ErrProjectNameTaken", err)
	}
}

// TestBuildLifecycle verifies the #49 build-state machine: StartBuild's conditional
// 'building' transition (and its duplicate-build guard), MarkBuildFailed leaving
// published untouched, PublishProject settling to 'published', and an edit resetting the
// build state back to 'idle'.
func TestBuildLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	alice, _ := s.UpsertOwnerByGitHubID(ctx, 100, "alice")
	bob, _ := s.UpsertOwnerByGitHubID(ctx, 200, "bob")

	m1 := []byte(`{"Services":[{"Name":"web","Role":"ui"}]}`)
	p, err := s.CreateProject(ctx, alice.ID, "app", m1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.BuildState != "idle" || p.BuildError != "" {
		t.Fatalf("a fresh Project should be idle with no error, got %+v", p)
	}

	// StartBuild transitions to 'building'; a second StartBuild while building is rejected
	// (the duplicate-publish guard), and bob can't start a build on alice's Project.
	building, err := s.StartBuild(ctx, alice.ID, p.ID)
	if err != nil || building.BuildState != "building" {
		t.Fatalf("StartBuild = (%+v, %v), want build_state=building", building, err)
	}
	if _, err := s.StartBuild(ctx, alice.ID, p.ID); !errors.Is(err, ErrBuildInProgress) {
		t.Errorf("second StartBuild = %v, want ErrBuildInProgress", err)
	}
	if _, err := s.StartBuild(ctx, bob.ID, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("cross-owner StartBuild = %v, want ErrProjectNotFound", err)
	}

	// A failed build records the error and state but never un-publishes (here it was
	// never published, so it stays an unplayable draft).
	if err := s.MarkBuildFailed(ctx, alice.ID, p.ID, "RUN npm ci failed"); err != nil {
		t.Fatalf("MarkBuildFailed: %v", err)
	}
	failed, _ := s.GetOwnerProject(ctx, alice.ID, p.ID)
	if failed.BuildState != "failed" || failed.BuildError != "RUN npm ci failed" || failed.Published {
		t.Errorf("after a failed build: %+v", failed)
	}
	if _, err := s.GetPublishedProject(ctx, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("a failed-build Project must not be playable, got %v", err)
	}

	// A successful publish settles to 'published' and clears the error; re-StartBuild is
	// allowed now that it is no longer 'building'.
	if _, err := s.StartBuild(ctx, alice.ID, p.ID); err != nil {
		t.Fatalf("re-StartBuild after failure: %v", err)
	}
	const sha = "0123456789abcdef0123456789abcdef01234567"
	if err := s.PublishProject(ctx, alice.ID, p.ID, sha, nil); err != nil {
		t.Fatalf("PublishProject: %v", err)
	}
	published, _ := s.GetOwnerProject(ctx, alice.ID, p.ID)
	if published.BuildState != "published" || published.BuildError != "" || !published.Published || published.CommitSHA != sha {
		t.Errorf("after publish: %+v", published)
	}

	// A failed RE-build of an already-published Project must NOT un-publish it: the old
	// build keeps playing at its commit while build_state reports the failure. This is the
	// load-bearing reason build_state is independent of published.
	if _, err := s.StartBuild(ctx, alice.ID, p.ID); err != nil {
		t.Fatalf("re-StartBuild on a published Project: %v", err)
	}
	if err := s.MarkBuildFailed(ctx, alice.ID, p.ID, "rebuild blew up"); err != nil {
		t.Fatalf("MarkBuildFailed on a published Project: %v", err)
	}
	rebuilt, _ := s.GetOwnerProject(ctx, alice.ID, p.ID)
	if !rebuilt.Published || rebuilt.BuildState != "failed" || rebuilt.CommitSHA != sha {
		t.Errorf("a failed re-build must keep the Project published at its old commit: %+v", rebuilt)
	}
	if playable, err := s.GetPublishedProject(ctx, p.ID); err != nil || playable.CommitSHA != sha {
		t.Errorf("a failed re-build must keep the Project playable at %q, got (%+v, %v)", sha, playable, err)
	}
	// Re-publish settles it back to 'published' so the edit step below starts from a clean
	// published state.
	if _, err := s.StartBuild(ctx, alice.ID, p.ID); err != nil {
		t.Fatalf("re-StartBuild before re-publish: %v", err)
	}
	if err := s.PublishProject(ctx, alice.ID, p.ID, sha, nil); err != nil {
		t.Fatalf("re-publish: %v", err)
	}

	// Editing the manifest returns the build state to 'idle' (the #51/#49 reconciliation).
	m2 := []byte(`{"Services":[{"Name":"web","Role":"ui"},{"Name":"api","Role":"api","PathPrefix":"/api"}]}`)
	edited, err := s.UpdateProject(ctx, alice.ID, p.ID, "app", m2)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.BuildState != "idle" || edited.BuildError != "" || edited.Published || edited.CommitSHA != "" {
		t.Errorf("editing the manifest must reset the build lifecycle to idle: %+v", edited)
	}
}

// TestReclaimStuckBuilds verifies the #49 recovery sweep: a build is reclaimed to 'failed'
// only once it is older than its OWN bound — (serviceCount+1)*perServiceTimeout + grace —
// so a live build is never touched, and a project with more services is given a
// proportionally longer window.
func TestReclaimStuckBuilds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	alice, _ := s.UpsertOwnerByGitHubID(ctx, 100, "alice")

	one := []byte(`{"Services":[{"Name":"web","Role":"ui"}]}`)
	two := []byte(`{"Services":[{"Name":"web","Role":"ui"},{"Name":"api","Role":"api","PathPrefix":"/api"}]}`)
	p1, _ := s.CreateProject(ctx, alice.ID, "one-svc", one)
	p2, _ := s.CreateProject(ctx, alice.ID, "two-svc", two)
	for _, id := range []string{p1.ID, p2.ID} {
		if _, err := s.StartBuild(ctx, alice.ID, id); err != nil {
			t.Fatalf("StartBuild(%s): %v", id, err)
		}
	}

	// A build that just started is NOT stale, so a generous window reclaims nothing.
	if n, err := s.ReclaimStuckBuilds(ctx, time.Hour, time.Hour); err != nil || n != 0 {
		t.Fatalf("ReclaimStuckBuilds(recent) = (%d, %v), want (0, nil)", n, err)
	}

	// Age both builds to 25 minutes ago. With perServiceTimeout=10m, grace=0 the windows are
	// p1: (1+1)*10m = 20m and p2: (2+1)*10m = 30m. So the 1-service build (25m > 20m) is
	// reclaimed but the 2-service build (25m < 30m) is still within its bound — the per-row
	// window in action.
	if _, err := s.pool.Exec(ctx, "UPDATE projects SET build_started_at = now() - interval '25 minutes' WHERE owner_id = $1", alice.ID); err != nil {
		t.Fatalf("age builds: %v", err)
	}
	n, err := s.ReclaimStuckBuilds(ctx, 10*time.Minute, 0)
	if err != nil || n != 1 {
		t.Fatalf("ReclaimStuckBuilds(per-row) = (%d, %v), want (1, nil) — only the 1-service build", n, err)
	}
	r1, _ := s.GetOwnerProject(ctx, alice.ID, p1.ID)
	if r1.BuildState != "failed" || r1.BuildError == "" || r1.Published {
		t.Errorf("the 1-service build past its bound = %+v, want failed with an error", r1)
	}
	if r2, _ := s.GetOwnerProject(ctx, alice.ID, p2.ID); r2.BuildState != "building" {
		t.Errorf("the 2-service build within its longer bound must stay 'building', got %q", r2.BuildState)
	}

	// Idempotent: the already-reclaimed build is not reclaimed again, and the 2-service one
	// is reclaimed once its (longer) window also passes.
	if n, _ := s.ReclaimStuckBuilds(ctx, 10*time.Minute, 0); n != 0 {
		t.Errorf("a settled build must not be reclaimed again, got %d", n)
	}
	if n, _ := s.ReclaimStuckBuilds(ctx, 0, 0); n != 1 {
		t.Errorf("the 2-service build must reclaim once its window passes, got %d", n)
	}
}

// TestPublishServiceCommits verifies the #50 per-service commit storage: PublishProject
// persists the headline commit_sha + the service_commits jsonb, they round-trip through
// the play read path, and an edit clears them (back to draft).
func TestPublishServiceCommits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	alice, _ := s.UpsertOwnerByGitHubID(ctx, 100, "alice")

	m := []byte(`{"Services":[{"Name":"web","Role":"ui"},{"Name":"api","Role":"api","PathPrefix":"/api"}]}`)
	p, err := s.CreateProject(ctx, alice.ID, "app", m)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.StartBuild(ctx, alice.ID, p.ID); err != nil {
		t.Fatalf("StartBuild: %v", err)
	}
	sc := []byte(`{"web":"websha","api":"apisha"}`)
	if err := s.PublishProject(ctx, alice.ID, p.ID, "websha", sc); err != nil {
		t.Fatalf("PublishProject: %v", err)
	}

	// The per-service commits round-trip through the play read path (jsonb reformats, so
	// assert semantically).
	pub, err := s.GetPublishedProject(ctx, p.ID)
	if err != nil || pub.CommitSHA != "websha" {
		t.Fatalf("GetPublishedProject = (%+v, %v), want commit_sha websha", pub, err)
	}
	var got map[string]string
	if err := json.Unmarshal(pub.ServiceCommits, &got); err != nil {
		t.Fatalf("service_commits not valid json: %v (%s)", err, pub.ServiceCommits)
	}
	if got["web"] != "websha" || got["api"] != "apisha" {
		t.Errorf("service_commits did not round-trip: %v", got)
	}

	// Editing the manifest clears service_commits (and the rest of the publish state).
	edited, err := s.UpdateProject(ctx, alice.ID, p.ID, "app", []byte(`{"Services":[{"Name":"web","Role":"ui"}]}`))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if len(edited.ServiceCommits) != 0 || edited.Published || edited.CommitSHA != "" {
		t.Errorf("editing the manifest must clear service_commits and the publish state: %+v", edited)
	}
}

// TestProjectSlug verifies the #51 per-Project slug: set/clear, per-owner uniqueness,
// owner scoping, and the public resolve-by-slug path (published-only).
func TestProjectSlug(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := startStore(t, ctx)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	alice, _ := s.UpsertOwnerByGitHubID(ctx, 100, "alice")
	bob, _ := s.UpsertOwnerByGitHubID(ctx, 200, "bob")
	if err := s.SetUsername(ctx, alice.ID, "alice-dev"); err != nil {
		t.Fatalf("set username: %v", err)
	}

	m := []byte(`{"Services":[{"Name":"web","Role":"ui"}]}`)
	p1, _ := s.CreateProject(ctx, alice.ID, "one", m)
	p2, _ := s.CreateProject(ctx, alice.ID, "two", m)

	if err := s.SetProjectSlug(ctx, alice.ID, p1.ID, "app"); err != nil {
		t.Fatalf("set slug: %v", err)
	}
	if got, _ := s.GetOwnerProject(ctx, alice.ID, p1.ID); got.Slug != "app" {
		t.Errorf("slug not persisted: %q", got.Slug)
	}
	// Per-owner unique: p2 can't reuse "app"; cross-owner set affects no row.
	if err := s.SetProjectSlug(ctx, alice.ID, p2.ID, "app"); !errors.Is(err, ErrProjectSlugTaken) {
		t.Errorf("duplicate slug = %v, want ErrProjectSlugTaken", err)
	}
	if err := s.SetProjectSlug(ctx, bob.ID, p1.ID, "x"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("cross-owner set slug = %v, want ErrProjectNotFound", err)
	}

	// Resolve-by-slug is published-only: a draft does not resolve.
	if _, err := s.GetPublishedProjectBySlug(ctx, "alice-dev", "app"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("draft resolve = %v, want ErrProjectNotFound", err)
	}
	if _, err := s.StartBuild(ctx, alice.ID, p1.ID); err != nil {
		t.Fatalf("StartBuild: %v", err)
	}
	if err := s.PublishProject(ctx, alice.ID, p1.ID, "sha", nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, err := s.GetPublishedProjectBySlug(ctx, "alice-dev", "app")
	if err != nil || got.ID != p1.ID {
		t.Fatalf("resolve published by slug = (%+v, %v), want p1", got, err)
	}
	if _, err := s.GetPublishedProjectBySlug(ctx, "alice-dev", "nope"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("unknown slug = %v, want ErrProjectNotFound", err)
	}

	// Clearing a slug is allowed, frees the name, and stops resolving; two NULL slugs do
	// not collide (Postgres treats NULLs as distinct under the unique constraint).
	if err := s.SetProjectSlug(ctx, alice.ID, p1.ID, ""); err != nil {
		t.Fatalf("clear slug: %v", err)
	}
	if _, err := s.GetPublishedProjectBySlug(ctx, "alice-dev", "app"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("a cleared slug must stop resolving, got %v", err)
	}
	if err := s.SetProjectSlug(ctx, alice.ID, p2.ID, ""); err != nil {
		t.Errorf("a second NULL slug must not collide: %v", err)
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
