package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/runner"
	"github.com/fernandolisboa/showcase-dev/internal/store"
)

// PublishedStore is the persistence the play-path Source needs: resolving a
// published Project by id (a subset of *store.Store), kept as an interface so the
// Source is testable without a database.
type PublishedStore interface {
	GetPublishedProject(ctx context.Context, id string) (store.Project, error)
}

// Source is a runner.ProjectSource backed by the control-plane database: it resolves
// a published Project's stored manifest into a runner.Project for the Runner to boot.
// Images are left empty here — the builder.BuildingSource that wraps this fills them
// from source at play time (build-at-publish, a later slice). A missing or
// unpublished id becomes runner.ErrProjectNotFound, so a runner.FallbackSource can
// defer to the demo fixture; any other error (notably a stored manifest that won't
// decode) is surfaced, never masked as not-found.
type Source struct {
	store PublishedStore
}

// NewSource wires the play-path Source to a store.
func NewSource(s PublishedStore) *Source { return &Source{store: s} }

// Project implements runner.ProjectSource.
func (s *Source) Project(ctx context.Context, projectID string) (runner.Project, error) {
	p, err := s.store.GetPublishedProject(ctx, projectID)
	if errors.Is(err, store.ErrProjectNotFound) {
		return runner.Project{}, runner.ErrProjectNotFound
	}
	if err != nil {
		return runner.Project{}, err
	}
	var m runcontract.Manifest
	if err := json.Unmarshal(p.Manifest, &m); err != nil {
		// A stored manifest that won't parse is a real fault — surface it. Mapping it
		// to not-found would silently boot the fallback fixture in its place.
		return runner.Project{}, fmt.Errorf("decode manifest for project %s: %w", projectID, err)
	}
	// Pin the build to the PUBLISHED commit so the play build is a cache hit on the
	// image built at publish — a Guest gets exactly the published version and never
	// waits on a rebuild from a moved HEAD (#19). Images stay empty; the BuildingSource
	// that wraps this fills them (from cache on a hit).
	return runner.Project{Manifest: m, Commit: p.CommitSHA, Images: map[string]string{}}, nil
}
