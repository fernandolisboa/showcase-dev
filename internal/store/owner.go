package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned by repository getters when no row matches.
var ErrNotFound = errors.New("store: not found")

// Owner is a persisted authenticated user (ADR-0008). GitHubUserID is GitHub's
// stable numeric id (the identity key); GitHubLogin is the handle at last
// sign-in (display only).
type Owner struct {
	ID           int64
	GitHubUserID int64
	GitHubLogin  string
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
RETURNING id, github_user_id, github_login`
	var o Owner
	if err := s.pool.QueryRow(ctx, q, githubUserID, githubLogin).
		Scan(&o.ID, &o.GitHubUserID, &o.GitHubLogin); err != nil {
		return Owner{}, fmt.Errorf("upsert owner: %w", err)
	}
	return o, nil
}

// OwnerByID looks up an Owner by primary key, returning ErrNotFound if absent.
func (s *Store) OwnerByID(ctx context.Context, id int64) (Owner, error) {
	const q = `SELECT id, github_user_id, github_login FROM owners WHERE id = $1`
	var o Owner
	err := s.pool.QueryRow(ctx, q, id).Scan(&o.ID, &o.GitHubUserID, &o.GitHubLogin)
	if errors.Is(err, pgx.ErrNoRows) {
		return Owner{}, ErrNotFound
	}
	if err != nil {
		return Owner{}, fmt.Errorf("get owner: %w", err)
	}
	return o, nil
}
