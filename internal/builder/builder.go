// Package builder builds Project images from source at publish time and caches
// them (ADR-0003): an image is built once per source version (a content hash now,
// a git commit once real repos arrive in #19), reused across Sessions, rebuilt
// when the version changes, and LRU-evicted when the cache outgrows its cap.
// Guests never wait on a build — the control plane warms the cache at startup.
//
// Untrusted Owner builds are sandboxed (ADR-0010): rather than running
// `docker build` on the host daemon — whose RUN steps would execute on the
// control-plane host — each build runs in a throwaway, rootless BuildKit
// container with no Docker socket, no host mounts but the read-only context,
// resource caps, a wall-clock timeout, and a dedicated egress-only build network
// with no route to the control plane or Sessions. The result is exported as an
// OCI tar and loaded into the local image store, so the cache contract and the
// Runner are unchanged (see sandbox.go). ADR-0010 names the trust threshold:
// this pragmatic isolation suffices for #19's trusted-ish Owners; prod-grade
// isolation (gVisor-wrapped builds / a dedicated build VM, ADR-0009) is a
// required gate before builds open to anonymous Owners.
package builder

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
)

// BuildSpec describes one image to build.
type BuildSpec struct {
	// ImageName is the untagged image name (e.g. "showcase/fixture-web"); the
	// builder appends ":<version>" to form the tag.
	ImageName string
	// Version is the cache key — a content hash or git commit. Same version =>
	// cache hit (no rebuild); a change rebuilds and invalidates the old image.
	Version string
	// Dockerfile is the path to the Dockerfile relative to the build context dir.
	Dockerfile string
	// Context materialises the build context and returns it with a cleanup func.
	// It is invoked only on a cache miss, so a warm build does no filesystem work.
	Context func() (dir string, cleanup func(), err error)
}

// runFunc runs docker and returns its combined output — injectable for tests.
// It carries the host-side image operations (load, image rm); the sandboxed
// build itself goes through buildFunc.
type runFunc func(ctx context.Context, args ...string) ([]byte, error)

func dockerRun(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// buildFunc builds the image tagged tag from the Dockerfile at dockerfileRel
// (relative to contextDir) inside an isolated sandbox and loads the result into
// the local image store. It returns the builder's log output for error context.
// Injectable so unit tests exercise cache/invalidation/eviction without a real
// sandbox; production is Builder.sandboxBuild (sandbox.go).
type buildFunc func(ctx context.Context, tag, contextDir, dockerfileRel string) ([]byte, error)

// entry is a cached build: the source version it was built from, its image tag,
// and a recency counter for LRU eviction.
type entry struct {
	version string
	tag     string
	used    uint64
}

// Builder builds and caches images. It is safe for concurrent use; builds are
// serialized (the cache cap is small and warming happens at startup, so this is
// simpler than per-key build dedup and avoids two builds racing one image name).
type Builder struct {
	run     runFunc
	build   buildFunc
	sandbox Sandbox
	max     int
	logger  *slog.Logger

	mu       sync.Mutex
	cache    map[string]entry // keyed by ImageName
	tick     uint64
	netReady bool // the build network has been ensured (memoised under mu)
}

// Option configures a Builder.
type Option func(*Builder)

// WithSandbox overrides the build-sandbox configuration (ADR-0010). Unset fields
// fall back to defaults; see Sandbox and defaultSandbox.
func WithSandbox(s Sandbox) Option {
	return func(b *Builder) { b.sandbox = s.withDefaults() }
}

// New builds a Builder that keeps at most max cached images (<= 0 means 1).
// Without options it sandboxes builds with the default rootless-BuildKit config.
func New(max int, logger *slog.Logger, opts ...Option) *Builder {
	if max < 1 {
		max = 1
	}
	b := &Builder{run: dockerRun, max: max, logger: logger, cache: map[string]entry{}, sandbox: defaultSandbox()}
	for _, opt := range opts {
		opt(b)
	}
	// Default build backend reads b.sandbox at call time, so WithSandbox above
	// (and any test override of b.build) takes effect.
	b.build = b.sandboxBuild
	return b
}

// Build returns the image tag for spec, building from source on a cache miss.
// On a hit (same version already built) it returns the cached tag without
// touching docker or the filesystem. A version change rebuilds and removes the
// previous image; exceeding the cap LRU-evicts the coldest image. A build failure
// returns an error carrying the builder's output (surfaced to the Owner by the
// caller).
func (b *Builder) Build(ctx context.Context, spec BuildSpec) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tick++

	if e, ok := b.cache[spec.ImageName]; ok && e.version == spec.Version {
		e.used = b.tick
		b.cache[spec.ImageName] = e
		return e.tag, nil
	}

	dir, cleanup, err := spec.Context()
	if err != nil {
		return "", fmt.Errorf("build context for %s: %w", spec.ImageName, err)
	}
	defer cleanup()

	tag := spec.ImageName + ":" + shortVersion(spec.Version)
	if out, err := b.build(ctx, tag, dir, spec.Dockerfile); err != nil {
		return "", fmt.Errorf("build %s: %w\n%s", tag, err, out)
	}

	// A new version supersedes the old image for this name — remove it (ADR-0003:
	// invalidate on new commit), best-effort.
	if prev, ok := b.cache[spec.ImageName]; ok && prev.tag != tag {
		b.removeImage(ctx, prev.tag, "invalidated")
	}
	b.cache[spec.ImageName] = entry{version: spec.Version, tag: tag, used: b.tick}
	b.evict(ctx)
	return tag, nil
}

// evict removes least-recently-used images until the cache is within its cap.
// Caller holds b.mu.
func (b *Builder) evict(ctx context.Context) {
	for len(b.cache) > b.max {
		var lruKey string
		var lruUsed uint64 = ^uint64(0)
		for k, e := range b.cache {
			if e.used < lruUsed {
				lruUsed, lruKey = e.used, k
			}
		}
		b.removeImage(ctx, b.cache[lruKey].tag, "evicted")
		delete(b.cache, lruKey)
	}
}

func (b *Builder) removeImage(ctx context.Context, tag, why string) {
	if out, err := b.run(ctx, "image", "rm", "-f", tag); err != nil {
		b.logger.Warn("builder: removing image failed", "tag", tag, "reason", why, "err", err, "out", string(out))
	}
}

// shortVersion trims a long version (e.g. a sha256 hex digest) to a tag-friendly
// length; short versions pass through unchanged. Versions are content hashes / git
// shas, not adversarial input, so a 12-hex-char (48-bit) prefix collision — which
// would alias two versions to one tag — is not a practical concern.
func shortVersion(v string) string {
	const n = 12
	if len(v) > n {
		return v[:n]
	}
	return v
}
