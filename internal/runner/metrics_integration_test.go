//go:build integration

// Seam-2 adversarial test for #27: the Traefik metrics endpoint must be
// reachable by the control plane (host loopback) but NOT by a container on a
// Session network — its service labels embed Session ids (ADR-0004), so a hostile
// Session could otherwise enumerate other live Sessions. Boots REAL Traefik from
// deploy/traefik so it exercises the shipped static-IP binding, not a stand-in.
//
//	go test -tags integration ./internal/runner/...
package runner

import (
	"context"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
)

// traefikComposeFile is the shipped Traefik stack, relative to this package dir.
const traefikComposeFile = "../../deploy/traefik/compose.yml"

// traefikITProject is a dedicated compose project for this test, distinct from the
// default ("traefik") that `make proxy-up` uses — so the suite never adopts or
// tears down a developer's running proxy. The compose file pins container_name and
// host ports, so if `make proxy-up` is already up, `up` here fails fast on the
// clash instead of silently hijacking (and later destroying) that instance.
const traefikITProject = "showcase-it-traefik"

// metricsURL is the host-loopback scrape path the control plane uses
// (config.TraefikMetricsURL default).
const metricsURL = "http://127.0.0.1:8084/metrics"

// TestMetricsEndpointSessionUnreachable proves the #27 trust boundary end-to-end:
// after Traefik is attached to a live Session network, a container ON that network
// cannot reach :8084, while the host-loopback scrape still returns metrics.
func TestMetricsEndpointSessionUnreachable(t *testing.T) {
	requireDocker(t)

	// Matches the sibling integration tests' budget, and stays under CI's per-job
	// `-timeout` so this test's own deadline fires first with a clean assertion
	// message rather than the harness killing the whole job.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Real Traefik from deploy/traefik — same artifact prod/dev run, so the static
	// metrics binding under test is the one shipped. Cleanup is registered first so
	// it runs LAST (after the Session is torn down and detached).
	deployTraefikUp(ctx, t)
	t.Cleanup(deployTraefikDown)
	waitMetricsReady(ctx, t)

	// Boot a Session and attach Traefik to its network (the moment the hole opened
	// pre-#27: :8084 was bound on 0.0.0.0 so it accepted on this later-joined NIC).
	reg := proxy.NewRegistry("localhost")
	r := NewCompose(StaticSource{P: FixtureProject()}, WithRuntime("runc"), WithProxy(reg, "showcase-traefik"))
	sessionID := "m" + mustToken(t, 6)
	network := NetworkName(sessionID)
	t.Cleanup(func() { _ = r.Teardown(context.Background(), sessionID) })
	if _, err := r.Provision(ctx, "fixture", sessionID); err != nil {
		t.Fatalf("provision: %v", err)
	}

	// Traefik's IP on the Session network — the address a hostile Session sees.
	traefikIP := traefikIPOnNetwork(t, "showcase-traefik", network)
	if traefikIP == "" {
		t.Fatal("Traefik is not attached to the Session network; test setup is invalid")
	}

	// Sanity: :80 IS reachable from the Session network (a 404 is a response, so
	// curl exits 0). This proves the attach worked and that the deny below is
	// port-specific, not a broken/absent route — and it mints an entrypoint metric
	// so the host scrape has Traefik counters to return.
	if _, err := dockerRun(ctx, network, "-s", "-o", "/dev/null", "--max-time", "8", "http://"+traefikIP+":80/"); err != nil {
		t.Fatalf("Traefik :80 should be reachable from the Session network (attach precondition): %v", err)
	}

	// AC1 + AC3: a container on the Session network cannot reach :8084 — so no
	// Session-id-bearing label is exposed on any Session-reachable interface. The
	// listener is bound to Traefik's own-network IP, never this NIC, so the connect
	// is refused (or, failing that, times out); either way curl exits non-zero.
	if out, err := dockerRun(ctx, network, "-s", "--max-time", "8", "http://"+traefikIP+":8084/metrics"); err == nil {
		t.Errorf("metrics endpoint must be UNREACHABLE from a Session network, but the scrape succeeded:\n%s", out)
	}

	// AC2: the control-plane scrape (host loopback) still works — idle teardown
	// keeps its signal. 200 with Traefik counters present.
	code, body := scrapeMetrics(ctx, t, metricsURL)
	if code != http.StatusOK {
		t.Fatalf("host-loopback scrape status = %d, want 200", code)
	}
	if !strings.Contains(body, "traefik_") {
		t.Errorf("host-loopback scrape returned no Traefik metrics:\n%s", body)
	}
	// And the real consumer can parse it without error (idle reaper path).
	if _, err := proxy.NewTraefikMetrics(metricsURL).RequestCounts(ctx); err != nil {
		t.Errorf("control-plane metrics poll failed: %v", err)
	}
}

// deployTraefikUp boots the shipped Traefik stack under a dedicated project name
// (traefikITProject) so it never adopts or destroys a developer's `make proxy-up`
// instance — if one is already running, the pinned container_name/host-port clash
// makes this fail fast instead.
func deployTraefikUp(ctx context.Context, t *testing.T) {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "compose", "-p", traefikITProject, "-f", traefikComposeFile, "up", "-d").CombinedOutput()
	if err != nil {
		t.Fatalf("traefik up: %v\n%s", err, out)
	}
}

func deployTraefikDown() {
	_ = exec.Command("docker", "compose", "-p", traefikITProject, "-f", traefikComposeFile, "down", "-v").Run()
}

// waitMetricsReady blocks until the host-loopback metrics endpoint serves 200, so
// the assertions don't race Traefik's startup.
func waitMetricsReady(ctx context.Context, t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := scrapeMetrics(ctx, t, metricsURL); code == http.StatusOK {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("Traefik metrics endpoint never became ready on host loopback")
}

// scrapeMetrics GETs url and returns the status code (0 on transport error) and
// body. A transport error is not fatal: waitMetricsReady polls through them.
func scrapeMetrics(ctx context.Context, t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build metrics request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// traefikIPOnNetwork returns the container's IPv4 address on the named network, or
// "" if it is not attached. Ranges the network map so a missing key is empty, not
// a template panic.
func traefikIPOnNetwork(t *testing.T, container, network string) string {
	t.Helper()
	out := dockerOut(t, "inspect", "-f",
		`{{range $name, $net := .NetworkSettings.Networks}}{{$name}} {{$net.IPAddress}}{{"\n"}}{{end}}`, container)
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == network {
			return f[1]
		}
	}
	return ""
}
