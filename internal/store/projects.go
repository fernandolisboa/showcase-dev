package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// uuidRE matches the canonical 8-4-4-4-12 hex id the projects.id column stores. The
// id reaches the getters from untrusted input (a URL path segment, the /api/play
// body, the default fixture id "fixture"); an id that isn't a uuid can never match a
// row, and pgx cannot even encode it for a uuid param, so the getters short-circuit
// it to ErrProjectNotFound rather than surfacing an encode error as a 500 — and so a
// FallbackSource still sees ErrProjectNotFound and defers to the demo fixture.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ErrProjectNotFound is returned by the Project getters when no row matches. For
// the owner-scoped getter that also covers a Project owned by someone else, so a
// cross-owner read is indistinguishable from a missing one (no existence oracle).
var ErrProjectNotFound = errors.New("store: project not found")

// ErrProjectNameTaken is returned when an Owner already has a Project with the
// requested name (the per-owner UNIQUE constraint, mapped from 23505).
var ErrProjectNameTaken = errors.New("store: project name already taken")

// ErrBuildInProgress is returned by StartBuild when the Project already has a build in
// flight (build_state = 'building'), so a caller can reject a duplicate publish (409)
// rather than starting a second concurrent build of the same Project (#49).
var ErrBuildInProgress = errors.New("store: a build is already in progress")

// ErrProjectSlugTaken is returned when an Owner already has a Project with the requested
// slug (the per-owner UNIQUE on slug, #51), mapped from 23505 on SetProjectSlug.
var ErrProjectSlugTaken = errors.New("store: project slug already taken")

// Project is a persisted Owner app (#19). Manifest is the Owner's run contract
// (ADR-0003) stored verbatim as jsonb; this layer keeps it opaque ([]byte) so the
// store stays domain-agnostic — the runner adapter unmarshals it into a
// runcontract.Manifest. Published gates whether a Guest can play it (a Project is
// published only after a successful build, in a later slice).
type Project struct {
	ID        string
	OwnerID   int64
	Name      string
	Manifest  []byte
	Published bool
	// CommitSHA is the source commit the Project was last published at (empty until
	// the first publish). It pins the played version — the build path uses it as the
	// cache key so play is a cache hit on the published image (#19).
	CommitSHA string
	// BuildState is the Owner-facing build lifecycle (#49): idle | building | failed |
	// published. It is independent of Published (the play gate): a re-build of a
	// published Project is published=true + build_state='building', and a failed
	// re-build stays published=true + build_state='failed' (a failed build never
	// un-publishes). BuildError holds a bounded log tail while build_state='failed',
	// empty otherwise.
	BuildState string
	BuildError string
	// ServiceCommits is the raw jsonb {service_name: commit_sha} pinning each service's
	// source commit for a multi-repo Project (#50), or nil for a pre-#50 single-repo
	// Project (whose services fall back to CommitSHA). Kept opaque ([]byte) like Manifest
	// so the store stays domain-agnostic; the play adapter unmarshals it.
	ServiceCommits []byte
	// Slug is the optional human-friendly per-Owner URL segment for showcase.dev/{username}
	// /{slug} (#51); empty if the Owner has not set one (the UUID still plays).
	Slug      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateProject inserts a new Project for an Owner. manifest must already be a
// validated, canonical JSON encoding of the run contract (the project layer maps
// the Owner form to a runcontract.Manifest and Validates it before calling). A name
// already used by this Owner surfaces as ErrProjectNameTaken (the per-owner UNIQUE).
func (s *Store) CreateProject(ctx context.Context, ownerID int64, name string, manifest []byte) (Project, error) {
	const q = `
INSERT INTO projects (owner_id, name, manifest)
VALUES ($1, $2, $3)
RETURNING id, owner_id, name, manifest, published, COALESCE(commit_sha, '') AS commit_sha, build_state, COALESCE(build_error, '') AS build_error, service_commits, COALESCE(slug, '') AS slug, created_at, updated_at`
	var p Project
	err := s.pool.QueryRow(ctx, q, ownerID, name, manifest).
		Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CommitSHA, &p.BuildState, &p.BuildError, &p.ServiceCommits, &p.Slug, &p.CreatedAt, &p.UpdatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return Project{}, ErrProjectNameTaken
	}
	if err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

// GetOwnerProject looks up one of an Owner's Projects by id, scoped to the Owner so
// one Owner can never read another's Project — a cross-owner id returns
// ErrProjectNotFound, identical to a genuinely missing one.
func (s *Store) GetOwnerProject(ctx context.Context, ownerID int64, id string) (Project, error) {
	if !uuidRE.MatchString(id) {
		return Project{}, ErrProjectNotFound
	}
	const q = `
SELECT id, owner_id, name, manifest, published, COALESCE(commit_sha, '') AS commit_sha, build_state, COALESCE(build_error, '') AS build_error, service_commits, COALESCE(slug, '') AS slug, created_at, updated_at
FROM projects WHERE id = $1 AND owner_id = $2`
	var p Project
	err := s.pool.QueryRow(ctx, q, id, ownerID).
		Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CommitSHA, &p.BuildState, &p.BuildError, &p.ServiceCommits, &p.Slug, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("get owner project: %w", err)
	}
	return p, nil
}

// GetPublishedProject looks up a published Project by id for the play path: a Guest
// plays by id, and only a published Project (one that built successfully — the flag
// is flipped at publish, a later slice) may boot. An unpublished or missing id — and
// any id that isn't a uuid — is ErrProjectNotFound, so a draft is never playable and
// a FallbackSource cleanly defers to the demo fixture.
func (s *Store) GetPublishedProject(ctx context.Context, id string) (Project, error) {
	if !uuidRE.MatchString(id) {
		return Project{}, ErrProjectNotFound
	}
	const q = `
SELECT id, owner_id, name, manifest, published, COALESCE(commit_sha, '') AS commit_sha, build_state, COALESCE(build_error, '') AS build_error, service_commits, COALESCE(slug, '') AS slug, created_at, updated_at
FROM projects WHERE id = $1 AND published`
	var p Project
	err := s.pool.QueryRow(ctx, q, id).
		Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CommitSHA, &p.BuildState, &p.BuildError, &p.ServiceCommits, &p.Slug, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("get published project: %w", err)
	}
	return p, nil
}

// GetPublishedProjectBySlug resolves a published Project by the Owner's username and the
// Project's slug — the public play path for showcase.dev/{username}/{slug} (#51). Only a
// published Project resolves; an unknown username/slug, an unpublished one, or an empty
// slug is ErrProjectNotFound (so a Guest can't reach a draft and the caller can 404).
func (s *Store) GetPublishedProjectBySlug(ctx context.Context, username, slug string) (Project, error) {
	if slug == "" {
		return Project{}, ErrProjectNotFound
	}
	const q = `
SELECT p.id, p.owner_id, p.name, p.manifest, p.published, COALESCE(p.commit_sha, '') AS commit_sha, p.build_state, COALESCE(p.build_error, '') AS build_error, p.service_commits, COALESCE(p.slug, '') AS slug, p.created_at, p.updated_at
FROM projects p JOIN owners o ON o.id = p.owner_id
WHERE o.username = $1 AND p.slug = $2 AND p.published`
	var p Project
	err := s.pool.QueryRow(ctx, q, username, slug).
		Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CommitSHA, &p.BuildState, &p.BuildError, &p.ServiceCommits, &p.Slug, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("get published project by slug: %w", err)
	}
	return p, nil
}

// SetProjectSlug sets — or, with an empty slug, clears — one of an Owner's Projects' URL
// slug (#51); the slug is editable. It does NOT touch publish state (a slug is a URL alias,
// not build config). Owner-scoped: a cross-owner or missing id affects no row and returns
// ErrProjectNotFound; a slug another of the Owner's Projects already uses is
// ErrProjectSlugTaken (the per-owner UNIQUE). slug must already be canonical (the project
// layer validates it); empty clears it back to NULL.
func (s *Store) SetProjectSlug(ctx context.Context, ownerID int64, id, slug string) error {
	if !uuidRE.MatchString(id) {
		return ErrProjectNotFound
	}
	const q = `UPDATE projects SET slug = NULLIF($3, ''), updated_at = now() WHERE id = $1 AND owner_id = $2`
	tag, err := s.pool.Exec(ctx, q, id, ownerID, slug)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation on (owner_id, slug)
		return ErrProjectSlugTaken
	}
	if err != nil {
		return fmt.Errorf("set project slug: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// ListProjectsByOwner returns an Owner's Projects, newest first.
func (s *Store) ListProjectsByOwner(ctx context.Context, ownerID int64) ([]Project, error) {
	const q = `
SELECT id, owner_id, name, manifest, published, COALESCE(commit_sha, '') AS commit_sha, build_state, COALESCE(build_error, '') AS build_error, service_commits, COALESCE(slug, '') AS slug, created_at, updated_at
FROM projects WHERE owner_id = $1 ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return scanProjects(rows)
}

// ListPublishedProjectsByUsername returns the PUBLISHED Projects of the Owner with
// the given showcase username, newest first — the public Portfolio listing
// (ADR-0005). An unknown/unclaimed username (or one with no published Projects)
// yields an empty slice; the caller uses OwnerByUsername to tell "no such Owner" from
// "Owner with nothing published".
func (s *Store) ListPublishedProjectsByUsername(ctx context.Context, username string) ([]Project, error) {
	const q = `
SELECT p.id, p.owner_id, p.name, p.manifest, p.published, COALESCE(p.commit_sha, '') AS commit_sha, p.build_state, COALESCE(p.build_error, '') AS build_error, p.service_commits, COALESCE(p.slug, '') AS slug, p.created_at, p.updated_at
FROM projects p JOIN owners o ON o.id = p.owner_id
WHERE o.username = $1 AND p.published
ORDER BY p.created_at DESC`
	rows, err := s.pool.Query(ctx, q, username)
	if err != nil {
		return nil, fmt.Errorf("list published projects: %w", err)
	}
	return scanProjects(rows)
}

// scanProjects drains rows into Projects (shared by the list queries).
func scanProjects(rows pgx.Rows) ([]Project, error) {
	defer rows.Close()
	var ps []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CommitSHA, &p.BuildState, &p.BuildError, &p.ServiceCommits, &p.Slug, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan projects: %w", err)
	}
	return ps, nil
}

// PublishProject marks one of an Owner's Projects published and records the commit it
// was built at, atomically (so a Guest never sees published-without-a-pinned-commit).
// It also settles the build lifecycle to 'published' and clears any prior build error
// (#49), and records the per-service commits for a multi-repo Project (#50; serviceCommits
// is the marshalled {service: commit} jsonb, or nil for a single-repo Project that pins
// every service to commitSHA). Owner-scoped: a cross-owner or missing id affects no row and
// returns ErrProjectNotFound. The caller publishes only after a successful build, so a
// published Project is always buildable at the recorded commits (#19).
func (s *Store) PublishProject(ctx context.Context, ownerID int64, id, commitSHA string, serviceCommits []byte) error {
	if !uuidRE.MatchString(id) {
		return ErrProjectNotFound
	}
	const q = `
UPDATE projects SET published = true, build_state = 'published', build_error = NULL, commit_sha = $3, service_commits = $4, updated_at = now()
WHERE id = $1 AND owner_id = $2`
	tag, err := s.pool.Exec(ctx, q, id, ownerID, commitSHA, serviceCommits)
	if err != nil {
		return fmt.Errorf("publish project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// UpdateProject replaces the name and manifest of one of an Owner's Projects. When the
// manifest actually changes it returns the Project to draft — published is cleared, the
// pinned commit_sha dropped, and the build lifecycle reset to 'idle' with any build
// error cleared — so a stale build can never outlive an edited run contract (#51/#49:
// the Owner re-publishes to rebuild). A pure rename (manifest unchanged) keeps the
// Project published at its commit and its build state intact. Owner-scoped: a cross-owner or missing id
// affects no row and returns ErrProjectNotFound; a name another of the Owner's Projects
// already uses surfaces as ErrProjectNameTaken (the per-owner UNIQUE). manifest must
// already be a validated, canonical JSON encoding (the project layer maps the Owner form
// and Validates it before calling).
func (s *Store) UpdateProject(ctx context.Context, ownerID int64, id, name string, manifest []byte) (Project, error) {
	if !uuidRE.MatchString(id) {
		return Project{}, ErrProjectNotFound
	}
	// The published/commit_sha reset is conditional on the manifest actually changing,
	// evaluated in one statement against the OLD row (an UPDATE's SET expressions see the
	// pre-update values) so a reader never observes a half-applied edit. jsonb IS DISTINCT
	// FROM compares semantically, so re-encoding the same content counts as no change.
	const q = `
UPDATE projects
SET name = $3,
    manifest = $4,
    published = CASE WHEN manifest IS DISTINCT FROM $4 THEN false ELSE published END,
    commit_sha = CASE WHEN manifest IS DISTINCT FROM $4 THEN NULL ELSE commit_sha END,
    service_commits = CASE WHEN manifest IS DISTINCT FROM $4 THEN NULL ELSE service_commits END,
    build_state = CASE WHEN manifest IS DISTINCT FROM $4 THEN 'idle' ELSE build_state END,
    build_error = CASE WHEN manifest IS DISTINCT FROM $4 THEN NULL ELSE build_error END,
    updated_at = now()
WHERE id = $1 AND owner_id = $2
RETURNING id, owner_id, name, manifest, published, COALESCE(commit_sha, '') AS commit_sha, build_state, COALESCE(build_error, '') AS build_error, service_commits, COALESCE(slug, '') AS slug, created_at, updated_at`
	var p Project
	err := s.pool.QueryRow(ctx, q, id, ownerID, name, manifest).
		Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CommitSHA, &p.BuildState, &p.BuildError, &p.ServiceCommits, &p.Slug, &p.CreatedAt, &p.UpdatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return Project{}, ErrProjectNameTaken
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("update project: %w", err)
	}
	return p, nil
}

// ReclaimStuckBuilds settles Projects stranded in build_state='building' back to 'failed'
// so the Owner can retry — a build interrupted by a crash or a process restart mid-build,
// whose in-process goroutine no longer exists to settle it (#49). Each Project's staleness
// window is computed from its OWN service count — (serviceCount+1)*perServiceTimeout, the
// same bound publish enforces — plus grace, so a live build is never reclaimed no matter
// how many services it declares, while a stranded one is recovered shortly after its
// bound. NOT owner-scoped: it sweeps every Owner's Projects. published is left untouched
// (a stranded re-build of a published Project keeps the old build playable). Returns how
// many rows it reclaimed.
func (s *Store) ReclaimStuckBuilds(ctx context.Context, perServiceTimeout, grace time.Duration) (int64, error) {
	// jsonb_array_length(manifest->'Services') is each Project's declared service count
	// (the manifest is the canonical encoding of runcontract.Manifest, whose Services field
	// serializes as "Services"); COALESCE to 1 guards the impossible null case. A 'building'
	// row always has build_started_at set by StartBuild.
	const q = `
UPDATE projects
SET build_state = 'failed',
    build_error = 'the build did not finish (the server restarted or the build was interrupted) — please retry',
    updated_at  = now()
WHERE build_state = 'building'
  AND build_started_at IS NOT NULL
  AND build_started_at < now() - make_interval(secs =>
        (COALESCE(jsonb_array_length(manifest -> 'Services'), 1) + 1) * $1 + $2)`
	tag, err := s.pool.Exec(ctx, q, perServiceTimeout.Seconds(), grace.Seconds())
	if err != nil {
		return 0, fmt.Errorf("reclaim stuck builds: %w", err)
	}
	return tag.RowsAffected(), nil
}

// StartBuild transitions one of an Owner's Projects into the 'building' state and stamps
// build_started_at, atomically and only if a build is not already in flight — the guard
// that lets a caller reject a duplicate publish instead of starting a second concurrent
// build (#49). It returns the updated Project on success, ErrBuildInProgress if the
// Project is already 'building', and ErrProjectNotFound if it is not the Owner's (or
// absent). Published is left untouched: a re-build of a published Project keeps it
// playable at the old commit until a new build succeeds.
func (s *Store) StartBuild(ctx context.Context, ownerID int64, id string) (Project, error) {
	if !uuidRE.MatchString(id) {
		return Project{}, ErrProjectNotFound
	}
	const q = `
UPDATE projects
SET build_state = 'building', build_error = NULL, build_started_at = now(), updated_at = now()
WHERE id = $1 AND owner_id = $2 AND build_state <> 'building'
RETURNING id, owner_id, name, manifest, published, COALESCE(commit_sha, '') AS commit_sha, build_state, COALESCE(build_error, '') AS build_error, service_commits, COALESCE(slug, '') AS slug, created_at, updated_at`
	var p Project
	err := s.pool.QueryRow(ctx, q, id, ownerID).
		Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CommitSHA, &p.BuildState, &p.BuildError, &p.ServiceCommits, &p.Slug, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row updated: either the Project isn't the Owner's (or is gone), or it is
		// already 'building'. Disambiguate so the caller can return 404 vs 409.
		if _, gerr := s.GetOwnerProject(ctx, ownerID, id); gerr != nil {
			return Project{}, gerr // ErrProjectNotFound (or a real error)
		}
		return Project{}, ErrBuildInProgress
	}
	if err != nil {
		return Project{}, fmt.Errorf("start build: %w", err)
	}
	return p, nil
}

// MarkBuildFailed records that the current build of one of an Owner's Projects failed,
// settling build_state to 'failed' and storing a bounded error tail (#49). Published is
// deliberately left untouched — a failed build never un-publishes, so a Project that was
// already published stays playable at its previous commit. Owner-scoped: a cross-owner
// or missing id affects no row and returns ErrProjectNotFound.
func (s *Store) MarkBuildFailed(ctx context.Context, ownerID int64, id, buildError string) error {
	if !uuidRE.MatchString(id) {
		return ErrProjectNotFound
	}
	const q = `
UPDATE projects SET build_state = 'failed', build_error = $3, updated_at = now()
WHERE id = $1 AND owner_id = $2`
	tag, err := s.pool.Exec(ctx, q, id, ownerID, buildError)
	if err != nil {
		return fmt.Errorf("mark build failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}
