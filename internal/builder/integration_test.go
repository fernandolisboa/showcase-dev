//go:build integration

// Seam tests for #14/#15: they run a real `docker build` of the embedded fixture
// and boot the multi-service Stack through the Runner. Require Docker. Run with
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

// TestBuildMultiServiceFromSourceThenBoot exercises the whole #14/#15/#16 path:
// build the UI and API images from their embedded Dockerfiles, boot a Session,
// reach BOTH services on the Session network (UI at "/", API under "/api"), and
// verify the platform stood up a fresh per-Session DB with scoped creds injected
// into the API as DATABASE_URL that actually authenticate. (Same-origin routing
// through Traefik is unit-tested in internal/proxy; the Traefik-in-the-loop e2e
// remains a noted follow-up.)
func TestBuildMultiServiceFromSourceThenBoot(t *testing.T) {
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("docker not available; skipping builder integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	b := New(20, logger)
	source := NewBuildingSource(
		runner.StaticSource{P: runner.Project{Manifest: fixture.Manifest(), Images: map[string]string{}}}, b,
		func(projectID string, svc runcontract.Service) (BuildSpec, error) {
			version, err := fixture.Version(svc.Name)
			if err != nil {
				return BuildSpec{}, err
			}
			return BuildSpec{
				ImageName:  "showcase-test/" + projectID + "-" + svc.Name,
				Version:    version,
				Dockerfile: fixture.Dockerfile,
				Context:    func() (string, func(), error) { return fixture.Extract(svc.Name) },
			}, nil
		},
		logger,
	)

	// Build both services from source ("publish" warm) and capture tags for cleanup.
	p, err := source.Project(ctx, "fixture")
	if err != nil {
		t.Fatalf("build from source: %v", err)
	}
	for _, svc := range []string{"web", "api"} {
		tag := p.Images[svc]
		if !strings.HasPrefix(tag, "showcase-test/fixture-"+svc+":") {
			t.Fatalf("service %s: expected a built tag, got %q", svc, tag)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", tag).Run() })
	}

	// Boot the multi-service Stack from the cached images.
	reg := proxy.NewRegistry("localhost")
	r := runner.NewCompose(source, runner.WithRuntime("runc"), runner.WithProxy(reg, ""))
	sessionID, err := proxy.NewSessionID()
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	t.Cleanup(func() { _ = r.Teardown(context.Background(), sessionID) })

	if _, err := r.Provision(ctx, "fixture", sessionID); err != nil {
		t.Fatalf("provision multi-service stack: %v", err)
	}

	route, ok := reg.Get(sessionID)
	if !ok || route.State != proxy.Live {
		t.Fatalf("session should be Live with both backends, got %+v ok=%v", route, ok)
	}
	if len(route.APIs) != 1 || route.APIs[0].PathPrefix != "/api" {
		t.Fatalf("expected one API backend at /api, got %+v", route.APIs)
	}

	network := runner.NetworkName(sessionID)
	// UI serves at "/".
	if out := curl(ctx, t, network, route.UI.URL); !strings.Contains(out, "Showcase UI is running") {
		t.Errorf("UI backend body = %q", out)
	}
	// API serves under "/api" (the proxy forwards the prefix unstripped).
	if out := curl(ctx, t, network, route.APIs[0].URL+"/api/"); !strings.Contains(out, "Showcase API is running") {
		t.Errorf("API backend body = %q", out)
	}

	// #16: the platform stood up a fresh per-Session DB and injected scoped creds
	// into the API as DATABASE_URL.
	project := runner.ProjectName(sessionID)
	dsn := strings.TrimSpace(dockerOut(ctx, t, "exec", project+"-api-1", "printenv", "DATABASE_URL"))
	if !strings.HasPrefix(dsn, "postgres://showcase:") || !strings.Contains(dsn, "@db:5432/showcase") {
		t.Fatalf("DATABASE_URL not injected as a scoped DSN: %q", dsn)
	}

	// The declared engine is used as-is — never substituted.
	if img := strings.TrimSpace(dockerOut(ctx, t, "inspect", project+"-db-1", "--format", "{{.Config.Image}}")); img != "postgres:17" {
		t.Errorf("db image = %q, want postgres:17 (declared engine used as-is)", img)
	}

	// The injected creds authenticate against the per-Session DB: a sidecar on the
	// Session network connects with the API's own DSN and runs a query. postgres:17
	// is already cached locally (the DB just booted from it), which matters because
	// the Session network is internal (no egress) and couldn't pull it.
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", network,
		"postgres:17", "psql", dsn, "-tAc", "SELECT 1").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("API's injected creds failed to connect to the per-Session DB: %v\n%s", err, out)
	}
	// (The DB tearing down with the Session — no leaked volume — is covered by the
	// Runner's own integration test, TestProvisionBootsReachableStackThenTeardownLeavesNothing.)
}

// dockerOut runs a docker command and returns stdout, failing the test on error
// with docker's stderr included for diagnostics.
func dockerOut(ctx context.Context, t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return string(out)
}

func curl(ctx context.Context, t *testing.T, network, url string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", network,
		"curlimages/curl:latest", "-sf", "--max-time", "15", url).CombinedOutput()
	if err != nil {
		t.Fatalf("not reachable at %s: %v\n%s", url, err, out)
	}
	return string(out)
}

// testWriter routes slog output to the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", p)
	return len(p), nil
}
