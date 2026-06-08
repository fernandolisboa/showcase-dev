package builder

import (
	"strings"
	"testing"
)

// TestSandboxRunArgsLockdown pins the whole lockdown surface of a sandboxed build
// (ADR-0010) without needing Docker, so the guarantees can't silently regress: the
// build carries no Docker socket and no writable host mount, only the read-only
// context; it is never privileged or host-networked; and it always sets the
// resource caps and the dedicated build network.
func TestSandboxRunArgsLockdown(t *testing.T) {
	s := defaultSandbox()
	const ctxDir = "/tmp/build-ctx"
	args := s.runArgs("showcase-build-abc", "showcase/x:v1", ctxDir, "Dockerfile")
	joined := strings.Join(args, " ")

	// Exactly one bind mount, and it is the read-only build context.
	var mounts []string
	for i, a := range args {
		if a == "-v" && i+1 < len(args) {
			mounts = append(mounts, args[i+1])
		}
	}
	if len(mounts) != 1 {
		t.Fatalf("a sandboxed build must mount only the build context, got mounts %v", mounts)
	}
	if mounts[0] != ctxDir+":/workspace:ro" {
		t.Errorf("the build context must be mounted read-only, got %q", mounts[0])
	}

	// No Docker socket, ever — that would be host takeover.
	if strings.Contains(joined, "docker.sock") {
		t.Errorf("a sandboxed build must never mount the Docker socket:\n%s", joined)
	}
	// No privilege escalation and no host networking.
	for _, forbidden := range []string{"--privileged", "network host", "--network=host"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("a sandboxed build must never carry %q:\n%s", forbidden, joined)
		}
	}
	// On the dedicated build bridge, not the default/host network.
	if !strings.Contains(joined, "--network "+defaultBuildNetwork) {
		t.Errorf("a sandboxed build must join the dedicated build network %q:\n%s", defaultBuildNetwork, joined)
	}
	// Resource caps are always present (bounded runaway, ADR-0010).
	for _, cap := range []string{"--memory", "--cpus", "--pids-limit"} {
		if !strings.Contains(joined, cap) {
			t.Errorf("a sandboxed build must set %s:\n%s", cap, joined)
		}
	}
	// The image is exported to stdout (dest=-), so no writable host output mount is
	// needed — the tar streams straight into `docker load`.
	if !strings.Contains(joined, "type=docker,name=showcase/x:v1,dest=-") {
		t.Errorf("the build output must stream a docker image tar to stdout (dest=-):\n%s", joined)
	}
	// The userns remap — not cap-drop — IS the host-privilege boundary (ADR-0010),
	// so guard the flags that would silently defeat it or re-grant host root.
	for _, forbidden := range []string{"--userns", "--pid ", "--pid=host", "--ipc ", "--ipc=host", "--cap-add", "--user "} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("a sandboxed build must never carry %q (it would breach the rootless userns boundary):\n%s", forbidden, joined)
		}
	}
	// The single device + the two profile relaxations rootless BuildKit needs (the
	// named residuals in ADR-0010) must stay present — their accidental removal
	// silently breaks the sandbox, and this is the only place that pins them.
	for _, required := range []string{"--device /dev/fuse", "seccomp=unconfined", "apparmor=unconfined", "systempaths=unconfined"} {
		if !strings.Contains(joined, required) {
			t.Errorf("a sandboxed build must carry %q (rootless BuildKit needs it):\n%s", required, joined)
		}
	}
}

// TestSandboxWithDefaults proves a partial WithSandbox config stays safe: unset
// fields — including the wall-clock Timeout — fall back to defaults rather than
// disabling a cap on untrusted builds.
func TestSandboxWithDefaults(t *testing.T) {
	got := Sandbox{MemoryMB: 4096}.withDefaults()
	if got.MemoryMB != 4096 {
		t.Errorf("explicit MemoryMB should survive, got %d", got.MemoryMB)
	}
	if got.Image != defaultSandboxImage || got.Network != defaultBuildNetwork {
		t.Errorf("unset Image/Network must default, got %q / %q", got.Image, got.Network)
	}
	if got.Timeout != defaultBuildTimeout {
		t.Errorf("an unset Timeout must default (never disable the cap), got %s", got.Timeout)
	}
	if got.CPUs <= 0 || got.PidsLimit <= 0 {
		t.Errorf("unset CPUs/PidsLimit must default, got %v / %d", got.CPUs, got.PidsLimit)
	}
}
