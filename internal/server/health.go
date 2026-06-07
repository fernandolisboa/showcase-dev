package server

import (
	"encoding/json"
	"net/http"
)

// health is the liveness endpoint. It reports that the process is up; it does
// not check dependencies (that is readiness, added with the write model).
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
