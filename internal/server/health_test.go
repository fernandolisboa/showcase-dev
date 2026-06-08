package server

import (
	"context"
	"encoding/json"
	"errors"
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
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{}, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRouterServesSPAFallback(t *testing.T) {
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{}, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/client/route", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("SPA fallback status = %d, want 200", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	cases := map[string]struct {
		check    func(context.Context) error
		wantCode int
	}{
		"no store (dev) is ready": {nil, http.StatusOK},
		"db reachable is ready":   {func(context.Context) error { return nil }, http.StatusOK},
		"db down is unavailable":  {func(context.Context) error { return errors.New("down") }, http.StatusServiceUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{}, nil, tc.check)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}
