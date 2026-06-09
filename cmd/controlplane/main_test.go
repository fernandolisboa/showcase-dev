package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
	"github.com/fernandolisboa/showcase-dev/internal/session"
)

// noopProvisioner is an idle Runner: nothing is ever provisioned in these
// process-lifecycle tests, so run's session drain is a no-op.
type noopProvisioner struct{}

func (noopProvisioner) Provision(context.Context, string, string) (string, error) {
	return "", nil
}
func (noopProvisioner) Teardown(context.Context, string) error { return nil }

func testManager() *session.Manager {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return session.NewManager(noopProvisioner{}, proxy.NewRegistry("demo.app"), "https", 0, logger)
}

// run must surface a failed bind as a non-nil error so main exits non-zero — a
// supervisor must not mistake a control plane that never bound for a clean start.
func TestRunReturnsErrorOnBindFailure(t *testing.T) {
	// Occupy a port so srv.ListenAndServe cannot bind it.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := &http.Server{Addr: occupied.Addr().String()}

	done := make(chan error, 1)
	go func() { done <- run(ctx, stop, logger, srv, testManager(), nil, srv.Addr, "test") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("run returned nil on bind failure, want non-nil (must exit non-zero)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after bind failure")
	}
}

// A signal-driven shutdown (ctx cancelled, no server error) is a clean exit 0.
func TestRunReturnsNilOnSignalShutdown(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := &http.Server{Addr: "127.0.0.1:0"} // ephemeral port: binds successfully

	done := make(chan error, 1)
	go func() { done <- run(ctx, stop, logger, srv, testManager(), nil, srv.Addr, "test") }()

	// Give the listener a moment to come up, then trigger a "signal" shutdown.
	time.Sleep(100 * time.Millisecond)
	stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v on signal shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after signal shutdown")
	}
}
