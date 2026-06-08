package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CreateLoginSession persists a web sign-in session. tokenHash is the hash of the
// opaque cookie token (never the token itself, ADR-0008) — the caller hashes
// before calling, so the raw token never reaches the store.
func (s *Store) CreateLoginSession(ctx context.Context, tokenHash string, ownerID int64, expiresAt time.Time) error {
	const q = `INSERT INTO login_sessions (token_hash, owner_id, expires_at) VALUES ($1, $2, $3)`
	if _, err := s.pool.Exec(ctx, q, tokenHash, ownerID, expiresAt); err != nil {
		return fmt.Errorf("create login session: %w", err)
	}
	return nil
}

// OwnerByLoginSession resolves the Owner behind a (hashed) session token, treating
// an expired row as absent (ErrNotFound) so a stale cookie is rejected even before
// the expired-row sweep removes it.
func (s *Store) OwnerByLoginSession(ctx context.Context, tokenHash string) (Owner, error) {
	const q = `
SELECT o.id, o.github_user_id, o.github_login, COALESCE(o.username, '')
FROM login_sessions s
JOIN owners o ON o.id = s.owner_id
WHERE s.token_hash = $1 AND s.expires_at > now()`
	var o Owner
	err := s.pool.QueryRow(ctx, q, tokenHash).Scan(&o.ID, &o.GitHubUserID, &o.GitHubLogin, &o.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		return Owner{}, ErrNotFound
	}
	if err != nil {
		return Owner{}, fmt.Errorf("owner by login session: %w", err)
	}
	return o, nil
}

// DeleteLoginSession removes one session (logout). Deleting a missing row is not
// an error — logout is idempotent.
func (s *Store) DeleteLoginSession(ctx context.Context, tokenHash string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM login_sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("delete login session: %w", err)
	}
	return nil
}

// DeleteExpiredLoginSessions prunes sessions past their expiry and reports how
// many were removed. Called periodically so the table doesn't grow unbounded.
func (s *Store) DeleteExpiredLoginSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM login_sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("prune expired login sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
