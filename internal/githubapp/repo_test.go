package githubapp

import "testing"

func TestParseRepoAccepts(t *testing.T) {
	cases := map[string]struct{ owner, repo string }{
		"owner/repo":                         {"owner", "repo"},
		"github.com/owner/repo":              {"owner", "repo"},
		"https://github.com/owner/repo":      {"owner", "repo"},
		"https://github.com/owner/repo.git":  {"owner", "repo"},
		"https://github.com/owner/repo/":     {"owner", "repo"},
		"HTTPS://GitHub.com/Fernando/My-App": {"Fernando", "My-App"},
		"o/r.with.dots":                      {"o", "r.with.dots"},
		"a-b-c/d_e-f.g":                      {"a-b-c", "d_e-f.g"},
	}
	for in, want := range cases {
		o, r, err := ParseRepo(in)
		if err != nil {
			t.Errorf("ParseRepo(%q) error: %v", in, err)
			continue
		}
		if o != want.owner || r != want.repo {
			t.Errorf("ParseRepo(%q) = %q/%q, want %q/%q", in, o, r, want.owner, want.repo)
		}
	}
	if got := CloneURL("owner", "repo"); got != "https://github.com/owner/repo.git" {
		t.Errorf("CloneURL = %q", got)
	}
}

func TestParseRepoRejects(t *testing.T) {
	bad := []string{
		"", "owner", "owner/repo/extra", "/repo", "owner/",
		"file:///etc/passwd",
		"ssh://git@github.com/o/r",
		"git@github.com:o/r.git",
		"https://gitlab.com/o/r",
		"https://github.com.evil.com/o/r", // host lookalike
		"https://github.com@evil.com/o/r", // userinfo trick
		"github.com/o/r:443",
		"https://github.com:443/o/r",
		"https://user:pass@github.com/o/r",
		"../../etc/passwd",
		"o/../r",
		"o/..",
		"o/.",
		"-flag/repo",               // leading-dash owner (argv flag)
		"owner/--upload-pack=evil", // flag injection in repo
		"o/r r",                    // whitespace
		"o/\tr",                    // tab
		"evil.com/o/r",             // bare host (owner cannot contain a dot)
		"２.com/o/r",                // fullwidth/unicode
	}
	for _, in := range bad {
		if o, r, err := ParseRepo(in); err == nil {
			t.Errorf("ParseRepo(%q) = %q/%q, want error", in, o, r)
		}
	}
}
