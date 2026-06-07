//go:build integration

// Seam-1 tracer (the async half): play -> background boot -> Live, with real
// containers. Run with: go test -tags integration ./internal/session/...
// (Traefik attach is skipped here — WithProxy with an empty container name — so
// this asserts the async state machine without standing up the proxy.)
package session

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
	"github.com/fernandolisboa/showcase-dev/internal/runner"
)

func TestPlayBootsSessionToLive(t *testing.T) {
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("docker not available; skipping Seam-1 integration test")
	}

	reg := proxy.NewRegistry("localhost")
	r := runner.NewCompose(
		runner.StaticSource{P: runner.FixtureProject()},
		runner.WithRuntime("runc"),
		runner.WithProxy(reg, ""),
	)
	m := NewManager(r, reg, "http", slog.New(slog.NewTextHandler(io.Discard, nil)))

	sess, err := m.Play("fixture")
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	t.Cleanup(func() { _ = r.Teardown(context.Background(), sess.ID) })

	// The URL comes back immediately, before the Stack is up.
	if want := "http://" + reg.Host(sess.ID); sess.URL != want {
		t.Errorf("url = %q, want %q", sess.URL, want)
	}

	// The background boot promotes the Session to Live within the boot window.
	var route proxy.Route
	var ok bool
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if route, ok = reg.Get(sess.ID); ok && route.State == proxy.Live {
			if route.UI.URL == "" {
				t.Error("live session is missing its UI backend")
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("session never reached Live; last route=%+v ok=%v", route, ok)
}
