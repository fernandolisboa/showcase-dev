package builder

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/runner"
)

// fakeBuilder is an imageBuilder that returns a canned tag per image name, or an
// error, and records the specs it received.
type fakeBuilder struct {
	tag   string
	err   error
	specs []BuildSpec
}

func (f *fakeBuilder) Build(_ context.Context, spec BuildSpec) (string, error) {
	f.specs = append(f.specs, spec)
	if f.err != nil {
		return "", f.err
	}
	if f.tag != "" {
		return f.tag, nil
	}
	return spec.ImageName + ":built", nil // distinct tag per service
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBuildingSourceSwapsInBuiltTags(t *testing.T) {
	base := runner.StaticSource{P: runner.FixtureProject()} // service "web", prebuilt whoami
	fb := &fakeBuilder{tag: "showcase/fixture-web:abc123"}
	spec := func(_ context.Context, projectID string, svc runcontract.Service, _ string) (BuildSpec, error) {
		return BuildSpec{ImageName: "showcase/" + projectID + "-" + svc.Name, Version: "v1"}, nil
	}
	src := NewBuildingSource(base, fb, spec, discardLogger())

	p, err := src.Project(context.Background(), "fixture")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if got := p.Images["web"]; got != "showcase/fixture-web:abc123" {
		t.Errorf("Images[web] = %q, want the built tag (not the prebuilt ref)", got)
	}
	if len(fb.specs) != 1 || fb.specs[0].ImageName != "showcase/fixture-web" {
		t.Errorf("unexpected build specs: %+v", fb.specs)
	}
}

func TestBuildingSourcePassesPinnedCommitToSpec(t *testing.T) {
	// The pinned Commit (the published commit on the play path, or the once-resolved
	// HEAD at publish) is passed straight through to every service's SpecFunc — so the
	// build is the published image (a cache hit) — and preserved on the way out.
	fb := &fakeBuilder{tag: "img"}
	base := runner.StaticSource{P: runner.Project{Manifest: runner.FixtureProject().Manifest, Commit: "pinnedsha"}}
	var sawCommit string
	spec := func(_ context.Context, projectID string, svc runcontract.Service, commit string) (BuildSpec, error) {
		sawCommit = commit
		return BuildSpec{ImageName: "showcase/" + projectID + "-" + svc.Name, Version: commit}, nil
	}
	p, err := NewBuildingSource(base, fb, spec, discardLogger()).Project(context.Background(), "p1")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if sawCommit != "pinnedsha" {
		t.Errorf("the pinned commit must reach the SpecFunc, got %q", sawCommit)
	}
	if p.Commit != "pinnedsha" {
		t.Errorf("the pinned commit must be preserved, got %q", p.Commit)
	}
}

func TestBuildingSourceBuildsEveryServiceSoSeedIsCovered(t *testing.T) {
	// A Seed reuses a declared service's image (manifest validation guarantees
	// seed.service is declared). Building every declared service must therefore
	// also supply the seed's image — runcontract.Compile needs it.
	base := runner.StaticSource{P: runner.Project{
		Manifest: runcontract.Manifest{
			Services: []runcontract.Service{{Name: "web"}, {Name: "api"}},
			Seed:     &runcontract.Seed{Service: "api", Command: []string{"true"}},
		},
		Images: map[string]string{},
	}}
	fb := &fakeBuilder{}
	spec := func(_ context.Context, _ string, svc runcontract.Service, _ string) (BuildSpec, error) {
		return BuildSpec{ImageName: "showcase/" + svc.Name, Version: "v1"}, nil
	}
	src := NewBuildingSource(base, fb, spec, discardLogger())

	p, err := src.Project(context.Background(), "fixture")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if p.Images["web"] == "" || p.Images["api"] == "" {
		t.Errorf("every declared service must be built; got %v", p.Images)
	}
	// The seed reuses "api", which is built — so Compile would find its image.
	if p.Images[p.Manifest.Seed.Service] == "" {
		t.Error("seed service image must be present (it reuses a declared service)")
	}
}

func TestBuildingSourcePropagatesBuildFailure(t *testing.T) {
	base := runner.StaticSource{P: runner.FixtureProject()}
	fb := &fakeBuilder{err: errors.New("docker build: boom")}
	spec := func(_ context.Context, _ string, svc runcontract.Service, _ string) (BuildSpec, error) {
		return BuildSpec{ImageName: "showcase/" + svc.Name}, nil
	}
	src := NewBuildingSource(base, fb, spec, discardLogger())

	if _, err := src.Project(context.Background(), "fixture"); err == nil {
		t.Fatal("expected the build failure to propagate")
	}
}
