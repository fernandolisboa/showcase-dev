// Package fixture is the in-repo, build-from-source Project the platform builds
// to exercise the builder (#14) and the multi-service Stack (#15) before real
// Owner repos arrive (#19). It declares a UI + API run-contract Manifest and
// embeds a build context per service, each exposed as a content hash (the cache
// key, standing in for a git commit) plus an extractor.
package fixture

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

// Dockerfile is each service context's Dockerfile, relative to the extracted dir.
const Dockerfile = "Dockerfile"

//go:embed web api
var appFS embed.FS

// Manifest is the fixture's run contract: a UI service at "/" plus an API service
// mounted same-origin at "/api" (#15). No DB — the multi-service same-origin path
// is the point here; the platform DB is exercised by the Runner's own fixture and
// by #16. Each service builds from its embedded context (matching dir name).
func Manifest() runcontract.Manifest {
	return runcontract.Manifest{
		Services: []runcontract.Service{
			{Name: "web", Repo: "internal/fixture/web", Dockerfile: Dockerfile, Port: 8080, Role: runcontract.RoleUI},
			{Name: "api", Repo: "internal/fixture/api", Dockerfile: Dockerfile, Port: 8080, Role: runcontract.RoleAPI, PathPrefix: "/api"},
		},
	}
}

// Version is a content hash over a service's embedded build context — the cache
// key that stands in for a git commit (ADR-0003: invalidate on new commit). It is
// stable across runs and changes only when that service's source changes.
func Version(service string) (string, error) {
	names, err := contextFiles(service)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, name := range names {
		data, err := appFS.ReadFile(name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00", name)
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Extract materialises a service's embedded build context into a fresh temp dir
// and returns it with a cleanup func. The builder uses the dir as the docker
// build context. The caller must call cleanup once the build is done.
func Extract(service string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "showcase-fixture-"+service+"-*")
	if err != nil {
		return "", nil, fmt.Errorf("fixture: temp dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	names, err := contextFiles(service)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	for _, name := range names {
		data, err := appFS.ReadFile(name)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		// Strip the service root (e.g. "web/") so files land at the context root.
		rel, _ := filepath.Rel(service, name)
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("fixture: mkdir: %w", err)
		}
		// 0644: these become the image's files via COPY, and the container runs as a
		// non-root user that must read them — an owner-only mode would 404 at runtime.
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("fixture: write %s: %w", rel, err)
		}
	}
	return dir, cleanup, nil
}

// contextFiles returns a service's embedded build-context file paths, sorted for
// a deterministic content hash. An unknown service yields an error.
func contextFiles(service string) ([]string, error) {
	var names []string
	err := fs.WalkDir(appFS, service, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fixture: walk %q: %w", service, err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("fixture: no build context for service %q", service)
	}
	sort.Strings(names)
	return names, nil
}
