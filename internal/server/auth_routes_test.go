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

// When an Authenticator is wired, the Owner routes are mounted on the public mux:
// /api/owner/me returns 401 (not the SPA 200 fallback) for an anonymous request,
// proving the route exists. With a nil provider, /login reports 501 (configured-off),
// again proving it's mounted rather than falling through to the SPA.
func TestAuthRoutesMountedWhenAuthenticatorPresent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authn := auth.New(nil, noopStore{}, false) // nil provider = sign-in unconfigured
	h := New(logger, config.Config{}, nil, nil, authn)

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
	h := New(logger, config.Config{}, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/owner/me", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/owner/me = %d, want SPA fallback 200 when sign-in disabled", rec.Code)
	}
}
