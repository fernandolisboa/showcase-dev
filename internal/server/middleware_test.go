package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A panicking handler must not crash the single-process control plane: the
// recoverer turns it into a 500. Because the recoverer sits inside the logger,
// the request is still logged.
func TestRecovererReturns500AndLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := requestLogger(logger, recoverer(logger, panicky))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/explode", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	logs := buf.String()
	if !strings.Contains(logs, "panic recovered") {
		t.Errorf("expected a panic-recovered log line, got:\n%s", logs)
	}
	if !strings.Contains(logs, `msg=request`) || !strings.Contains(logs, "/explode") {
		t.Errorf("expected the request to still be logged, got:\n%s", logs)
	}
}

// The recoverer must isolate a panic to its own request: a panic on one request
// does not stop the server from serving the next.
func TestRecovererIsolatesPanicAcrossRequests(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/explode" {
			panic("boom")
		}
		w.WriteHeader(http.StatusOK)
	})
	h := recoverer(logger, next)

	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/explode", nil))
	if bad.Code != http.StatusInternalServerError {
		t.Fatalf("panicking request status = %d, want 500", bad.Code)
	}

	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if ok.Code != http.StatusOK {
		t.Fatalf("subsequent request status = %d, want 200", ok.Code)
	}
}

// Concurrent panicking requests are each recovered independently (race check).
func TestRecovererConcurrentPanics(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	h := recoverer(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/explode", nil))
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
		}()
	}
	wg.Wait()
}
