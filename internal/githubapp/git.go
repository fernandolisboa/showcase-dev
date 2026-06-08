package githubapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// shaRE matches a full 40-hex git commit SHA.
var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// hardeningArgs are the -c flags applied to EVERY host-side git invocation so a
// malicious Owner repo cannot execute code on the control-plane host at CLONE time —
// the clone runs OUTSIDE the ADR-0010 build sandbox, one step before the sandboxed
// build. They block the ext:: arbitrary-command transport, disable hooks, neuter LFS
// smudge/process filters (which run subprocesses from repo data), and turn off
// submodule recursion. Combined with the --no-recurse-submodules flags below, no
// git feature ever runs a subprocess from attacker-controlled repo content.
func hardeningArgs() []string {
	return []string{
		"-c", "protocol.ext.allow=never",
		"-c", "core.hooksPath=/dev/null",
		"-c", "credential.helper=",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.process=",
		"-c", "filter.lfs.required=false",
		"-c", "transfer.fsckObjects=true",
		"-c", "submodule.recurse=false",
	}
}

// hardeningEnv is the locked-down environment for host-side git: no terminal prompt
// or askpass (so a private repo can never block waiting for credentials), no system
// or global config (so an operator ~/.gitconfig insteadOf/hooks cannot subvert or
// redirect the credentialed clone), and LFS smudge skipped. When token is non-empty
// the auth header is injected via GIT_CONFIG_* — NOT argv (so it never appears in
// /proc/<pid>/cmdline) and NOT the URL (so it never lands in .git/config).
func hardeningEnv(token string) []string {
	env := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"GIT_LFS_SKIP_SMUDGE=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"HOME=/nonexistent",
		"PATH=" + os.Getenv("PATH"),
	}
	if token != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer "+token,
		)
	}
	return env
}

// redact removes the token from text before it can reach a log or an Owner-facing
// error. Defense in depth: the token is in env (not argv/output) by design, but a
// future git version echoing a header must never leak it.
func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "REDACTED")
}

// runGit runs git with the hardening flags + locked-down env. On error it wraps the
// (redacted) output so a caller can surface a build/clone failure without leaking the
// token.
func runGit(ctx context.Context, token string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append(hardeningArgs(), args...)...)
	cmd.Env = hardeningEnv(token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w\n%s", args[0], err, redact(string(out), token))
	}
	return out, nil
}

// ResolveCommit returns the full SHA that ref ("HEAD" if empty) points at in
// owner/repo, via git ls-remote. token may be empty for a public repo.
func ResolveCommit(ctx context.Context, owner, repo, ref, token string) (string, error) {
	return resolveCommit(ctx, CloneURL(owner, repo), ref, token)
}

func resolveCommit(ctx context.Context, remote, ref, token string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	out, err := runGit(ctx, token, "ls-remote", "--", remote, ref)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", fmt.Errorf("ls-remote: ref %q not found", ref)
	}
	sha := strings.Fields(line)[0]
	if !shaRE.MatchString(sha) {
		return "", fmt.Errorf("ls-remote: unexpected output")
	}
	return sha, nil
}

// CloneCommit shallow-fetches owner/repo at EXACTLY sha into a fresh temp dir, then
// strips .git so the resulting tree carries no credentials or git metadata into the
// read-only-mounted build context. Submodules, hooks, and LFS smudge are disabled
// (see hardeningArgs/hardeningEnv). The caller MUST run cleanup. sha must be a full
// 40-hex commit (pin it via ResolveCommit) so a moved ref cannot swap the tree.
func CloneCommit(ctx context.Context, owner, repo, sha, token string) (dir string, cleanup func(), err error) {
	return cloneCommit(ctx, CloneURL(owner, repo), sha, token)
}

func cloneCommit(ctx context.Context, remote, sha, token string) (dir string, cleanup func(), err error) {
	if !shaRE.MatchString(sha) {
		return "", nil, fmt.Errorf("clone: %q is not a full commit sha", sha)
	}
	parent, err := os.MkdirTemp("", "showcase-clone-*")
	if err != nil {
		return "", nil, fmt.Errorf("clone temp dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(parent) }
	work := filepath.Join(parent, "repo")

	// init + fetch the exact object + checkout: pins the tree to sha with no ref
	// ambiguity, and never touches submodules.
	steps := [][]string{
		{"init", "--quiet", work},
		{"-C", work, "remote", "add", "origin", remote},
		{"-C", work, "fetch", "--quiet", "--depth", "1", "--no-tags", "--no-recurse-submodules", "origin", sha},
		{"-C", work, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--no-recurse-submodules", sha},
	}
	for _, s := range steps {
		if _, e := runGit(ctx, token, s...); e != nil {
			cleanup()
			return "", nil, e
		}
	}

	// The token never entered .git (we used an auth header, not the URL), but strip
	// .git regardless so no git metadata can reach the build context (ADR-0008: a
	// platform secret never enters a demo/build).
	if e := os.RemoveAll(filepath.Join(work, ".git")); e != nil {
		cleanup()
		return "", nil, fmt.Errorf("strip .git: %w", e)
	}
	return work, cleanup, nil
}
