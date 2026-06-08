//go:build integration

// Adversarial boundary tests for #31 (ADR-0010): they prove the build sandbox
// holds — a build can fetch dependencies but cannot reach the control plane or a
// Session, an untrusted Owner RUN cannot phone home across networks, and a runaway
// RUN is killed by the wall-clock cap. These are the gate #19 depends on, so they
// assert behaviour (reachable / isolated / killed), never specific docker flags
// (those are pinned without Docker in sandbox_test.go). Require Docker. Run with
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
)

// TestSandboxBuildNetworkIsolation proves AC1+AC2 of #31 deterministically: the
// build network has internet egress (deps, ADR-0007) but no route to a sentinel on
// a separate, Session-style network — so a build can reach package registries yet
// not the control plane or another Session.
func TestSandboxBuildNetworkIsolation(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	b := New(20, logger)
	if err := b.ensureNetwork(ctx); err != nil {
		t.Fatalf("ensure build network: %v", err)
	}
	buildNet := b.sandbox.Network

	// A sentinel standing in for a Session / the control plane: it lives on its own
	// bridge, never the build network. startSentinel proves it's actually up by
	// reaching it from its own network first, so the deny below can't be vacuous.
	victimNet := "showcase-victim-" + mustHex(t)
	mustDocker(ctx, t, "network", "create", victimNet)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", victimNet).Run() })
	sentinelIP := startSentinel(ctx, t, victimNet)

	// AC2: the build network reaches the internet (so builds can fetch deps).
	if out, err := dockerCurl(ctx, buildNet, "-sS", "--max-time", "15", "https://example.com"); err != nil {
		t.Fatalf("build network should have internet egress (ADR-0007): %v\n%s", err, out)
	}
	// AC1: but it has NO route to the sentinel's network — a build cannot reach a
	// Session or the control plane.
	if out, err := dockerCurl(ctx, buildNet, "-s", "--max-time", "8", "http://"+sentinelIP+"/"); err == nil {
		t.Fatalf("the build network reached a sentinel on a Session-style network; isolation breached\n%s", out)
	}
}

// TestSandboxBuildRunCannotReachSessions drives a real malicious Dockerfile: its
// RUN tries to phone home to the sentinel. Because the build runs on the isolated
// build network, the curl is blocked, so the RUN's `if reached -> exit 1` never
// fires and the build succeeds. A breach would print SENTINEL-REACHED and fail the
// build; a build that dies before the network check is reported as inconclusive
// (so a broken egress can't masquerade as a passing isolation test).
func TestSandboxBuildRunCannotReachSessions(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	b := New(20, logger)
	if err := b.ensureNetwork(ctx); err != nil {
		t.Fatalf("ensure build network: %v", err)
	}

	victimNet := "showcase-victim-" + mustHex(t)
	mustDocker(ctx, t, "network", "create", victimNet)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", victimNet).Run() })
	sentinelIP := startSentinel(ctx, t, victimNet)

	// apk add proves the build network's egress works (so a failure below is the
	// cross-network reach, not a dead build network); the curl proves it can't
	// cross into the sentinel's network.
	df := fmt.Sprintf("FROM alpine:3.21\n"+
		"RUN apk add --no-cache curl\n"+
		"RUN if curl -sf --max-time 6 -o /dev/null http://%s/; then echo SENTINEL-REACHED; exit 1; fi; echo isolated-ok\n", sentinelIP)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}

	tag, err := b.Build(ctx, BuildSpec{
		ImageName:  "showcase-test/exfil-attempt",
		Version:    "v1",
		Dockerfile: "Dockerfile",
		Context:    func() (string, func(), error) { return dir, func() {}, nil },
	})
	if err != nil {
		if strings.Contains(err.Error(), "SENTINEL-REACHED") {
			t.Fatalf("a build RUN reached a sentinel on a Session-style network; isolation breached:\n%v", err)
		}
		t.Fatalf("inconclusive: the build failed before the network check (e.g. apk/egress), not on isolation:\n%v", err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", tag).Run() })
}

// TestSandboxBuildResourceCapTimeout proves AC2's resource caps: a build whose RUN
// never terminates is killed by the wall-clock cap (and the build container is
// force-removed), rather than running for its full sleep or hanging the platform.
func TestSandboxBuildResourceCapTimeout(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Pre-warm the images so the 25s cap is spent on the runaway RUN, not a cold
	// image pull — otherwise the deadline could fire mid-pull and the test would
	// pass without ever exercising the runaway path (a vacuous gate).
	for _, img := range []string{"moby/buildkit:rootless", "alpine:3.21"} {
		if out, err := exec.CommandContext(ctx, "docker", "pull", "-q", img).CombinedOutput(); err != nil {
			t.Fatalf("pre-pull %s: %v\n%s", img, err, out)
		}
	}

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	// A tight wall-clock cap; every other knob defaults.
	b := New(20, logger, WithSandbox(Sandbox{Timeout: 25 * time.Second}))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.21\nRUN sleep 600\n"), 0o644); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}

	start := time.Now()
	tag, err := b.Build(ctx, BuildSpec{
		ImageName:  "showcase-test/runaway",
		Version:    "v1",
		Dockerfile: "Dockerfile",
		Context:    func() (string, func(), error) { return dir, func() {}, nil },
	})
	elapsed := time.Since(start)
	if err == nil {
		_ = exec.Command("docker", "image", "rm", "-f", tag).Run()
		t.Fatal("a build with a non-terminating RUN should have been killed by the wall-clock cap")
	}
	if !strings.Contains(err.Error(), "exceeded limits") {
		t.Errorf("expected a wall-clock timeout (exceeded limits), got: %v", err)
	}
	// The kill must be the ~25s deadline firing on the running RUN — not an instant
	// setup error (which returns well before 25s) and not the 600s sleep running out.
	if elapsed < 20*time.Second {
		t.Errorf("build failed after only %s; too early for the 25s cap to have fired on the RUN", elapsed)
	}
	if elapsed > 2*time.Minute {
		t.Errorf("build ran %s; the 25s wall-clock cap did not kill the runaway", elapsed)
	}
}

// --- helpers ---------------------------------------------------------------

func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("docker not available; skipping builder integration test")
	}
	// Every build goes through rootless BuildKit, which needs /dev/fuse (the sandbox
	// passes --device /dev/fuse). Skip cleanly rather than hard-fail on a Docker host
	// that lacks it (some local WSL2 setups); CI's ubuntu-latest has it.
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("/dev/fuse not available; skipping rootless-BuildKit build test")
	}
}

func mustHex(t *testing.T) string {
	t.Helper()
	h, err := randHex()
	if err != nil {
		t.Fatalf("randHex: %v", err)
	}
	return h
}

func mustDocker(ctx context.Context, t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// dockerCurl runs curl in a throwaway container on the given network and returns
// the result without failing the test, so callers can assert success OR failure.
func dockerCurl(ctx context.Context, network string, curlArgs ...string) ([]byte, error) {
	args := append([]string{"run", "--rm", "--network", network, "curlimages/curl:latest"}, curlArgs...)
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// startSentinel runs a tiny HTTP server on the given network and returns its IP,
// having first confirmed it answers from its own network (a positive control, so a
// later cross-network deny is meaningful and not just an unstarted sentinel).
func startSentinel(ctx context.Context, t *testing.T, network string) (ip string) {
	t.Helper()
	name := "showcase-sentinel-" + mustHex(t)
	mustDocker(ctx, t, "run", "-d", "--name", name, "--network", network, "traefik/whoami")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	ip = strings.TrimSpace(dockerOut(ctx, t, "inspect", "-f",
		fmt.Sprintf("{{(index .NetworkSettings.Networks %q).IPAddress}}", network), name))
	if ip == "" {
		t.Fatalf("sentinel %s has no IP on %s", name, network)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := dockerCurl(ctx, network, "-s", "--max-time", "3", "http://"+ip+"/"); err == nil {
			return ip
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("sentinel %s never became reachable on its own network", name)
	return ip
}
