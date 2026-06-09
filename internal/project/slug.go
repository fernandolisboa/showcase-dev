package project

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// slugRE is the canonical (already-lowercased) per-Project URL slug: 2–39 chars,
// alphanumeric with single internal hyphens, no leading/trailing/double hyphen — the same
// clean shape as a showcase username, since both are URL segments (#51).
var slugRE = regexp.MustCompile(`^[a-z0-9](-?[a-z0-9]){1,38}$`)

// reservedSlugs are words that would collide with per-Portfolio sub-routes under
// /{username}/... (e.g. a future /{username}/edit), so a Project may not claim them. A
// small project-local set (the slug lives under the username, so it does NOT collide with
// the top-level product routes the username reserved-list guards). Lowercase; matched
// after canonicalization.
var reservedSlugs = map[string]bool{
	"edit": true, "new": true, "settings": true, "delete": true, "admin": true,
}

// ErrSlugInvalid is a slug validation failure (format or reserved). A slug already taken
// by another of the Owner's Projects is store.ErrProjectSlugTaken, surfaced at write time.
var ErrSlugInvalid = errors.New("invalid slug")

// CanonicalSlug validates a requested slug and returns its canonical (lowercased, trimmed)
// form, or ErrSlugInvalid. It does NOT check uniqueness — that's the store's per-owner
// UNIQUE constraint at write time. An empty input is the caller's concern (clearing a slug
// is handled before calling this).
func CanonicalSlug(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if !slugRE.MatchString(s) {
		return "", fmt.Errorf("%w: must be 2-39 chars, lowercase letters/digits/hyphens, no leading/trailing/double hyphen", ErrSlugInvalid)
	}
	if reservedSlugs[s] {
		return "", fmt.Errorf("%w: %q is reserved", ErrSlugInvalid, s)
	}
	return s, nil
}
