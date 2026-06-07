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
	return f.tag, nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBuildingSourceSwapsInBuiltTags(t *testing.T) {
	base := runner.StaticSource{P: runner.FixtureProject()} // service "web", prebuilt whoami
	fb := &fakeBuilder{tag: "showcase/fixture-web:abc123"}
	spec := func(projectID string, svc runcontract.Service) (BuildSpec, error) {
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

func TestBuildingSourcePropagatesBuildFailure(t *testing.T) {
	base := runner.StaticSource{P: runner.FixtureProject()}
	fb := &fakeBuilder{err: errors.New("docker build: boom")}
	spec := func(projectID string, svc runcontract.Service) (BuildSpec, error) {
		return BuildSpec{ImageName: "showcase/" + svc.Name}, nil
	}
	src := NewBuildingSource(base, fb, spec, discardLogger())

	if _, err := src.Project(context.Background(), "fixture"); err == nil {
		t.Fatal("expected the build failure to propagate")
	}
}
