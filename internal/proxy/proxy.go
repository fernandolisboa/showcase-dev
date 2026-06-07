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
)

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
