package builder

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Sandbox configures the throwaway, hardened container each untrusted Owner build
// runs in (ADR-0010). The platform never runs `docker build` on the host daemon;
// it runs a rootless image builder whose RUN steps cannot touch the control-plane
// host, other Projects/Sessions, or the Docker socket.
type Sandbox struct {
	// Image is the rootless image-builder run per build (default
	// "moby/buildkit:rootless"): daemonless, userspace, exports an OCI image tar.
	Image string
	// Network is a dedicated bridge the build joins: internet egress for deps
	// (ADR-0007) but a separate L2 segment with no route to the control plane or
	// Sessions. Created on demand.
	Network string
	// MemoryMB, CPUs and PidsLimit cap build resources so a runaway RUN (fork
	// bomb, memory hog) is bounded, not just the wall clock.
	MemoryMB  int
	CPUs      float64
	PidsLimit int
	// Timeout is the wall-clock cap per build; on expiry the build container is
	// force-removed. 0 disables (the caller's context still applies).
	Timeout time.Duration
}

const (
	defaultSandboxImage   = "moby/buildkit:rootless"
	defaultBuildNetwork   = "showcase-build"
	defaultBuildMemoryMB  = 2048
	defaultBuildCPUs      = 2.0
	defaultBuildPidsLimit = 2048
	defaultBuildTimeout   = 5 * time.Minute
)

func defaultSandbox() Sandbox {
	return Sandbox{
		Image:     defaultSandboxImage,
		Network:   defaultBuildNetwork,
		MemoryMB:  defaultBuildMemoryMB,
		CPUs:      defaultBuildCPUs,
		PidsLimit: defaultBuildPidsLimit,
		Timeout:   defaultBuildTimeout,
	}
}

// withDefaults fills unset fields so a partial WithSandbox config stays safe — in
// particular an unset Timeout falls back to the default rather than disabling the
// wall-clock cap on untrusted builds.
func (s Sandbox) withDefaults() Sandbox {
	d := defaultSandbox()
	if s.Image == "" {
		s.Image = d.Image
	}
	if s.Network == "" {
		s.Network = d.Network
	}
	if s.MemoryMB <= 0 {
		s.MemoryMB = d.MemoryMB
	}
	if s.CPUs <= 0 {
		s.CPUs = d.CPUs
	}
	if s.PidsLimit <= 0 {
		s.PidsLimit = d.PidsLimit
	}
	if s.Timeout <= 0 {
		s.Timeout = d.Timeout
	}
	return s
}

// sandboxBuild builds tag from the Dockerfile at dockerfileRel inside contextDir,
// in a rootless BuildKit container, and loads the exported OCI tar into the local
// image store (ADR-0010). It is Builder's production buildFunc.
func (b *Builder) sandboxBuild(ctx context.Context, tag, contextDir, dockerfileRel string) ([]byte, error) {
	if err := b.ensureNetwork(ctx); err != nil {
		return nil, err
	}
	// The rootless builder runs as a UID that differs from the host user which
	// materialised the context (e.g. CI's runner), so make the throwaway context
	// readable by it before mounting it read-only — otherwise BuildKit can't even
	// lstat the Dockerfile.
	if err := relaxContextPerms(contextDir); err != nil {
		return nil, fmt.Errorf("build sandbox: prepare context: %w", err)
	}
	// The wall-clock cap bounds only the untrusted build steps. The trusted
	// host-side `docker load` further down stays on the parent ctx, so a build that
	// nearly exhausts the cap can't truncate the load and surface it as a build
	// failure.
	buildCtx := ctx
	if b.sandbox.Timeout > 0 {
		var cancel context.CancelFunc
		buildCtx, cancel = context.WithTimeout(ctx, b.sandbox.Timeout)
		defer cancel()
	}

	suffix, err := randHex()
	if err != nil {
		return nil, fmt.Errorf("build sandbox: %w", err)
	}
	name := "showcase-build-" + suffix

	tarFile, err := os.CreateTemp("", "showcase-build-*.tar")
	if err != nil {
		return nil, fmt.Errorf("build sandbox: temp tar: %w", err)
	}
	tarPath := tarFile.Name()
	defer os.Remove(tarPath)

	// Force-remove the build container on every exit path. A wall-clock timeout
	// kills the `docker run` client, but the daemon would keep the build running,
	// so this explicit rm — on a fresh context, since ctx may be expired — is what
	// actually enforces the cap. (No --rm, so this is the single teardown path.)
	defer func() { _, _ = b.run(context.Background(), "rm", "-f", name) }()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(buildCtx, "docker", b.sandbox.runArgs(name, tag, contextDir, dockerfileRel)...)
	cmd.Stdout = tarFile // the OCI image tar (dest=-)
	cmd.Stderr = &stderr // buildkit progress + errors
	runErr := cmd.Run()
	_ = tarFile.Close()
	if runErr != nil {
		if buildCtx.Err() != nil {
			return stderr.Bytes(), fmt.Errorf("sandboxed build exceeded limits: %w", buildCtx.Err())
		}
		return stderr.Bytes(), fmt.Errorf("sandboxed build failed: %w", runErr)
	}

	// Load the exported image into the host store, tagged as `tag` (the tag rides
	// in the tar via --output name=...), so the Runner boots it unchanged.
	if out, err := b.run(ctx, "load", "-i", tarPath); err != nil {
		return append(stderr.Bytes(), out...), fmt.Errorf("load built image %s: %w", tag, err)
	}
	return stderr.Bytes(), nil
}

// runArgs builds the `docker run …` argv for one sandboxed build. It is the whole
// lockdown surface, so it is its own function (unit-tested in isolation, no Docker
// needed): no Docker socket, no host mount but the read-only context, dropped
// privileges, resource caps, and the dedicated build network.
func (s Sandbox) runArgs(name, tag, contextDir, dockerfileRel string) []string {
	args := []string{
		"run", "--name", name,
		"--network", s.Network,
		// Rootless BuildKit maps build-"root" to an unprivileged host UID (the real
		// isolation), so it needs neither --privileged nor the Docker socket. It does
		// need these relaxations to set up its user namespace + overlay snapshotter
		// and to let its nested RUN steps mount their own /proc (Docker's default
		// proc masking otherwise makes /proc not "fully visible", so the nested mount
		// is refused). These widen only the build container's own surface, not the
		// host's — that residual is named in ADR-0010.
		"--security-opt", "seccomp=unconfined",
		"--security-opt", "apparmor=unconfined",
		"--security-opt", "systempaths=unconfined",
		"--device", "/dev/fuse",
		// The build context is the ONLY host path mounted, read-only. No Docker
		// socket, no other host mounts — an Owner RUN step can reach neither the
		// control-plane host nor other Projects/Sessions through the filesystem.
		"-v", contextDir + ":/workspace:ro",
		"--entrypoint", "buildctl-daemonless.sh",
	}
	if s.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(s.MemoryMB)+"m")
	}
	if s.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(s.CPUs, 'f', -1, 64))
	}
	if s.PidsLimit > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(s.PidsLimit))
	}
	args = append(args, s.Image,
		"build", "--frontend", "dockerfile.v0",
		"--local", "context=/workspace",
		"--local", "dockerfile=/workspace",
		"--opt", "filename="+dockerfileRel,
		// Export a docker-loadable image tar to stdout (dest=-): no writable host
		// mount needed, the tar streams straight into `docker load`.
		"--output", "type=docker,name="+tag+",dest=-",
	)
	return args
}

// ensureNetwork creates the dedicated build bridge if it does not yet exist
// (memoised under b.mu). It is a normal — not internal — bridge: a build needs
// egress to fetch dependencies (ADR-0007), but as a separate L2 segment it has no
// route to the control plane or per-Session networks, so a build can reach package
// registries yet not the platform or other Sessions.
func (b *Builder) ensureNetwork(ctx context.Context) error {
	if b.sandbox.Network == "" || b.netReady {
		return nil
	}
	if _, err := b.run(ctx, "network", "inspect", b.sandbox.Network); err == nil {
		b.netReady = true
		return nil
	}
	if out, err := b.run(ctx, "network", "create", b.sandbox.Network); err != nil {
		if !strings.Contains(string(out), "already exists") {
			return fmt.Errorf("create build network %s: %w\n%s", b.sandbox.Network, err, out)
		}
	}
	b.netReady = true
	return nil
}

// relaxContextPerms makes the throwaway build context readable by the rootless
// builder's UID, which differs from the host user that materialised the context.
// It only ADDS group/other read (and dir-traverse) bits, never stripping an
// executable bit a Dockerfile RUN might rely on, and it never follows symlinks —
// a malicious Owner repo (#19) could otherwise point one at a host file and have
// it chmod'd world-readable. The context is a throwaway dir on a single-tenant
// control-plane host, so widening its read perms for the build window is harmless.
func relaxContextPerms(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never chmod through a symlink (it could target a host file)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		add := fs.FileMode(0o044) // r-- for group+other on files
		if d.IsDir() {
			add = 0o055 // r-x so the builder can traverse
		}
		mode := info.Mode().Perm()
		if mode|add == mode {
			return nil // already permissive enough
		}
		return os.Chmod(path, mode|add)
	})
}

// randHex returns 16 hex chars of randomness for a unique, non-colliding build
// container name (also across control-plane processes sharing one daemon).
func randHex() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("random build id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
