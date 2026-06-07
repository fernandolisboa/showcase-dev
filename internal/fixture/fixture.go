// Package fixture is the in-repo, build-from-source Project the platform builds
// to exercise the builder (#14) before real Owner repos arrive (#19). It embeds a
// tiny build context (a Dockerfile + static site) and exposes it as a content
// hash (the cache key, standing in for a git commit) plus an extractor that
// materialises it as a docker build context.
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
)

// Dockerfile is the build context's Dockerfile, relative to the extracted dir.
const Dockerfile = "Dockerfile"

//go:embed app
var appFS embed.FS

// Version is a content hash over the embedded build context — the cache key that
// stands in for a git commit (ADR-0003: invalidate on new commit). It is stable
// across runs and changes only when the fixture source changes, so editing the
// Dockerfile or site triggers a rebuild exactly as a new commit would.
func Version() (string, error) {
	names, err := contextFiles()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, name := range names {
		data, err := appFS.ReadFile(name)
		if err != nil {
			return "", err
		}
		// name + NUL + content, so neither renames nor edits collide.
		fmt.Fprintf(h, "%s\x00", name)
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Extract materialises the embedded build context into a fresh temp directory and
// returns it with a cleanup func. The builder uses the directory as the docker
// build context. The caller must call cleanup once the build is done.
func Extract() (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "showcase-fixture-*")
	if err != nil {
		return "", nil, fmt.Errorf("fixture: temp dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	names, err := contextFiles()
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
		// Strip the embed root ("app/") so files land at the context root.
		rel, _ := filepath.Rel("app", name)
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("fixture: mkdir: %w", err)
		}
		// 0644: these become the image's files via COPY, and the container runs as a
		// non-root user (uid 1000) that must be able to read them — an owner-only
		// mode would make the served files unreadable at runtime.
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("fixture: write %s: %w", rel, err)
		}
	}
	return dir, cleanup, nil
}

// contextFiles returns the embedded build-context file paths, sorted for a
// deterministic content hash.
func contextFiles() ([]string, error) {
	var names []string
	err := fs.WalkDir(appFS, "app", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fixture: walk embed: %w", err)
	}
	sort.Strings(names)
	return names, nil
}
