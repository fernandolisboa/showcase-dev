//go:build integration

// Seam tests for #14/#15/#16/#17: they build the embedded fixture from source in
// the sandbox (ADR-0010) and boot the multi-service Stack through the Runner —
// proving real, multi-stage builds still work end to end through the sandbox.
// Require Docker. Run with
//
//	go test -tags integration ./internal/builder/...
package builder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/fixture"
	"github.com/fernandolisboa/showcase-dev/internal/proxy"
	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/runner"
)

// TestBuildMultiServiceFromSourceThenBoot exercises the whole #14/#15/#16/#17
// path: build the UI and API images from their embedded Dockerfiles, boot a
// Session, reach BOTH services on the Session network (UI at "/", API under
// "/api"), and verify the platform stood up a fresh per-Session DB whose scoped
// creds the API authenticates with — serving back the representative rows a seed
// one-shot wrote before the Demo was ever served. (Same-origin routing through
// Traefik is unit-tested in internal/proxy; the Traefik-in-the-loop e2e remains a
// noted follow-up.)
func TestBuildMultiServiceFromSourceThenBoot(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	built := buildFixture(ctx, t)

	// Boot the multi-service Stack from the cached images.
	reg := proxy.NewRegistry("localhost")
	r := runner.NewCompose(runner.StaticSource{P: built}, runner.WithRuntime("runc"), runner.WithProxy(reg, ""))
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

	// #17: reaching Live means the seed one-shot completed and the API healthcheck
	// passed — so the API serves the representative rows the seed migrated into the
	// fresh per-Session DB before the Demo was served. This also proves the injected
	// creds authenticate (the API read the rows with its own DATABASE_URL).
	items := curl(ctx, t, network, route.APIs[0].URL+"/api/items")
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(items, want) {
			t.Errorf("seeded item %q missing from /api/items: %q", want, items)
		}
	}

	// #16: the DB is the declared engine, used as-is — never substituted.
	project := runner.ProjectName(sessionID)
	if img := strings.TrimSpace(dockerOut(ctx, t, "inspect", project+"-db-1", "--format", "{{.Config.Image}}")); img != "postgres:17" {
		t.Errorf("db image = %q, want postgres:17 (declared engine used as-is)", img)
	}
	// And the API was injected a scoped per-Session DSN.
	if dsn := containerEnv(ctx, t, project+"-api-1", "DATABASE_URL"); !strings.HasPrefix(dsn, "postgres://showcase:") || !strings.Contains(dsn, "@db:5432/showcase") {
		t.Errorf("DATABASE_URL not injected as a scoped DSN: %q", dsn)
	}
	// (The DB tearing down with the Session — no leaked volume — is covered by the
	// Runner's own integration test, TestProvisionBootsReachableStackThenTeardownLeavesNothing.)
}

// TestPerSessionSeedDataIsolated proves the third acceptance criterion of #17:
// each Session boots with the representative seed data, and one Guest's writes
// never reach another — the per-Session DB + named volume of #16 give structural
// isolation. It boots two Sessions from the same built images, writes to one with
// that Session's own injected creds, and shows the other never sees the write.
func TestPerSessionSeedDataIsolated(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	built := buildFixture(ctx, t)

	reg := proxy.NewRegistry("localhost")
	r := runner.NewCompose(runner.StaticSource{P: built}, runner.WithRuntime("runc"), runner.WithProxy(reg, ""))

	boot := func(label string) (sessionID, network, apiURL string) {
		sessionID, err := proxy.NewSessionID()
		if err != nil {
			t.Fatalf("session id (%s): %v", label, err)
		}
		t.Cleanup(func() { _ = r.Teardown(context.Background(), sessionID) })
		if _, err := r.Provision(ctx, "fixture", sessionID); err != nil {
			t.Fatalf("provision session %s: %v", label, err)
		}
		route, ok := reg.Get(sessionID)
		if !ok || route.State != proxy.Live || len(route.APIs) != 1 {
			t.Fatalf("session %s should be Live with an API, got %+v ok=%v", label, route, ok)
		}
		return sessionID, runner.NetworkName(sessionID), route.APIs[0].URL
	}

	sessionA, netA, apiA := boot("A")
	_, netB, apiB := boot("B")

	// Both Sessions boot with the representative seed data.
	for _, s := range []struct{ label, network, api string }{{"A", netA, apiA}, {"B", netB, apiB}} {
		body := curl(ctx, t, s.network, s.api+"/api/items")
		for _, want := range []string{"alpha", "beta", "gamma"} {
			if !strings.Contains(body, want) {
				t.Errorf("session %s missing seeded item %q: %q", s.label, want, body)
			}
		}
	}

	// A Guest writes to Session A's DB using A's own injected creds.
	const mark = "delta-from-a"
	dsnA := containerEnv(ctx, t, runner.ProjectName(sessionA)+"-api-1", "DATABASE_URL")
	if out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", netA,
		"postgres:17", "psql", dsnA, "-c", "INSERT INTO items (name) VALUES ('"+mark+"')").CombinedOutput(); err != nil {
		t.Fatalf("write to session A: %v\n%s", err, out)
	}

	// Session A reflects its own write; Session B never sees it.
	if a := curl(ctx, t, netA, apiA+"/api/items"); !strings.Contains(a, mark) {
		t.Errorf("session A should reflect its own write, got %q", a)
	}
	if b := curl(ctx, t, netB, apiB+"/api/items"); strings.Contains(b, mark) {
		t.Errorf("session B must not see session A's write, got %q", b)
	}
}

// TestBuildHasEgress pins ADR-0007's other half: build-time egress stays
// PERMITTED. Unlike the sealed runtime Stack (internal network, default-deny), the
// sandboxed build runs on a dedicated egress-enabled build network (ADR-0010) so
// it can fetch dependencies. It builds a tiny image whose RUN step needs the
// network (apk fetches a package); if the build sandbox cut egress, this build —
// and real Owner builds — would fail.
func TestBuildHasEgress(t *testing.T) {
	requireDocker(t)

	dir := t.TempDir()
	const dockerfile = "Dockerfile"
	// The unique `RUN echo` busts the Docker layer cache so the apk fetch below
	// actually re-executes every run — a warm cache would skip it and prove nothing
	// about egress. alpine:3.21 is a recent, supported base; `apk add` reaching the
	// Alpine CDN is the network signal (it fails if the build has no egress).
	content := fmt.Sprintf("FROM alpine:3.21\nRUN echo %d\nRUN apk add --no-cache tzdata\n", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, dockerfile), []byte(content), 0o644); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	tag, err := New(20, logger).Build(ctx, BuildSpec{
		ImageName:  "showcase-test/build-egress",
		Version:    "v1",
		Dockerfile: dockerfile,
		Context:    func() (string, func(), error) { return dir, func() {}, nil },
	})
	if err != nil {
		t.Fatalf("a build whose RUN needs the network failed; build egress must be permitted (ADR-0007): %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", tag).Run() })
}

// buildFixture builds the embedded UI + API images from source ("publish" warm)
// and returns the Project with the built image tags, registering image cleanup.
// A second call hits the docker build cache and returns the same tags.
func buildFixture(ctx context.Context, t *testing.T) runner.Project {
	t.Helper()
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
	return p
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

// containerEnv reads one environment variable off a container via `docker
// inspect`, which (unlike `exec printenv`) works on a scratch image that carries
// no shell or coreutils — the API image is a static binary on scratch.
func containerEnv(ctx context.Context, t *testing.T, container, key string) string {
	t.Helper()
	out := dockerOut(ctx, t, "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", container)
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("env %s not found on container %s", key, container)
	return ""
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
