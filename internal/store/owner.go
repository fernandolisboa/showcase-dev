package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned by repository getters when no row matches.
var ErrNotFound = errors.New("store: not found")

// ErrUsernameTaken is returned when claiming a username already held by another
// Owner (the UNIQUE constraint, ADR-0005).
var ErrUsernameTaken = errors.New("store: username already taken")

// Owner is a persisted authenticated user (ADR-0008). GitHubUserID is GitHub's
// stable numeric id (the identity key); GitHubLogin is the handle at last
// sign-in (display only). Username is the public showcase handle (ADR-0005),
// empty until the Owner claims one — decoupled from GitHubLogin.
type Owner struct {
	ID           int64
	GitHubUserID int64
	GitHubLogin  string
	Username     string
}

// UpsertOwnerByGitHubID records the Owner on first sign-in and refreshes the
// cached GitHub login on every later sign-in, keyed on GitHub's stable numeric
// id so a handle rename updates the display name without creating a new Owner.
func (s *Store) UpsertOwnerByGitHubID(ctx context.Context, githubUserID int64, githubLogin string) (Owner, error) {
	const q = `
INSERT INTO owners (github_user_id, github_login)
VALUES ($1, $2)
ON CONFLICT (github_user_id)
DO UPDATE SET github_login = EXCLUDED.github_login, updated_at = now()
RETURNING id, github_user_id, github_login, COALESCE(username, '')`
	var o Owner
	if err := s.pool.QueryRow(ctx, q, githubUserID, githubLogin).
		Scan(&o.ID, &o.GitHubUserID, &o.GitHubLogin, &o.Username); err != nil {
		return Owner{}, fmt.Errorf("upsert owner: %w", err)
	}
	return o, nil
}

// OwnerByID looks up an Owner by primary key, returning ErrNotFound if absent.
func (s *Store) OwnerByID(ctx context.Context, id int64) (Owner, error) {
	const q = `SELECT id, github_user_id, github_login, COALESCE(username, '') FROM owners WHERE id = $1`
	var o Owner
	err := s.pool.QueryRow(ctx, q, id).Scan(&o.ID, &o.GitHubUserID, &o.GitHubLogin, &o.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		return Owner{}, ErrNotFound
	}
	if err != nil {
		return Owner{}, fmt.Errorf("get owner: %w", err)
	}
	return o, nil
}

// OwnerByUsername looks up an Owner by their public showcase username (the
// Portfolio lookup, ADR-0005). The caller passes the canonical (lowercased) form.
func (s *Store) OwnerByUsername(ctx context.Context, username string) (Owner, error) {
	const q = `SELECT id, github_user_id, github_login, COALESCE(username, '') FROM owners WHERE username = $1`
	var o Owner
	err := s.pool.QueryRow(ctx, q, username).Scan(&o.ID, &o.GitHubUserID, &o.GitHubLogin, &o.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		return Owner{}, ErrNotFound
	}
	if err != nil {
		return Owner{}, fmt.Errorf("get owner by username: %w", err)
	}
	return o, nil
}

// SetUsername claims (or changes) an Owner's public username. username must be
// the already-validated canonical (lowercased) form. A collision with another
// Owner's username surfaces as ErrUsernameTaken (the UNIQUE constraint).
func (s *Store) SetUsername(ctx context.Context, ownerID int64, username string) error {
	const q = `UPDATE owners SET username = $2, updated_at = now() WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, ownerID, username)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return ErrUsernameTaken
	}
	if err != nil {
		return fmt.Errorf("set username: %w", err)
	}
	return nil
}
