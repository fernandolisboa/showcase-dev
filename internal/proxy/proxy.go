// Package proxy integrates the control plane with Traefik (ADR-0004/0009). It
// keeps a registry of active Sessions and serves Traefik's dynamic configuration
// over the HTTP provider, so each Session is reachable at its own unguessable
// subdomain `s-<id>.run.<domain>` with same-origin `/api`, and a Guest sees a
// booting page until the Stack's healthcheck passes, then the live Demo. The
// Runner keeps this registry current as it provisions and tears down Sessions.
package proxy

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"net"
	"strings"
	"sync"
)

// State is a Session route's lifecycle state.
type State int

const (
	// Booting routes the whole subdomain to the control-plane booting page.
	Booting State = iota
	// Live routes "/" to the UI and each declared prefix to its API.
	Live
	// Failed is a Session whose Stack never came up (no healthcheck within the boot
	// timeout). The Stack is gone; the route lingers, routing to the control-plane
	// "failed to start" page until the reaper removes it (ADR-0006, #13).
	Failed
	// Crashed is a live Session whose Stack stopped being healthy and the restart
	// policy didn't recover it. Like Failed: Stack gone, route lingers on the
	// "demo crashed" page until reaped (ADR-0006, #13).
	Crashed
)

// Terminal reports whether a state is a failure end-state (Stack destroyed, route
// lingering on a failure page).
func (s State) Terminal() bool { return s == Failed || s == Crashed }

// Backend is a Stack service reachable on the per-Session network.
type Backend struct {
	// PathPrefix is where an API mounts (e.g. "/api"); empty for the UI.
	PathPrefix string
	// URL is the in-network address, e.g. "http://web:8080".
	URL string
}

// Route is one Session's routing: its host, state, UI backend, and API backends.
type Route struct {
	SessionID string
	Host      string
	State     State
	UI        Backend
	APIs      []Backend
}

// Registry is the thread-safe set of active Session routes. The Runner mutates
// it; the Traefik HTTP-provider Handler reads it.
type Registry struct {
	mu     sync.RWMutex
	domain string
	routes map[string]Route
}

// NewRegistry builds a Registry whose subdomains live under run.<domain>.
func NewRegistry(domain string) *Registry {
	return &Registry{domain: domain, routes: map[string]Route{}}
}

// Host returns the Session's fully-qualified demo host.
func (r *Registry) Host(sessionID string) string {
	return fmt.Sprintf("s-%s.run.%s", sessionID, r.domain)
}

// Add registers a Session as Booting with its eventual backends.
func (r *Registry) Add(sessionID string, ui Backend, apis []Backend) Route {
	route := Route{
		SessionID: sessionID,
		Host:      r.Host(sessionID),
		State:     Booting,
		UI:        ui,
		APIs:      apis,
	}
	r.mu.Lock()
	r.routes[sessionID] = route
	r.mu.Unlock()
	return route
}

// Promote flips a Session from Booting to Live once its Stack is healthy.
// It reports whether the Session was present.
func (r *Registry) Promote(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	route, ok := r.routes[sessionID]
	if !ok {
		return false
	}
	route.State = Live
	r.routes[sessionID] = route
	return true
}

// Remove drops a Session's route (teardown).
func (r *Registry) Remove(sessionID string) {
	r.mu.Lock()
	delete(r.routes, sessionID)
	r.mu.Unlock()
}

// Fail moves a Session's route into a terminal failure state (Failed or Crashed),
// clearing its backends — the Stack is gone, but the route lingers so the Guest's
// auto-refreshing page lands on the failure message instead of a dead 404 (#13).
// It (re)creates the route if absent, since a won't-start teardown removes it
// first; the reaper removes the lingering route after FailureLinger.
func (r *Registry) Fail(sessionID string, state State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	route, ok := r.routes[sessionID]
	if !ok {
		route = Route{SessionID: sessionID, Host: r.Host(sessionID)}
	}
	route.State = state
	route.UI = Backend{}
	route.APIs = nil
	r.routes[sessionID] = route
}

// StateByHost returns the state of the Session served at host (e.g.
// "s-<id>.run.<domain>"), for the status page to pick which message to render. It
// returns only the state — never backends — so the Demo-facing status listener
// stays structurally unable to read the backend map (ADR-0004).
func (r *Registry) StateByHost(host string) (State, bool) {
	id := r.sessionIDFromHost(host)
	if id == "" {
		return 0, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.routes[id]
	return route.State, ok
}

// sessionIDFromHost extracts the Session id from a demo host, or "" if host isn't
// a demo host under this registry's domain.
func (r *Registry) sessionIDFromHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	rest, ok := strings.CutSuffix(host, ".run."+r.domain)
	if !ok {
		return ""
	}
	id, ok := strings.CutPrefix(rest, "s-")
	if !ok {
		return ""
	}
	return id
}

// Get returns a Session's current route, if present.
func (r *Registry) Get(sessionID string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.routes[sessionID]
	return route, ok
}

// Len reports the number of active Session routes (booting + live) — the
// concurrent-Session count the global cap is enforced against (#12). Removing a
// route on teardown decrements it, so a slot frees the instant a Session ends.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.routes)
}

// List returns a copy of the current routes, ordered by session id. Unlike the
// unexported snapshot it is part of the package API: the teardown reaper reads it
// to learn which Sessions exist and their state (proxy/../session.Reaper).
func (r *Registry) List() []Route {
	return r.snapshot()
}

// snapshot returns the current routes, ordered by session id for deterministic
// config output.
func (r *Registry) snapshot() []Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Route, 0, len(r.routes))
	for _, route := range r.routes {
		out = append(out, route)
	}
	sortRoutes(out)
	return out
}

func sortRoutes(routes []Route) {
	for i := 1; i < len(routes); i++ {
		for j := i; j > 0 && routes[j-1].SessionID > routes[j].SessionID; j-- {
			routes[j-1], routes[j] = routes[j], routes[j-1]
		}
	}
}

// sessionIDEncoding is lowercase base32 without padding: the alphabet [a-z2-7]
// is a subset of the [a-z0-9-] the Runner and subdomains allow.
var sessionIDEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewSessionID returns a high-entropy, unguessable Session id (128 bits) — the
// URL is the capability to reach a live Session (ADR-0004).
func NewSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("session id: %w", err)
	}
	return strings.ToLower(sessionIDEncoding.EncodeToString(b)), nil
}
