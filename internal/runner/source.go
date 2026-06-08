package runner

import (
	"context"
	"errors"
)

// ErrProjectNotFound is what a ProjectSource returns when no Project matches the id.
// FallbackSource keys its fallback on exactly this sentinel, so a database-backed
// source must translate its own not-found into this (not leak a store-layer error),
// and must return any other error (e.g. a corrupt stored manifest) unchanged so it
// is surfaced rather than silently masked by the fallback.
var ErrProjectNotFound = errors.New("runner: project not found")

// FallbackSource resolves from Primary, deferring to Secondary only when Primary
// reports ErrProjectNotFound. It lets the live Runner resolve Owner Projects from the
// database (Primary) while the hand-configured demo fixture (Secondary) keeps
// booting — so the demo stays green even with a database wired and nothing published
// yet. Any non-not-found error from Primary is returned as-is (no fallback), so a
// real fault never hides behind the fixture.
type FallbackSource struct {
	Primary   ProjectSource
	Secondary ProjectSource
}

// Project implements ProjectSource.
func (f FallbackSource) Project(ctx context.Context, projectID string) (Project, error) {
	p, err := f.Primary.Project(ctx, projectID)
	if errors.Is(err, ErrProjectNotFound) {
		return f.Secondary.Project(ctx, projectID)
	}
	return p, err
}
