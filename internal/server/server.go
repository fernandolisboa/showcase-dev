// Package server wires the control-plane HTTP routes: liveness, the (future)
// control-plane API under /api, and the embedded React SPA for everything else.
package server

import (
	"log/slog"
	"net/http"

	"github.com/fernandolisboa/showcase-dev/internal/config"
	"github.com/fernandolisboa/showcase-dev/internal/web"
)

// New builds the control-plane HTTP handler.
func New(logger *slog.Logger, _ config.Config) http.Handler {
	mux := http.NewServeMux()

	// Liveness. Readiness (incl. the DB check) lands with the write-model slice.
	mux.HandleFunc("GET /healthz", health)

	// The control-plane API surface mounts under /api in later slices.

	// Everything else is the embedded React SPA.
	mux.Handle("/", web.Handler())

	return requestLogger(logger, mux)
}

// requestLogger logs one line per request after it is served.
func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		logger.Info("request", "method", r.Method, "path", r.URL.Path)
	})
}
