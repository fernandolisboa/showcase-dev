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
RETURNING id, owner_id, name, manifest, published, created_at, updated_at`
	var p Project
	err := s.pool.QueryRow(ctx, q, ownerID, name, manifest).
		Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CreatedAt, &p.UpdatedAt)
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
SELECT id, owner_id, name, manifest, published, created_at, updated_at
FROM projects WHERE id = $1 AND owner_id = $2`
	var p Project
	err := s.pool.QueryRow(ctx, q, id, ownerID).
		Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CreatedAt, &p.UpdatedAt)
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
SELECT id, owner_id, name, manifest, published, created_at, updated_at
FROM projects WHERE id = $1 AND published`
	var p Project
	err := s.pool.QueryRow(ctx, q, id).
		Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("get published project: %w", err)
	}
	return p, nil
}

// ListProjectsByOwner returns an Owner's Projects, newest first.
func (s *Store) ListProjectsByOwner(ctx context.Context, ownerID int64) ([]Project, error) {
	const q = `
SELECT id, owner_id, name, manifest, published, created_at, updated_at
FROM projects WHERE owner_id = $1 ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var ps []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Manifest, &p.Published, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return ps, nil
}
