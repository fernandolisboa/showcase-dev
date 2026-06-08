// Package server wires the control-plane HTTP routes: liveness, the (future)
// control-plane API under /api, and the embedded React SPA for everything else.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/fernandolisboa/showcase-dev/internal/auth"
	"github.com/fernandolisboa/showcase-dev/internal/config"
	"github.com/fernandolisboa/showcase-dev/internal/web"
)

// New builds the control-plane HTTP handler. play, when non-nil, is the spin-up
// API handler mounted at POST /api/play (#10). ready, when non-nil, is the
// readiness check (a DB ping) behind GET /readyz; nil means persistence is not
// configured (dev), so the control plane reports ready without a backing store.
// authn, when non-nil, mounts the Owner sign-in routes (ADR-0008); nil means
// persistence/sign-in is disabled (dev without a DB).
func New(logger *slog.Logger, _ config.Config, play http.Handler, ready func(context.Context) error, authn *auth.Authenticator) http.Handler {
	mux := http.NewServeMux()

	// Liveness: the process is up. Independent of the DB so a transient DB blip
	// doesn't get the process killed by a liveness probe.
	mux.HandleFunc("GET /healthz", health)

	// Readiness: the process can serve traffic that needs its dependencies — here,
	// the database. Distinct from liveness so an orchestrator drains (not kills) a
	// control plane whose DB is briefly unreachable.
	mux.HandleFunc("GET /readyz", readiness(ready))

	// Spin-up API: a Guest starts a Session — anonymous, no account (ADR-0008).
	if play != nil {
		mux.Handle("POST /api/play", play)
	}

	// Owner sign-in (ADR-0008). All on the public listener; /api/play stays
	// ungated (Guests are anonymous). Login/Callback report 501 if no GitHub App
	// credentials are configured.
	if authn != nil {
		mux.HandleFunc("GET /login", authn.Login)
		mux.HandleFunc("GET /auth/github/callback", authn.Callback)
		mux.HandleFunc("POST /logout", authn.Logout)
		mux.HandleFunc("GET /api/owner/me", authn.Me)
	}

	// Everything else is the embedded React SPA.
	mux.Handle("/", web.Handler())

	// recoverer sits inside requestLogger so a panicking request is turned into a
	// 500 before control returns to the logger, and is therefore still logged.
	return requestLogger(logger, recoverer(logger, mux))
}

// requestLogger logs one line per request after it is served.
func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		logger.Info("request", "method", r.Method, "path", r.URL.Path)
	})
}

// recoverer turns a handler panic into a 500 so a single panicking request never
// crashes the single-process control plane (one process per VM, ADR-0009).
func recoverer(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered",
					"method", r.Method, "path", r.URL.Path,
					"panic", rec, "stack", string(debug.Stack()))
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
