package githubapp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHardeningArgsBlockDangerousFeatures(t *testing.T) {
	got := strings.Join(hardeningArgs(), " ")
	for _, want := range []string{
		"protocol.ext.allow=never", // no arbitrary-command transport
		"core.hooksPath=/dev/null", // no repo-controlled hooks
		"filter.lfs.smudge=",       // no LFS smudge subprocess
		"filter.lfs.process=",
		"submodule.recurse=false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("git hardening lost %q (got: %s)", want, got)
		}
	}
}

func TestHardeningEnvLocksDownAndHidesToken(t *testing.T) {
	env := strings.Join(hardeningEnv("sekret-token"), "\n")
	for _, want := range []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"GIT_LFS_SKIP_SMUDGE=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("git env lost %q", want)
		}
	}
	// The token is injected as an auth header via GIT_CONFIG_*, never in argv/URL.
	if !strings.Contains(env, "GIT_CONFIG_VALUE_0=Authorization: Bearer sekret-token") {
		t.Error("token must be passed via the GIT_CONFIG_* auth header, not argv/URL")
	}
	if strings.Contains(strings.Join(hardeningEnv(""), "\n"), "GIT_CONFIG_COUNT") {
		t.Error("no auth config must be set when there is no token")
	}
}

// Negative control proving the env lockdown is load-bearing: a poisoned operator
// global gitconfig (an insteadOf rewrite that redirects the clone elsewhere) is
// HONORED by a plain git but IGNORED by our hardened path (GIT_CONFIG_GLOBAL=/dev/null).
func TestResolveCommitIgnoresOperatorGlobalConfig(t *testing.T) {
	gitOrSkip(t)
	src := initRepo(t, map[string]string{"Dockerfile": "FROM scratch\n"})

	cfg := filepath.Join(t.TempDir(), "gitconfig")
	// Rewrite the src path to a non-existent target (deterministic, no network).
	if err := os.WriteFile(cfg, []byte("[url \"/nonexistent-redirect-target/\"]\n\tinsteadOf = "+src+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Negative control: a plain git honoring that global config is redirected and fails.
	plain := exec.Command("git", "ls-remote", "--", src, "HEAD")
	plain.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+cfg, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if out, err := plain.CombinedOutput(); err == nil {
		t.Fatalf("negative control: a plain git should be redirected by the insteadOf rewrite, but it succeeded:\n%s", out)
	}

	// Our hardened resolveCommit forces GIT_CONFIG_GLOBAL=/dev/null, so the rewrite is
	// neutralized even with the poisoned config inherited from the process env.
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	if _, err := resolveCommit(context.Background(), src, "HEAD", ""); err != nil {
		t.Fatalf("hardened resolveCommit must ignore the operator global insteadOf: %v", err)
	}
}

func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// setupGit runs git WITHOUT the production hardening, for test-fixture setup only.
func setupGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initRepo makes a one-commit repo at a fresh dir and returns its path, allowing the
// local transport to serve an arbitrary in-history SHA.
func initRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	src := t.TempDir()
	setupGit(t, src, "init", "--quiet", "-b", "main")
	for name, content := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setupGit(t, src, "add", "-A")
	setupGit(t, src, "commit", "--quiet", "-m", "snapshot")
	setupGit(t, src, "config", "uploadpack.allowReachableSHA1InWant", "true")
	setupGit(t, src, "config", "uploadpack.allowAnySHA1InWant", "true")
	return src
}

func TestCloneCommitFetchesPinnedSHAAndStripsGit(t *testing.T) {
	gitOrSkip(t)
	src := initRepo(t, map[string]string{"web/Dockerfile": "FROM scratch\n"})

	sha, err := resolveCommit(context.Background(), src, "HEAD", "")
	if err != nil {
		t.Fatalf("resolveCommit: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("sha = %q, want a 40-hex commit", sha)
	}

	dir, cleanup, err := cloneCommit(context.Background(), src, sha, "")
	if err != nil {
		t.Fatalf("cloneCommit: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dir, "web", "Dockerfile")); err != nil {
		t.Errorf("cloned tree missing the file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git must be stripped from the build context (stat err = %v)", err)
	}
}

func TestCloneCommitRejectsNonSHA(t *testing.T) {
	gitOrSkip(t)
	if _, _, err := cloneCommit(context.Background(), "/tmp/whatever", "main", ""); err == nil {
		t.Fatal("cloneCommit must reject a non-40-hex ref (only a pinned commit)")
	}
}

// The adversarial host-safety gate (mirrors ADR-0010's "adversarial boundary tests,
// not happy-path"): a repo carrying an ext:: submodule and an LFS smudge filter must
// NOT execute anything on the control-plane host at clone time.
func TestCloneCommitDoesNotExecuteRepoControlledCode(t *testing.T) {
	gitOrSkip(t)
	pwned := filepath.Join(t.TempDir(), "pwned")

	// A submodule whose URL is the ext:: arbitrary-command transport, plus an
	// .gitattributes routing every file through an LFS smudge filter.
	src := initRepo(t, map[string]string{
		".gitmodules":    "[submodule \"x\"]\n\tpath = x\n\turl = ext::sh -c \"touch " + pwned + "\"\n",
		".gitattributes": "* filter=lfs diff=lfs merge=lfs\n",
		"Dockerfile":     "FROM scratch\n",
	})

	sha, err := resolveCommit(context.Background(), src, "HEAD", "")
	if err != nil {
		t.Fatalf("resolveCommit: %v", err)
	}
	dir, cleanup, err := cloneCommit(context.Background(), src, sha, "")
	if err != nil {
		t.Fatalf("cloneCommit: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("SECURITY: repo-controlled code executed on the host (ext:: submodule ran)")
	}
	// The submodule must not have been populated (--no-recurse-submodules).
	if entries, err := os.ReadDir(filepath.Join(dir, "x")); err == nil && len(entries) > 0 {
		t.Error("submodule was populated despite --no-recurse-submodules")
	}
}
