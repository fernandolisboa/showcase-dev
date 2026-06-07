// Package server wires the control-plane HTTP routes: liveness, the (future)
// control-plane API under /api, and the embedded React SPA for everything else.
package server

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/fernandolisboa/showcase-dev/internal/config"
	"github.com/fernandolisboa/showcase-dev/internal/web"
)

// New builds the control-plane HTTP handler. play, when non-nil, is the spin-up
// API handler mounted at POST /api/play (#10).
func New(logger *slog.Logger, _ config.Config, play http.Handler) http.Handler {
	mux := http.NewServeMux()

	// Liveness. Readiness (incl. the DB check) lands with the write-model slice.
	mux.HandleFunc("GET /healthz", health)

	// Spin-up API: a Guest starts a Session — anonymous, no account (ADR-0008).
	if play != nil {
		mux.Handle("POST /api/play", play)
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
