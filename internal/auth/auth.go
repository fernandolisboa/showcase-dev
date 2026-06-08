package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/store"
)

const (
	// sessionCookie carries the opaque login-session token; stateCookie carries the
	// short-lived anti-CSRF value for the OAuth round-trip.
	sessionCookie = "sid"
	stateCookie   = "showcase_oauth_state"
	// sessionTTL bounds how long a sign-in lasts; stateTTL bounds the OAuth dance.
	sessionTTL = 30 * 24 * time.Hour
	stateTTL   = 10 * time.Minute
)

// SessionStore is the persistence the Authenticator needs (a subset of *store.Store),
// kept as an interface so handlers are testable without a database.
type SessionStore interface {
	UpsertOwnerByGitHubID(ctx context.Context, githubUserID int64, githubLogin string) (store.Owner, error)
	CreateLoginSession(ctx context.Context, tokenHash string, ownerID int64, expiresAt time.Time) error
	OwnerByLoginSession(ctx context.Context, tokenHash string) (store.Owner, error)
	DeleteLoginSession(ctx context.Context, tokenHash string) error
}

// Authenticator wires the OAuth provider to the session store and issues/validates
// the login cookie. A nil provider means sign-in is not configured (no GitHub App
// credentials) — Login/Callback then report 501, but session validation still works.
type Authenticator struct {
	provider IdentityProvider
	store    SessionStore
	secure   bool // set the Secure cookie flag (prod, HTTPS)
}

// New builds an Authenticator. secure should be true in prod (HTTPS) so cookies
// carry the Secure flag.
func New(provider IdentityProvider, store SessionStore, secure bool) *Authenticator {
	return &Authenticator{provider: provider, store: store, secure: secure}
}

// Login starts the OAuth flow: mint anti-CSRF state, drop it in a short-lived
// cookie, and redirect to GitHub.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	if a.provider == nil {
		http.Error(w, "sign-in is not configured", http.StatusNotImplemented)
		return
	}
	state, err := newToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	a.setCookie(w, stateCookie, state, stateTTL)
	http.Redirect(w, r, a.provider.AuthURL(state), http.StatusFound)
}

// Callback completes the flow: verify state, exchange the code for the GitHub
// identity, upsert the Owner, create a session, and set the login cookie.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	if a.provider == nil {
		http.Error(w, "sign-in is not configured", http.StatusNotImplemented)
		return
	}
	// CSRF: the state in the query must match the one we put in the cookie.
	sc, err := r.Cookie(stateCookie)
	if err != nil || sc.Value == "" || sc.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid OAuth state", http.StatusBadRequest)
		return
	}
	a.clearCookie(w, stateCookie)

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	user, err := a.provider.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "github sign-in failed", http.StatusBadGateway)
		return
	}
	owner, err := a.store.UpsertOwnerByGitHubID(r.Context(), user.ID, user.Login)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	token, err := newToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := a.store.CreateLoginSession(r.Context(), hashToken(token), owner.ID, time.Now().Add(sessionTTL)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	a.setCookie(w, sessionCookie, token, sessionTTL)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Logout revokes the current session and clears the cookie. Idempotent.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = a.store.DeleteLoginSession(r.Context(), hashToken(c.Value))
	}
	a.clearCookie(w, sessionCookie)
	w.WriteHeader(http.StatusNoContent)
}

// Me returns the signed-in Owner as JSON, or 401 if there is no valid session.
func (a *Authenticator) Me(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.currentOwner(r)
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"login": owner.GitHubLogin})
}

// RequireOwner gates a handler on a valid session, injecting the Owner into the
// request context for downstream handlers (OwnerFrom). Used by mutating Owner
// endpoints in later slices.
func (a *Authenticator) RequireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, ok := a.currentOwner(r)
		if !ok {
			http.Error(w, "not signed in", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerCtxKey{}, owner)))
	})
}

// currentOwner resolves the Owner behind the request's session cookie, if any.
func (a *Authenticator) currentOwner(r *http.Request) (store.Owner, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return store.Owner{}, false
	}
	owner, err := a.store.OwnerByLoginSession(r.Context(), hashToken(c.Value))
	if err != nil {
		return store.Owner{}, false
	}
	return owner, true
}

type ownerCtxKey struct{}

// OwnerFrom returns the Owner injected by RequireOwner, if present.
func OwnerFrom(ctx context.Context) (store.Owner, bool) {
	o, ok := ctx.Value(ownerCtxKey{}).(store.Owner)
	return o, ok
}

func (a *Authenticator) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode, // Lax: the GitHub callback is a top-level GET, so the cookie rides along; Strict would drop it.
	})
}

func (a *Authenticator) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// newToken returns 32 bytes of crypto/rand entropy, hex-encoded — the opaque
// session/state token. It fails closed so a token is never minted from a degraded
// random source.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashToken is what we persist/compare: the SHA-256 of the opaque token, so the
// raw token (the bearer credential) never touches the database.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
