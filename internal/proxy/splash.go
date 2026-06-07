package proxy

import "net/http"

// SplashHandler serves the "booting…" page Traefik routes a Session to until its
// Stack is healthy (ADR-0004). It auto-refreshes, so once the Runner promotes
// the Session to Live the next refresh lands on the real Demo — no client code.
func SplashHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePage(w, splashHTML, true)
	})
}

// StatusHandler serves the per-Session status page for every non-live state:
// booting (auto-refreshes onto the live Demo) and the terminal failure pages
// "failed to start" / "crashed" (no refresh — the Stack is gone). It takes a
// narrow lookup that yields only the state for a host, never backends, so this
// Demo-facing listener stays structurally unable to read the backend map
// (ADR-0004). An unknown host renders an "ended" page rather than a bare 404.
func StatusHandler(lookup func(host string) (State, bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, ok := lookup(r.Host)
		switch {
		case !ok:
			writePage(w, endedHTML, false)
		case state == Failed:
			writePage(w, failedHTML, false)
		case state == Crashed:
			writePage(w, crashedHTML, false)
		default: // Booting (a Live host routes to its backend, never here)
			writePage(w, splashHTML, true)
		}
	})
}

// writePage renders a status page. refresh adds the 2s auto-refresh hint used
// while booting; terminal pages omit it so they don't reload forever.
func writePage(w http.ResponseWriter, html string, refresh bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if refresh {
		w.Header().Set("Retry-After", "2")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
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

// terminalPage renders a static failure/ended page: no spinner, no auto-refresh,
// since the Stack is gone and the route will be removed shortly.
func terminalPage(title, heading, message string) string {
	return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + title + `</title>
  <style>
    :root { color-scheme: light dark; }
    body { font-family: system-ui, sans-serif; display: grid; place-items: center;
           min-height: 100vh; margin: 0; }
    .box { text-align: center; max-width: 32rem; padding: 0 1rem; }
    p { opacity: 0.7; }
  </style>
</head>
<body>
  <div class="box">
    <h1>` + heading + `</h1>
    <p>` + message + `</p>
  </div>
</body>
</html>
`
}

var (
	// failedHTML is shown when a Session's Stack never became healthy (won't start).
	failedHTML = terminalPage("Demo failed to start",
		"This demo failed to start",
		"Something went wrong while booting this demo. Each play starts a fresh environment — head back and try again.")
	// crashedHTML is shown when a live Session stopped being healthy and didn't recover.
	crashedHTML = terminalPage("Demo crashed",
		"This demo crashed",
		"The demo stopped responding and couldn't recover. Each play starts a fresh environment — head back and try again.")
	// endedHTML is shown for a host with no active Session (already torn down).
	endedHTML = terminalPage("Demo session ended",
		"This demo session has ended",
		"Demo sessions are temporary. Head back and press play to start a fresh one.")
)
