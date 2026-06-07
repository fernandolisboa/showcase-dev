package builder

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/runner"
)

// imageBuilder is the slice of *Builder that BuildingSource needs (so tests can
// substitute a fake).
type imageBuilder interface {
	Build(ctx context.Context, spec BuildSpec) (string, error)
}

// SpecFunc resolves a Project's service to the spec for building its image. For
// the fixture it points at the embedded build context; for real Owner Projects
// (#19) it will clone the repo at the configured commit.
type SpecFunc func(projectID string, svc runcontract.Service) (BuildSpec, error)

// BuildingSource is a runner.ProjectSource that builds each service's image from
// source (caching via the Builder) and returns the Project with the built image
// tags — replacing the prebuilt refs the base source carries (ADR-0003).
type BuildingSource struct {
	base    runner.ProjectSource
	builder imageBuilder
	spec    SpecFunc
	logger  *slog.Logger
}

// NewBuildingSource wraps base, building each service's image via builder using
// spec to locate the source.
func NewBuildingSource(base runner.ProjectSource, builder imageBuilder, spec SpecFunc, logger *slog.Logger) *BuildingSource {
	return &BuildingSource{base: base, builder: builder, spec: spec, logger: logger}
}

// Project resolves the base Project, then builds every service's image and swaps
// in the built tags. A build failure is surfaced to the Owner (logged) and
// returned, so the caller (a play, or the startup warm) handles it — a failed
// build at play time becomes the "failed to start" page (#13).
func (s *BuildingSource) Project(ctx context.Context, projectID string) (runner.Project, error) {
	p, err := s.base.Project(ctx, projectID)
	if err != nil {
		return runner.Project{}, err
	}

	// Build every declared service. A Seed reuses a declared service's image
	// (manifest validation requires seed.service to be a declared service), so
	// building all services also supplies the seed's image — no special case.
	images := make(map[string]string, len(p.Manifest.Services))
	for _, svc := range p.Manifest.Services {
		spec, err := s.spec(projectID, svc)
		if err != nil {
			return runner.Project{}, fmt.Errorf("resolve build spec for %s/%s: %w", projectID, svc.Name, err)
		}
		tag, err := s.builder.Build(ctx, spec)
		if err != nil {
			// Owner-facing: the error carries docker's build output (ADR-0006).
			s.logger.Error("project build failed", "project", projectID, "service", svc.Name, "err", err)
			return runner.Project{}, fmt.Errorf("build %s/%s: %w", projectID, svc.Name, err)
		}
		images[svc.Name] = tag
	}
	p.Images = images
	return p, nil
}
