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
	"sync"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
)

// Provisioner boots and tears down a Session's Stack (the Runner, ADR-0001).
//
// Contract: Provision must leave nothing running on error — a failed boot tears
// down its own containers/networks before returning (ADR-0006). The Manager only
// drops the route on a Provision error; it does not call Teardown defensively, so
// a Provisioner that returns after starting containers would leak them. (Compose
// honors this: every error path runs Teardown on a WithoutCancel context.)
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
//
// Lifecycle (ADR-0006: a boot leaves nothing behind, even on shutdown): every
// boot derives its context from baseCtx, so cancelling baseCtx aborts every
// in-flight `compose up` (which propagates through exec.CommandContext into the
// Runner's WithoutCancel teardown). inflight tracks the running boots so Shutdown
// can wait for each aborted boot to finish tearing down before the process exits.
type Manager struct {
	runner    Provisioner
	registry  *proxy.Registry
	scheme    string
	bootLimit time.Duration
	logger    *slog.Logger
	newID     func() (string, error)

	baseCtx  context.Context
	cancel   context.CancelFunc
	inflight sync.WaitGroup
}

// NewManager builds a Manager. scheme is the demo URL scheme ("http" dev, "https"
// prod). The returned Manager owns a server-scoped base context; call Shutdown to
// cancel in-flight boots and drain them before the process exits.
func NewManager(runner Provisioner, registry *proxy.Registry, scheme string, logger *slog.Logger) *Manager {
	baseCtx, cancel := context.WithCancel(context.Background())
	return &Manager{
		runner:    runner,
		registry:  registry,
		scheme:    scheme,
		bootLimit: 3 * time.Minute,
		logger:    logger,
		newID:     proxy.NewSessionID,
		baseCtx:   baseCtx,
		cancel:    cancel,
	}
}

// Play starts a fresh Session for the Project and returns its URL immediately. The
// Stack boots in the background; until it is healthy the proxy serves the booting
// page, then the live Demo. A boot failure tears the Session down (the polished
// "failed to start" page is #13/#20).
//
// The global concurrency cap (#12) enforces *in front of* this call: a rejection
// must happen before newID/Add and before the goroutine is spawned, so a rejected
// play never mints an id, registers a route, or starts a boot.
func (m *Manager) Play(projectID string) (Session, error) {
	id, err := m.newID()
	if err != nil {
		return Session{}, fmt.Errorf("mint session id: %w", err)
	}
	// Register booting now so the subdomain routes to the splash the instant the
	// Guest navigates — before the background boot has even begun. The Runner
	// later re-Adds the same id with the real backends (runner.Compose.Provision);
	// that second Add overwrites this placeholder while the route is still Booting,
	// and Booting routes ignore backends (proxy/config.go), so the overwrite is a
	// no-op for routing. Two owners, by design: this Add is the Manager guaranteeing
	// the route exists synchronously before Play returns; the Runner's Add is part
	// of its own standalone contract (it registers backends even when driven without
	// a Manager — see runner integration tests). Single-sourcing would force the
	// Runner to assume a pre-registered route and break that standalone use.
	m.registry.Add(id, proxy.Backend{}, nil)

	// Track the boot so Shutdown can drain it. Increment before spawning so a
	// concurrent Shutdown either sees this boot in the WaitGroup or runs after it.
	m.inflight.Add(1)
	go m.boot(projectID, id)

	return Session{ID: id, URL: fmt.Sprintf("%s://%s", m.scheme, m.registry.Host(id))}, nil
}

// boot provisions the Stack on a context derived from the Manager's server-scoped
// baseCtx (the Guest's request has already returned). The Runner re-registers the
// real backends, attaches the proxy, and promotes to live; on failure — including
// a Shutdown that cancels baseCtx mid-boot — it tears down and we drop the route,
// so an aborted boot leaves nothing behind (ADR-0006).
func (m *Manager) boot(projectID, sessionID string) {
	defer m.inflight.Done()
	ctx, cancel := context.WithTimeout(m.baseCtx, m.bootLimit)
	defer cancel()
	if _, err := m.runner.Provision(ctx, projectID, sessionID); err != nil {
		m.logger.Error("session boot failed", "session", sessionID, "err", err)
		m.registry.Remove(sessionID)
	}
}

// Shutdown cancels every in-flight boot and waits for them to drain (each aborted
// boot runs its Provisioner's teardown on cancel), bounded by ctx. It returns
// ctx.Err() if the drain deadline is hit before all boots finish — the caller
// should treat that as "some boots may still be tearing down" — and nil once all
// boots have drained. Safe to call once during graceful shutdown.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.cancel()

	done := make(chan struct{})
	go func() {
		m.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
		// Free hardening on a guest-facing surface: never let a browser sniff this
		// JSON as another type (ADR-0004 keeps the Demo origin isolated).
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_ = json.NewEncoder(w).Encode(sess)
	})
}
