//go:build integration

// Real-GitHub end-to-end for the App clone path. It exercises App JWT -> installation
// token -> clone at HEAD, asserting .git is stripped and the token never lands in the
// materialized context. It self-skips unless GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY,
// and SHOWCASE_E2E_REPO (an owner/repo the App is installed on) are all set, so CI —
// which has none — skips it. Run locally with those exported (e.g. from .env.local).
package githubapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealClone(t *testing.T) {
	appID := os.Getenv("GITHUB_APP_ID")
	key := os.Getenv("GITHUB_APP_PRIVATE_KEY")
	repoRef := os.Getenv("SHOWCASE_E2E_REPO")
	if appID == "" || key == "" || repoRef == "" {
		t.Skip("set GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY, and SHOWCASE_E2E_REPO to run the real-GitHub clone test")
	}
	owner, repo, err := ParseRepo(repoRef)
	if err != nil {
		t.Fatalf("SHOWCASE_E2E_REPO: %v", err)
	}

	c, err := New(appID, key)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tok, exp, err := c.InstallationToken(ctx, owner, repo)
	if err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}
	if !exp.After(time.Now()) {
		t.Error("installation token is already expired")
	}

	sha, err := ResolveCommit(ctx, owner, repo, "HEAD", tok)
	if err != nil {
		t.Fatalf("ResolveCommit: %v", err)
	}
	dir, cleanup, err := CloneCommit(ctx, owner, repo, sha, tok)
	if err != nil {
		t.Fatalf("CloneCommit: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Error(".git must be stripped from the build context")
	}
	// Belt-and-suspenders for ADR-0008: the token must not appear anywhere in the
	// materialized tree (it went via an auth header, not the URL).
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(p); strings.Contains(string(b), tok) {
			t.Errorf("SECURITY: installation token leaked into %s", p)
		}
		return nil
	})
}
