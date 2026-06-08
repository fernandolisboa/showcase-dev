package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// health is the liveness endpoint. It reports that the process is up; it does
// not check dependencies (that is readiness, below).
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// readiness reports whether the control plane's dependencies are reachable. A nil
// check means persistence is not configured (dev) — the control plane is ready
// without a store. A failing check returns 503 so an orchestrator drains rather
// than kills the process (liveness stays green).
func readiness(check func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := check(ctx); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable", "reason": "database unreachable"})
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	}
}
