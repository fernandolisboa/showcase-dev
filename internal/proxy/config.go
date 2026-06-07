package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// statusService is the shared Traefik service every non-live Session routes to —
// the control plane's status backend, which serves the booting page and the
// terminal "failed to start" / "crashed" pages (#13).
const statusService = "status"

// ConfigHandler serves Traefik's dynamic configuration (HTTP provider) built
// from the Registry. Traefik polls it; the response is the source of truth for
// per-Session routing, which keeps the map in the control plane (ADR-0004/0009).
type ConfigHandler struct {
	reg        *Registry
	bootingURL string
	entryPoint string
}

// NewConfigHandler builds the handler. bootingURL is the control-plane splash
// backend Traefik routes booting Sessions to; entryPoint (e.g. "web") may be ""
// to let Traefik use its defaults.
func NewConfigHandler(reg *Registry, bootingURL, entryPoint string) *ConfigHandler {
	return &ConfigHandler{reg: reg, bootingURL: bootingURL, entryPoint: entryPoint}
}

func (h *ConfigHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.build())
}

// ConfigListenerHandler is the control-plane-only surface that serves Traefik's
// dynamic config (HTTP provider) at GET /traefik. Traefik polls it directly over
// the host gateway; no Session is ever routed to this listener (a booting Session
// goes to the splash listener instead). It must additionally bind an internal
// interface only — a Demo must never reach the backend map (ADR-0004); see
// cmd/controlplane's InternalHost.
//
// Any non-/traefik path still returns the splash rather than a 404, so this
// listener's only extra surface beyond /traefik is the harmless booting page.
func ConfigListenerHandler(reg *Registry, bootingURL, entryPoint string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /traefik", NewConfigHandler(reg, bootingURL, entryPoint))
	mux.Handle("/", SplashHandler())
	return mux
}

// BootingListenerHandler is the status surface a non-live Session is forwarded to
// (bootingURL points here): it serves the booting page and the terminal failure
// pages. It deliberately has NO /traefik route: Traefik forwards the Guest's full
// request path, so a compromised Demo that presents a Session's Host and asks for
// /traefik gets a status page, not the control-plane backend map (ADR-0004).
// Keeping it on its own listener — never the one that serves /traefik — is the
// structural wall; lookup yields only the state, never backends.
func BootingListenerHandler(lookup func(host string) (State, bool)) http.Handler {
	return StatusHandler(lookup)
}

// build renders the dynamic config for all active Sessions.
func (h *ConfigHandler) build() dynamicConfig {
	routers := map[string]router{}
	services := map[string]service{}
	anyStatus := false

	for _, rt := range h.reg.snapshot() {
		hostRule := fmt.Sprintf("Host(`%s`)", rt.Host)

		if rt.State != Live {
			// Booting, Failed, Crashed: whole subdomain → the control-plane status
			// page (booting spinner, then either the live Demo or a failure page).
			routers["s-"+rt.SessionID] = h.router(hostRule, statusService, 1)
			anyStatus = true
			continue
		}

		// Live: each API prefix wins over the catch-all UI via higher priority.
		for i, api := range rt.APIs {
			name := fmt.Sprintf("s-%s-api-%d", rt.SessionID, i)
			rule := fmt.Sprintf("%s && %s", hostRule, apiPathRule(api.PathPrefix))
			routers[name] = h.router(rule, name, 100)
			services[name] = serviceTo(api.URL)
		}
		uiName := "s-" + rt.SessionID + "-ui"
		routers[uiName] = h.router(hostRule, uiName, 1)
		services[uiName] = serviceTo(rt.UI.URL)
	}

	if anyStatus {
		services[statusService] = serviceTo(h.bootingURL)
	}

	return dynamicConfig{HTTP: httpConfig{Routers: routers, Services: services}}
}

func (h *ConfigHandler) router(rule, svc string, priority int) router {
	r := router{Rule: rule, Service: svc, Priority: priority}
	if h.entryPoint != "" {
		r.EntryPoints = []string{h.entryPoint}
	}
	return r
}

func serviceTo(url string) service {
	return service{LoadBalancer: loadBalancer{Servers: []server{{URL: url}}}}
}

// apiPathRule matches an API mount on a path-segment boundary, not a raw string
// prefix. Traefik's PathPrefix(`/api`) also matches `/apidocs`/`/apiary`, which
// would wrongly swallow an Owner UI route; Path(`/api`) || PathPrefix(`/api/`)
// matches only the `/api` segment and its subpaths. The prefix is trimmed of a
// trailing slash first so a declared `/api/` and `/api` produce the same rule.
func apiPathRule(prefix string) string {
	p := strings.TrimRight(prefix, "/")
	return fmt.Sprintf("(Path(`%s`) || PathPrefix(`%s/`))", p, p)
}

// --- Traefik dynamic-config wire format ---

type dynamicConfig struct {
	HTTP httpConfig `json:"http"`
}

type httpConfig struct {
	Routers  map[string]router  `json:"routers"`
	Services map[string]service `json:"services"`
}

type router struct {
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	Priority    int      `json:"priority,omitempty"`
	EntryPoints []string `json:"entryPoints,omitempty"`
}

type service struct {
	LoadBalancer loadBalancer `json:"loadBalancer"`
}

type loadBalancer struct {
	Servers []server `json:"servers"`
}

type server struct {
	URL string `json:"url"`
}
