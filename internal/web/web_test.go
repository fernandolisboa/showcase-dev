package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// fixture mirrors a real Vite build: an index.html, a top-level file, and an
// assets/ directory holding a build artifact.
func fixture() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>app</title>")},
		"favicon.ico":      {Data: []byte("icon-bytes")},
		"assets/app.js":    {Data: []byte("console.log(1)")},
		"assets/style.css": {Data: []byte("body{}")},
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// A directory path must never yield an autoindex listing on this guest-facing
// surface. It falls through to index.html instead.
func TestDirectoryPathDoesNotProduceListing(t *testing.T) {
	h := handlerFor(fixture())

	for _, path := range []string{"/assets", "/assets/"} {
		rec := get(t, h, path)

		if rec.Code == http.StatusMovedPermanently || rec.Code == http.StatusFound {
			t.Errorf("%s: status = %d, want no redirect to a listing", path, rec.Code)
		}
		body := rec.Body.String()
		// http.FileServer's autoindex emits anchor links to the directory entries.
		if strings.Contains(body, "app.js") || strings.Contains(body, "<a href=") {
			t.Errorf("%s: body looks like a directory listing:\n%s", path, body)
		}
		// It should serve the SPA shell instead.
		if !strings.Contains(body, "<title>app</title>") {
			t.Errorf("%s: expected index.html fallback, got:\n%s", path, body)
		}
	}
}

// A regular file under a directory is still served verbatim.
func TestRegularFileIsServed(t *testing.T) {
	h := handlerFor(fixture())

	rec := get(t, h, "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "console.log(1)" {
		t.Errorf("body = %q, want the file contents", got)
	}
}

// Unknown client-side routes resolve to index.html (SPA fallback).
func TestUnknownRouteFallsBackToIndex(t *testing.T) {
	h := handlerFor(fixture())

	rec := get(t, h, "/some/client/route")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<title>app</title>") {
		t.Errorf("expected index.html fallback, got:\n%s", rec.Body.String())
	}
}
