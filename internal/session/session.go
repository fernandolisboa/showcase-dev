// Package session turns a Guest "play" into a live Session: it mints an
// unguessable id, returns the Session URL immediately (so the proxy can serve the
// booting page the instant the Guest navigates), then provisions the Stack in the
// background and promotes it to live when healthy. Anonymous Guests, ephemeral
// Sessions (ADR-0004/0006/0008).
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
)

// Provisioner boots and tears down a Session's Stack (the Runner, ADR-0001).
type Provisioner interface {
	Provision(ctx context.Context, projectID, sessionID string) (string, error)
	Teardown(ctx context.Context, sessionID string) error
}

// Session is what a Guest receives from Play.
type Session struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Manager orchestrates the async play flow over a Provisioner and the proxy
// registry (shared with the Runner).
type Manager struct {
	runner    Provisioner
	registry  *proxy.Registry
	scheme    string
	bootLimit time.Duration
	logger    *slog.Logger
	newID     func() (string, error)
}

// NewManager builds a Manager. scheme is the demo URL scheme ("http" dev, "https"
// prod).
func NewManager(runner Provisioner, registry *proxy.Registry, scheme string, logger *slog.Logger) *Manager {
	return &Manager{
		runner:    runner,
		registry:  registry,
		scheme:    scheme,
		bootLimit: 3 * time.Minute,
		logger:    logger,
		newID:     proxy.NewSessionID,
	}
}

// Play starts a fresh Session for the Project and returns its URL immediately. The
// Stack boots in the background; until it is healthy the proxy serves the booting
// page, then the live Demo. A boot failure tears the Session down (the polished
// "failed to start" page is #13/#20). The global concurrency cap is #12.
func (m *Manager) Play(projectID string) (Session, error) {
	id, err := m.newID()
	if err != nil {
		return Session{}, fmt.Errorf("mint session id: %w", err)
	}
	// Register booting now so the subdomain routes to the splash the instant the
	// Guest navigates — before the background boot has even begun.
	m.registry.Add(id, proxy.Backend{}, nil)

	go m.boot(projectID, id)

	return Session{ID: id, URL: fmt.Sprintf("%s://%s", m.scheme, m.registry.Host(id))}, nil
}

// boot provisions the Stack on a detached context (the Guest's request has already
// returned). The Runner re-registers the real backends, attaches the proxy, and
// promotes to live; on failure it tears down and we drop the route.
func (m *Manager) boot(projectID, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), m.bootLimit)
	defer cancel()
	if _, err := m.runner.Provision(ctx, projectID, sessionID); err != nil {
		m.logger.Error("session boot failed", "session", sessionID, "err", err)
		m.registry.Remove(sessionID)
	}
}

// PlayHandler is the spin-up API: it starts a Session for the configured Project
// and returns it as JSON. Anonymous — no account required (ADR-0008). Mount it
// under "POST /api/play" so the method is enforced by the router.
func PlayHandler(m *Manager, projectID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sess, err := m.Play(projectID)
		if err != nil {
			http.Error(w, "could not start session", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sess)
	})
}
