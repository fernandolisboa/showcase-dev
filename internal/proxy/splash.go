package proxy

import "net/http"

// SplashHandler serves the "booting…" page Traefik routes a Session to until its
// Stack is healthy (ADR-0004). It auto-refreshes, so once the Runner promotes
// the Session to Live the next refresh lands on the real Demo — no client code.
func SplashHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(splashHTML))
	})
}

const splashHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta http-equiv="refresh" content="2">
  <title>Starting your demo…</title>
  <style>
    :root { color-scheme: light dark; }
    body { font-family: system-ui, sans-serif; display: grid; place-items: center;
           min-height: 100vh; margin: 0; }
    .box { text-align: center; }
    .spinner { width: 2.5rem; height: 2.5rem; margin: 0 auto 1rem;
               border: 3px solid currentColor; border-top-color: transparent;
               border-radius: 50%; animation: spin 0.8s linear infinite; opacity: 0.6; }
    @keyframes spin { to { transform: rotate(360deg); } }
    p { opacity: 0.7; }
  </style>
</head>
<body>
  <div class="box">
    <div class="spinner"></div>
    <h1>Starting your demo…</h1>
    <p>Booting a fresh, isolated environment. This page will load it automatically.</p>
  </div>
</body>
</html>
`
