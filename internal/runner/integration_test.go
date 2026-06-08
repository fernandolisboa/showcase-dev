//go:build integration

// Seam-2 tests: they boot real containers and require Docker. Run with
//
//	go test -tags integration ./internal/runner/...
//
// They assert behaviour (reachable, healthy, isolated, no leaks) — never
// specific docker flags.
package runner

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

// TestProvisionKeepsProxyRegistryCurrent checks the ADR-0004 contract that the
// Runner keeps the proxy's sessionId->backend map current: a successful boot
// leaves the Session Live with its UI backend registered; teardown drops it.
// (No real Traefik attach here — WithProxy with an empty container name skips it.)
func TestProvisionKeepsProxyRegistryCurrent(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	reg := proxy.NewRegistry("localhost")
	r := NewCompose(StaticSource{P: FixtureProject()}, WithRuntime("runc"), WithProxy(reg, ""))
	sessionID := "px" + mustToken(t, 6)
	t.Cleanup(func() { _ = r.Teardown(context.Background(), sessionID) })

	if _, err := r.Provision(ctx, "fixture", sessionID); err != nil {
		t.Fatalf("provision: %v", err)
	}
	route, ok := reg.Get(sessionID)
	if !ok {
		t.Fatal("session should be registered after provision")
	}
	if route.State != proxy.Live {
		t.Errorf("session should be Live after a healthy boot, got %v", route.State)
	}
	if want := "http://s-" + sessionID + "-web-1:8080"; route.UI.URL != want {
		t.Errorf("UI backend = %q, want %q", route.UI.URL, want)
	}

	if err := r.Teardown(ctx, sessionID); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if _, ok := reg.Get(sessionID); ok {
		t.Error("session route should be gone after teardown")
	}
}

func TestProvisionBootsReachableStackThenTeardownLeavesNothing(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	r := NewCompose(StaticSource{P: FixtureProject()}, WithRuntime("runc"))
	sessionID := "it" + mustToken(t, 6)
	project, network := ProjectName(sessionID), NetworkName(sessionID)
	// Safety net even if an assertion fails mid-test.
	t.Cleanup(func() { _ = r.Teardown(context.Background(), sessionID) })

	url, err := r.Provision(ctx, "fixture", sessionID)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if url != "http://web:8080" {
		t.Fatalf("reachable url = %q", url)
	}

	// Reachable + healthy: a sidecar on the Session network can fetch the UI.
	if out, err := dockerRun(ctx, network, "-sf", "--max-time", "15", url); err != nil {
		t.Fatalf("UI not reachable at %s: %v\n%s", url, err, out)
	}

	// Health (the crash signal, #13) reports a healthy Stack end-to-end.
	if healthy, err := r.Health(ctx, sessionID); err != nil || !healthy {
		t.Errorf("Health = %v, err = %v; want healthy", healthy, err)
	}

	// Default-deny egress (ADR-0007): the same internal network cannot reach out.
	// By hostname (the obvious case)...
	if _, err := dockerRun(ctx, network, "-s", "--max-time", "8", "https://example.com"); err == nil {
		t.Error("expected egress to a hostname to be denied on the internal Session network")
	}
	// ...and by raw IP over plain HTTP, so this proves an L3 route-less deny, not a
	// DNS lookup that fails to resolve (literal address skips name resolution) nor
	// a TLS handshake that fails for some other reason — it must fail at TCP connect.
	if _, err := dockerRun(ctx, network, "-s", "--max-time", "8", "http://1.1.1.1"); err == nil {
		t.Error("expected egress to a raw IP to be denied on the internal Session network")
	}

	if err := r.Teardown(ctx, sessionID); err != nil {
		t.Fatalf("teardown: %v", err)
	}

	// With the Stack gone, Health reports unhealthy (no containers) rather than erroring.
	if healthy, err := r.Health(ctx, sessionID); err != nil || healthy {
		t.Errorf("Health after teardown = %v, err = %v; want unhealthy, no error", healthy, err)
	}

	// No leaks: no containers, no network, no volumes remain for this project.
	if got := dockerOut(t, "ps", "-aq", "--filter", "label=com.docker.compose.project="+project); got != "" {
		t.Errorf("leaked containers after teardown: %q", got)
	}
	if got := dockerOut(t, "network", "ls", "--format", "{{.Name}}", "--filter", "name=^"+network+"$"); got != "" {
		t.Errorf("leaked network after teardown: %q", got)
	}
	if got := dockerOut(t, "volume", "ls", "-q", "--filter", "label=com.docker.compose.project="+project); got != "" {
		t.Errorf("leaked volumes after teardown: %q", got)
	}
}

// TestProvisionFailedBootLeavesNothing exercises the ADR-0006 fail-graceful path:
// a boot that can never succeed (a bogus image ref) must surface an error and
// leave zero containers, network, or volumes behind.
func TestProvisionFailedBootLeavesNothing(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Same fixture, but the app image can't be pulled, so `up --wait` fails.
	proj := FixtureProject()
	proj.Images = map[string]string{"web": "showcase-dev/does-not-exist:nope"}

	r := NewCompose(StaticSource{P: proj}, WithRuntime("runc"))
	sessionID := "itfail" + mustToken(t, 6)
	project, network := ProjectName(sessionID), NetworkName(sessionID)
	// Safety net in case the provision unexpectedly succeeds or partially boots.
	t.Cleanup(func() { _ = r.Teardown(context.Background(), sessionID) })

	if _, err := r.Provision(ctx, "fixture", sessionID); err == nil {
		t.Fatal("expected provision to fail on a bogus image ref")
	}

	// Fail-graceful: the failed boot must have torn itself down completely.
	if got := dockerOut(t, "ps", "-aq", "--filter", "label=com.docker.compose.project="+project); got != "" {
		t.Errorf("leaked containers after failed boot: %q", got)
	}
	if got := dockerOut(t, "network", "ls", "--format", "{{.Name}}", "--filter", "name=^"+network+"$"); got != "" {
		t.Errorf("leaked network after failed boot: %q", got)
	}
	if got := dockerOut(t, "volume", "ls", "-q", "--filter", "label=com.docker.compose.project="+project); got != "" {
		t.Errorf("leaked volumes after failed boot: %q", got)
	}
}

// TestEgressAllowListEnforcement is the end-to-end proof of ADR-0011: with a
// declared allow-list, an allowed host:port is reachable through the egress-proxy
// sidecar, a non-allowed host is refused BY the proxy, and — the load-bearing
// claim — an app that ignores the proxy and dials out directly still has no route
// (routelessness, not the proxy, is the enforcement boundary).
func TestEgressAllowListEnforcement(t *testing.T) {
	requireDocker(t)
	buildEgressProxyImage(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	sessionID := "iteg" + mustToken(t, 6)
	network := NetworkName(sessionID)
	egressNet := ProjectName(sessionID) + "-egress-net"
	// A target that stands in for "an allowed external host", reachable only on the
	// egress net (never the sealed Session net). Allow-listed by its name:80.
	target := "egtgt" + mustToken(t, 6)
	proxyURL := "http://egress:8888"

	proj := FixtureProject()
	proj.Manifest.Egress = []runcontract.EgressRule{{Host: target, Port: 80}}

	r := NewCompose(StaticSource{P: proj}, WithRuntime("runc"), WithEgressProxyImage(egressProxyTestImage))
	t.Cleanup(func() {
		_, _ = exec.Command("docker", "rm", "-f", target).CombinedOutput()
		_ = r.Teardown(context.Background(), sessionID)
	})

	if _, err := r.Provision(ctx, "fixture", sessionID); err != nil {
		t.Fatalf("provision: %v", err)
	}

	// Stand up the allow-listed target on the egress net (the only segment that
	// reaches it). whoami answers 200 on any path on :80.
	if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", target,
		"--network", egressNet, "traefik/whoami").CombinedOutput(); err != nil {
		t.Fatalf("start egress target: %v\n%s", err, out)
	}

	// Allowed: an app reaches target:80 THROUGH the proxy (the convenience path).
	if out, err := dockerRun(ctx, network, "-sf", "--max-time", "20", "--proxy", proxyURL, "http://"+target+"/"); err != nil {
		t.Fatalf("allow-listed host must be reachable through the proxy: %v\n%s", err, out)
	}

	// Blocked by the proxy: a non-allowed host is refused (curl -f fails on 403),
	// even via the proxy — the proxy is allow-list-only, never an open relay.
	if _, err := dockerRun(ctx, network, "-sf", "--max-time", "15", "--proxy", proxyURL, "http://1.1.1.1/"); err == nil {
		t.Error("a non-allow-listed host must be refused by the proxy")
	}

	// Bypass denied: an app that IGNORES the proxy and dials a raw IP directly
	// still has no route — routelessness on the internal net is the real boundary,
	// unchanged by the presence of an allow-list (ADR-0011).
	if _, err := dockerRun(ctx, network, "-s", "--max-time", "8", "http://1.1.1.1"); err == nil {
		t.Error("direct egress (ignoring the proxy) must still be denied even with an allow-list")
	}

	if err := r.Teardown(ctx, sessionID); err != nil {
		t.Fatalf("teardown: %v", err)
	}
}

// egressProxyTestImage is the local tag the integration test builds and the
// Runner injects as the platform egress-proxy image.
const egressProxyTestImage = "showcase-dev/egress-proxy:test"

var buildProxyOnce sync.Once

// buildEgressProxyImage builds the egress-proxy image once per test binary from
// the repo root (the Dockerfile's build context is the module root).
func buildEgressProxyImage(t *testing.T) {
	t.Helper()
	buildProxyOnce.Do(func() {
		// Test CWD is the package dir (internal/runner); the module root is two up.
		out, err := exec.Command("docker", "build", "-t", egressProxyTestImage,
			"-f", "../../cmd/egress-proxy/Dockerfile", "../..").CombinedOutput()
		if err != nil {
			t.Fatalf("build egress-proxy image: %v\n%s", err, out)
		}
	})
}

// mustToken returns a random hex token or fails the test — keeps callers terse.
func mustToken(t *testing.T, n int) string {
	t.Helper()
	tok, err := randToken(n)
	if err != nil {
		t.Fatalf("randToken: %v", err)
	}
	return tok
}

func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("docker not available; skipping Seam-2 integration test")
	}
}

// dockerRun runs curl in a throwaway container attached to the Session network.
func dockerRun(ctx context.Context, network string, curlArgs ...string) ([]byte, error) {
	args := append([]string{"run", "--rm", "--network", network, "curlimages/curl:latest"}, curlArgs...)
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

func dockerOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		t.Fatalf("docker %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
