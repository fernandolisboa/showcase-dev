package githubapp

import (
	"fmt"
	"regexp"
	"strings"
)

// ownerRE / repoRE constrain a GitHub owner and repo to their real charsets so a
// parsed owner/repo can never smuggle a path traversal, an argv flag, a host, or a
// homoglyph past the gate. owner: alphanumeric with single internal hyphens, 1-39
// chars (GitHub's rule). repo: alphanumerics plus . _ -, 1-100 chars, never starting
// with a dash (no argv-flag shape) and never the bare "." or "..".
var (
	ownerRE = regexp.MustCompile(`^[A-Za-z0-9](?:-?[A-Za-z0-9]){0,38}$`)
	repoRE  = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9._-]{0,99}$`)
)

// ParseRepo turns an Owner-supplied Service.Repo into (owner, repo), accepting ONLY
// github.com references: "owner/repo", "github.com/owner/repo", or
// "https://github.com/owner/repo(.git)". Anything else — another host, a scheme like
// file:// or ssh, userinfo, a port, extra path segments, "..", control chars — is
// rejected. Manifest.Validate only checks Repo is non-empty, so this is the SOLE
// SSRF/abuse gate: the clone URL is rebuilt from the validated tuple (CloneURL),
// never substringed from the input, and the host is pinned to github.com.
func ParseRepo(raw string) (owner, repo string, err error) {
	s := strings.TrimSpace(raw)
	if strings.ContainsAny(s, " \t\r\n") || strings.ContainsRune(s, 0) {
		return "", "", fmt.Errorf("repo %q contains whitespace or control characters", raw)
	}
	switch low := strings.ToLower(s); {
	case strings.HasPrefix(low, "https://github.com/"):
		s = s[len("https://github.com/"):]
	case strings.HasPrefix(low, "http://github.com/"):
		s = s[len("http://github.com/"):]
	case strings.HasPrefix(low, "github.com/"):
		s = s[len("github.com/"):]
	case strings.Contains(s, "://"), strings.ContainsAny(s, "@:"):
		// A scheme, host, userinfo, or port that is not bare github.com.
		return "", "", fmt.Errorf("repo %q must be a github.com owner/repo reference", raw)
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")

	owner, repo, ok := strings.Cut(s, "/")
	if !ok || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("repo %q must be owner/repo", raw)
	}
	if !ownerRE.MatchString(owner) {
		return "", "", fmt.Errorf("invalid repo owner in %q", raw)
	}
	if repo == "." || repo == ".." || !repoRE.MatchString(repo) {
		return "", "", fmt.Errorf("invalid repo name in %q", raw)
	}
	return owner, repo, nil
}

// CloneURL is the https clone URL for a validated owner/repo, host pinned to
// github.com. Only ever call with the output of ParseRepo.
func CloneURL(owner, repo string) string {
	return "https://github.com/" + owner + "/" + repo + ".git"
}
