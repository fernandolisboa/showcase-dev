package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/store"
)

// fakeStore is an in-memory SessionStore for handler tests (no database).
type fakeStore struct {
	owners   map[int64]store.Owner // by github user id
	sessions map[string]int64      // token hash -> owner id
	nextID   int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{owners: map[int64]store.Owner{}, sessions: map[string]int64{}}
}

func (f *fakeStore) UpsertOwnerByGitHubID(_ context.Context, gid int64, login string) (store.Owner, error) {
	o, ok := f.owners[gid]
	if !ok {
		f.nextID++
		o = store.Owner{ID: f.nextID, GitHubUserID: gid}
	}
	o.GitHubLogin = login
	f.owners[gid] = o
	return o, nil
}

func (f *fakeStore) CreateLoginSession(_ context.Context, hash string, ownerID int64, _ time.Time) error {
	f.sessions[hash] = ownerID
	return nil
}

func (f *fakeStore) OwnerByLoginSession(_ context.Context, hash string) (store.Owner, error) {
	oid, ok := f.sessions[hash]
	if !ok {
		return store.Owner{}, store.ErrNotFound
	}
	for _, o := range f.owners {
		if o.ID == oid {
			return o, nil
		}
	}
	return store.Owner{}, store.ErrNotFound
}

func (f *fakeStore) DeleteLoginSession(_ context.Context, hash string) error {
	delete(f.sessions, hash)
	return nil
}

// fakeProvider is a stand-in IdentityProvider — no real GitHub.
type fakeProvider struct{ user GitHubUser }

func (p fakeProvider) AuthURL(state string) string {
	return "https://github.test/login/oauth/authorize?state=" + state
}
func (p fakeProvider) Exchange(_ context.Context, code string) (GitHubUser, error) {
	if code == "bad" {
		return GitHubUser{}, errors.New("exchange failed")
	}
	return p.user, nil
}

func cookie(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginSetsStateAndRedirects(t *testing.T) {
	a := New(fakeProvider{}, newFakeStore(), false)
	rec := httptest.NewRecorder()
	a.Login(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	res := rec.Result()

	if res.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", res.StatusCode)
	}
	state := cookie(res, stateCookie)
	if state == nil || state.Value == "" {
		t.Fatal("state cookie not set")
	}
	if loc := res.Header.Get("Location"); !strings.Contains(loc, "state="+state.Value) {
		t.Errorf("redirect %q does not carry the state cookie value", loc)
	}
}

func TestLoginDisabledWithoutProvider(t *testing.T) {
	a := New(nil, newFakeStore(), false)
	rec := httptest.NewRecorder()
	a.Login(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 when sign-in unconfigured", rec.Code)
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	a := New(fakeProvider{}, newFakeStore(), false)
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state=attacker&code=x", nil)
	req.AddCookie(&http.Cookie{Name: stateCookie, Value: "legit"})
	rec := httptest.NewRecorder()
	a.Callback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on state mismatch", rec.Code)
	}
}

func TestCallbackHandlesOAuthError(t *testing.T) {
	fs := newFakeStore()
	a := New(fakeProvider{}, fs, false)
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state=ok&error=access_denied", nil)
	req.AddCookie(&http.Cookie{Name: stateCookie, Value: "ok"})
	rec := httptest.NewRecorder()
	a.Callback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on OAuth error", rec.Code)
	}
	if len(fs.sessions) != 0 {
		t.Error("a denied authorization must not create a session")
	}
}

// signIn drives the full Login→Callback flow and returns the session cookie.
func signIn(t *testing.T, a *Authenticator) *http.Cookie {
	t.Helper()
	// Login to obtain a state cookie.
	rec := httptest.NewRecorder()
	a.Login(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	state := cookie(rec.Result(), stateCookie)
	if state == nil {
		t.Fatal("no state cookie from login")
	}
	// Callback with matching state + a good code.
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state="+state.Value+"&code=ok", nil)
	req.AddCookie(state)
	rec = httptest.NewRecorder()
	a.Callback(rec, req)
	res := rec.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303", res.StatusCode)
	}
	sid := cookie(res, sessionCookie)
	if sid == nil || sid.Value == "" {
		t.Fatal("no session cookie set after callback")
	}
	return sid
}

func TestCallbackSignsInAndMeReturnsOwner(t *testing.T) {
	fs := newFakeStore()
	a := New(fakeProvider{user: GitHubUser{ID: 4242, Login: "octocat"}}, fs, false)

	sid := signIn(t, a)

	// The owner was persisted and a session created.
	if len(fs.owners) != 1 || len(fs.sessions) != 1 {
		t.Fatalf("expected 1 owner + 1 session, got %d/%d", len(fs.owners), len(fs.sessions))
	}
	// The raw cookie token must NOT be a stored key — only its hash is.
	if _, raw := fs.sessions[sid.Value]; raw {
		t.Error("raw token stored as session key; only the hash must be persisted")
	}
	if _, hashed := fs.sessions[hashToken(sid.Value)]; !hashed {
		t.Error("session not keyed by token hash")
	}

	// Me with the session cookie returns the owner.
	req := httptest.NewRequest(http.MethodGet, "/api/owner/me", nil)
	req.AddCookie(sid)
	rec := httptest.NewRecorder()
	a.Me(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Me status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "octocat") {
		t.Errorf("Me body = %q, want it to contain the login", rec.Body.String())
	}
}

func TestMeUnauthorizedWithoutSession(t *testing.T) {
	a := New(fakeProvider{}, newFakeStore(), false)
	rec := httptest.NewRecorder()
	a.Me(rec, httptest.NewRequest(http.MethodGet, "/api/owner/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	fs := newFakeStore()
	a := New(fakeProvider{user: GitHubUser{ID: 7, Login: "dev"}}, fs, false)
	sid := signIn(t, a)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(sid)
	rec := httptest.NewRecorder()
	a.Logout(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", rec.Code)
	}
	if cleared := cookie(rec.Result(), sessionCookie); cleared == nil || cleared.MaxAge >= 0 {
		t.Error("logout must clear the session cookie (MaxAge < 0)")
	}
	if len(fs.sessions) != 0 {
		t.Error("logout must delete the session row")
	}
	// The old cookie no longer authenticates.
	meReq := httptest.NewRequest(http.MethodGet, "/api/owner/me", nil)
	meReq.AddCookie(sid)
	meRec := httptest.NewRecorder()
	a.Me(meRec, meReq)
	if meRec.Code != http.StatusUnauthorized {
		t.Errorf("revoked session still authenticates: status %d", meRec.Code)
	}
}

func TestRequireOwnerGatesAndInjects(t *testing.T) {
	fs := newFakeStore()
	a := New(fakeProvider{user: GitHubUser{ID: 9, Login: "gopher"}}, fs, false)
	sid := signIn(t, a)

	var gotLogin string
	guarded := a.RequireOwner(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o, ok := OwnerFrom(r.Context()); ok {
			gotLogin = o.GitHubLogin
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Without a session → 401, handler not reached.
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/owner/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", rec.Code)
	}

	// With a session → 200 and the owner is in context.
	req := httptest.NewRequest(http.MethodGet, "/api/owner/x", nil)
	req.AddCookie(sid)
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotLogin != "gopher" {
		t.Errorf("authenticated: status %d, owner %q; want 200/gopher", rec.Code, gotLogin)
	}
}
