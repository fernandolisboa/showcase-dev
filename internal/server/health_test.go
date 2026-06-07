package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/config"
)

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	health(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
}

func TestRouterServesHealthz(t *testing.T) {
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{}, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRouterServesSPAFallback(t *testing.T) {
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{}, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/client/route", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("SPA fallback status = %d, want 200", rec.Code)
	}
}
