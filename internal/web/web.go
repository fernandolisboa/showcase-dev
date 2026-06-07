// Package web serves the embedded React single-page app. Per ADR-0009 the Vite
// build is compiled into the binary (go:embed) so a deploy is one static
// artifact. Vite writes its build into ./dist (see web/vite.config.ts). Before
// the first `make web-build`, dist holds only a placeholder.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Handler serves the SPA: a built asset when the path matches one, otherwise
// index.html so client-side routes resolve. With no build present it serves a
// minimal boot page (the API, incl. /healthz, works regardless).
func Handler() http.Handler {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; this cannot fail.
	}
	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asset := strings.TrimPrefix(r.URL.Path, "/"); asset != "" {
			if f, err := dist.Open(asset); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		if index, err := fs.ReadFile(dist, "index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholder))
	})
}

const placeholder = `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
	`<title>Showcase</title></head><body style="font-family:system-ui;margin:3rem">` +
	`<h1>Showcase control plane</h1>` +
	`<p>UI not built yet — run <code>make web-build</code>. Health: <code>/healthz</code>.</p>` +
	`</body></html>`
