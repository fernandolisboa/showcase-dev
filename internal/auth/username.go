package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/fernandolisboa/showcase-dev/internal/store"
)

// usernameRE is the canonical (already-lowercased) showcase username: 2–39 chars,
// alphanumeric with single internal hyphens, no leading/trailing/double hyphen —
// clean URL segments for showcase.dev/{username} (ADR-0005), GitHub-handle-like.
var usernameRE = regexp.MustCompile(`^[a-z0-9](-?[a-z0-9]){1,38}$`)

// reservedUsernames are names that share the path namespace with product routes
// and so cannot be claimed (ADR-0005). Lowercase; matching is case-insensitive
// because usernames are canonicalized to lowercase before the check. Keep in sync
// with the routes the control plane serves.
var reservedUsernames = map[string]bool{
	"about": true, "admin": true, "api": true, "assets": true, "auth": true,
	"blog": true, "contact": true, "dashboard": true, "demo": true, "docs": true,
	"explore": true, "healthz": true, "help": true,
	"login": true, "logout": true, "me": true, "new": true, "owner": true,
	"pricing": true, "privacy": true, "readyz": true,
	"run": true, "search": true, "settings": true, "signin": true, "signup": true,
	"static": true, "status": true, "support": true, "terms": true, "www": true,
}

// ErrUsernameInvalid is a validation failure (format or reserved). ErrUsernameTaken
// is re-exported from store for callers that switch on it.
var (
	ErrUsernameInvalid = errors.New("invalid username")
	ErrUsernameTaken   = store.ErrUsernameTaken
)

// CanonicalUsername validates a requested username and returns its canonical
// (lowercased, trimmed) form, or ErrUsernameInvalid. It does NOT check
// uniqueness — that's the store's UNIQUE constraint at write time.
func CanonicalUsername(raw string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(raw))
	if !usernameRE.MatchString(u) {
		return "", fmt.Errorf("%w: must be 2-39 chars, lowercase letters/digits/hyphens, no leading/trailing/double hyphen", ErrUsernameInvalid)
	}
	if reservedUsernames[u] {
		return "", fmt.Errorf("%w: %q is reserved", ErrUsernameInvalid, u)
	}
	return u, nil
}

// ClaimUsername sets the signed-in Owner's public username. It must be mounted
// behind RequireOwner (it reads the Owner from context). Body: {"username": "..."}.
// 400 on invalid/reserved, 409 if already taken by another Owner.
func (a *Authenticator) ClaimUsername(w http.ResponseWriter, r *http.Request) {
	owner, ok := OwnerFrom(r.Context())
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	// Set-once for the MVP: a username IS the Portfolio URL, so silently changing
	// it would break existing links and immediately free the old name for
	// squatting/impersonation. Changing a username is a deliberate future flow.
	if owner.Username != "" {
		http.Error(w, "username already set", http.StatusConflict)
		return
	}
	var body struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	username, err := CanonicalUsername(body.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch err := a.store.SetUsername(r.Context(), owner.ID, username); {
	case errors.Is(err, store.ErrUsernameTaken):
		http.Error(w, "username already taken", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"username": username})
}
