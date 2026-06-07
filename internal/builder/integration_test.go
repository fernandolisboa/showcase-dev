//go:build integration

// Seam tests for #14: they run a real `docker build` of the embedded fixture and
// boot it through the Runner. Require Docker. Run with
//
//	go test -tags integration ./internal/builder/...
package builder

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/fixture"
	"github.com/fernandolisboa/showcase-dev/internal/proxy"
	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/runner"
)

// TestBuildFromSourceThenBoot exercises the whole #14 path: build the fixture
// image from its embedded Dockerfile, then boot a Session from the cached image
// and reach it — proving the Runner boots from a built image, not a prebuilt ref.
func TestBuildFromSourceThenBoot(t *testing.T) {
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("docker not available; skipping builder integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	ver, err := fixture.Version()
	if err != nil {
		t.Fatalf("fixture version: %v", err)
	}
	b := New(20, logger)
	source := NewBuildingSource(
		runner.StaticSource{P: runner.FixtureProject()}, b,
		func(projectID string, svc runcontract.Service) (BuildSpec, error) {
			return BuildSpec{
				ImageName:  "showcase-test/" + projectID + "-" + svc.Name,
				Version:    ver,
				Dockerfile: fixture.Dockerfile,
				Context:    fixture.Extract,
			}, nil
		},
		logger,
	)

	// Build from source (the "publish" warm) and capture the built tag for cleanup.
	p, err := source.Project(ctx, "fixture")
	if err != nil {
		t.Fatalf("build from source: %v", err)
	}
	tag := p.Images["web"]
	if !strings.HasPrefix(tag, "showcase-test/fixture-web:") {
		t.Fatalf("expected a built tag, got %q", tag)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", tag).Run() })

	// Boot a Session from the cached image and reach it.
	reg := proxy.NewRegistry("localhost")
	r := runner.NewCompose(source, runner.WithRuntime("runc"), runner.WithProxy(reg, ""))
	sessionID, err := proxy.NewSessionID()
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	t.Cleanup(func() { _ = r.Teardown(context.Background(), sessionID) })

	url, err := r.Provision(ctx, "fixture", sessionID)
	if err != nil {
		t.Fatalf("provision from built image: %v", err)
	}

	network := runner.NetworkName(sessionID)
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", network,
		"curlimages/curl:latest", "-sf", "--max-time", "15", url).CombinedOutput()
	if err != nil {
		t.Fatalf("built fixture not reachable at %s: %v\n%s", url, err, out)
	}
	if !strings.Contains(string(out), "Showcase fixture is running") {
		t.Errorf("unexpected body from built image: %q", out)
	}
}

// testWriter routes slog output to the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", p)
	return len(p), nil
}
