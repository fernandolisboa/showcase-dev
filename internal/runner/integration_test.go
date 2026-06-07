//go:build integration

// Seam-2 tests: they boot real containers and require Docker. Run with
//   go test -tags integration ./internal/runner/...
// They assert behaviour (reachable, healthy, isolated, no leaks) — never
// specific docker flags.
package runner

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestProvisionBootsReachableStackThenTeardownLeavesNothing(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	r := NewCompose(StaticSource{P: FixtureProject()}, WithRuntime("runc"))
	sessionID := "it" + randToken(6)
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

	// Default-deny egress (ADR-0007): the same internal network cannot reach out.
	if _, err := dockerRun(ctx, network, "-s", "--max-time", "8", "https://example.com"); err == nil {
		t.Error("expected egress to be denied on the internal Session network")
	}

	if err := r.Teardown(ctx, sessionID); err != nil {
		t.Fatalf("teardown: %v", err)
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
