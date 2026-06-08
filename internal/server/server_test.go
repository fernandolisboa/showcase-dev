package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/config"
)

func newTestServer(play http.Handler) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(logger, config.Config{}, play, nil)
}

// The spin-up API must be reachable through New's mux at POST /api/play, and the
// method must be enforced (#10): a non-POST must not reach the play handler. The
// "POST /api/play" pattern is method-specific, and the "/" SPA catch-all sits
// below it, so a GET /api/play falls through to the SPA — it never starts a
// Session. This locks the wiring so a refactor can't silently drop the route or
// let a GET spin up a Stack.
func TestPlayRouteWiredAndMethodEnforced(t *testing.T) {
	var posts, gets int
	play := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts++
		default:
			gets++
		}
		w.WriteHeader(http.StatusOK)
	})
	h := newTestServer(play)

	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/play", nil))
	if post.Code != http.StatusOK || posts != 1 {
		t.Errorf("POST /api/play did not reach the play handler (status=%d, posts=%d)", post.Code, posts)
	}

	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/play", nil))
	if gets != 0 {
		t.Error("GET /api/play reached the play handler; the POST-only method must be enforced")
	}
}

// When no play handler is wired (play == nil), /api/play falls through to the SPA
// rather than panicking — the route is simply absent.
func TestPlayRouteAbsentWhenNil(t *testing.T) {
	h := newTestServer(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/play", nil))
	if rec.Code == http.StatusInternalServerError {
		t.Errorf("nil play should not 500, got %d", rec.Code)
	}
}
