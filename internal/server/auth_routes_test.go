package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/auth"
	"github.com/fernandolisboa/showcase-dev/internal/config"
	"github.com/fernandolisboa/showcase-dev/internal/project"
	"github.com/fernandolisboa/showcase-dev/internal/store"
)

// noopStore satisfies auth.SessionStore; the wiring test never reaches a method
// that matters (no session cookie), so the bodies are trivial.
type noopStore struct{}

func (noopStore) UpsertOwnerByGitHubID(context.Context, int64, string) (store.Owner, error) {
	return store.Owner{}, nil
}
func (noopStore) CreateLoginSession(context.Context, string, int64, time.Time) error { return nil }
func (noopStore) OwnerByLoginSession(context.Context, string) (store.Owner, error) {
	return store.Owner{}, store.ErrNotFound
}
func (noopStore) DeleteLoginSession(context.Context, string) error { return nil }
func (noopStore) SetUsername(context.Context, int64, string) error { return nil }

// When an Authenticator is wired, the Owner routes are mounted on the public mux:
// /api/owner/me returns 401 (not the SPA 200 fallback) for an anonymous request,
// proving the route exists. With a nil provider, /login reports 501 (configured-off),
// again proving it's mounted rather than falling through to the SPA.
func TestAuthRoutesMountedWhenAuthenticatorPresent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authn := auth.New(nil, noopStore{}, false) // nil provider = sign-in unconfigured
	h := New(logger, config.Config{}, nil, nil, authn, nil)

	me := httptest.NewRecorder()
	h.ServeHTTP(me, httptest.NewRequest(http.MethodGet, "/api/owner/me", nil))
	if me.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/owner/me = %d, want 401 (route mounted, anonymous)", me.Code)
	}

	login := httptest.NewRecorder()
	h.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/login", nil))
	if login.Code != http.StatusNotImplemented {
		t.Errorf("GET /login = %d, want 501 (route mounted, provider unconfigured)", login.Code)
	}
}

// Without an Authenticator (no DB, dev), the same paths fall through to the SPA
// (200) rather than erroring — sign-in is simply absent.
func TestAuthRoutesAbsentWithoutAuthenticator(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(logger, config.Config{}, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/owner/me", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/owner/me = %d, want SPA fallback 200 when sign-in disabled", rec.Code)
	}
}

// noopProjectStore satisfies project.Store; the route-mounting test never reaches a
// method (RequireOwner 401s the anonymous request first), so the bodies are trivial.
type noopProjectStore struct{}

func (noopProjectStore) CreateProject(context.Context, int64, string, []byte) (store.Project, error) {
	return store.Project{}, nil
}
func (noopProjectStore) UpdateProject(context.Context, int64, string, string, []byte) (store.Project, error) {
	return store.Project{}, store.ErrProjectNotFound
}
func (noopProjectStore) GetOwnerProject(context.Context, int64, string) (store.Project, error) {
	return store.Project{}, store.ErrProjectNotFound
}
func (noopProjectStore) ListProjectsByOwner(context.Context, int64) ([]store.Project, error) {
	return nil, nil
}
func (noopProjectStore) StartBuild(context.Context, int64, string) (store.Project, error) {
	return store.Project{}, store.ErrProjectNotFound
}
func (noopProjectStore) MarkBuildFailed(context.Context, int64, string, string) error { return nil }
func (noopProjectStore) PublishProject(context.Context, int64, string, string, []byte) error {
	return nil
}
func (noopProjectStore) ReclaimStuckBuilds(context.Context, time.Duration, time.Duration) (int64, error) {
	return 0, nil
}
func (noopProjectStore) OwnerByUsername(context.Context, string) (store.Owner, error) {
	return store.Owner{}, store.ErrNotFound
}
func (noopProjectStore) ListPublishedProjectsByUsername(context.Context, string) ([]store.Project, error) {
	return nil, nil
}

// With both an Authenticator and project Handlers wired, the Owner project routes
// are mounted and gated: an anonymous request gets 401 (RequireOwner), not the SPA
// 200 fallback — proving the route exists behind the gate.
func TestProjectRoutesMountedWhenPresent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authn := auth.New(nil, noopStore{}, false)
	projects := project.NewHandlers(noopProjectStore{}, nil)
	h := New(logger, config.Config{}, nil, nil, authn, projects)

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPost, "/api/owner/projects"},
		{http.MethodGet, "/api/owner/projects"},
		{http.MethodGet, "/api/owner/projects/some-id"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401 (route mounted, anonymous)", tc.method, tc.path, rec.Code)
		}
	}
}

// The Portfolio route is PUBLIC: an anonymous request reaches the handler (here the
// noop store has no such Owner, so 404) rather than being gated (401) or falling
// through to the SPA (200) — proving it's mounted and ungated.
func TestPortfolioRouteIsPublicAndMounted(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authn := auth.New(nil, noopStore{}, false)
	projects := project.NewHandlers(noopProjectStore{}, nil)
	h := New(logger, config.Config{}, nil, nil, authn, projects)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/portfolio/alice", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/portfolio/alice = %d, want 404 (public route mounted, unknown user)", rec.Code)
	}
}

// Without project Handlers, the project routes fall through to the SPA (200) — the
// feature is simply absent (dev without a DB).
func TestProjectRoutesAbsentWithoutHandlers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authn := auth.New(nil, noopStore{}, false)
	h := New(logger, config.Config{}, nil, nil, authn, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/owner/projects", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/owner/projects = %d, want SPA fallback 200 when projects disabled", rec.Code)
	}
}
