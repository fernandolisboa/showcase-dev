package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
)

var errBoom = errors.New("boom")

type fakeRunner struct {
	called   chan string
	failWith error
}

func (f *fakeRunner) Provision(_ context.Context, _, sessionID string) (string, error) {
	f.called <- sessionID
	if f.failWith != nil {
		return "", f.failWith
	}
	return "http://" + sessionID, nil
}

func (f *fakeRunner) Teardown(context.Context, string) error { return nil }

func newTestManager(runner Provisioner, reg *proxy.Registry) *Manager {
	return NewManager(runner, reg, "https", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestPlayReturnsURLAndRegistersBootingThenBootsInBackground(t *testing.T) {
	reg := proxy.NewRegistry("showcasedemo.app")
	runner := &fakeRunner{called: make(chan string, 1)}
	m := newTestManager(runner, reg)

	sess, err := m.Play("fixture")
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("expected a session id")
	}
	if want := "https://" + reg.Host(sess.ID); sess.URL != want {
		t.Errorf("url = %q, want %q", sess.URL, want)
	}
	// Booting is registered synchronously, before the background boot.
	if route, ok := reg.Get(sess.ID); !ok || route.State != proxy.Booting {
		t.Errorf("session should be Booting immediately, got %+v ok=%v", route, ok)
	}
	// The background boot runs with the same id.
	select {
	case got := <-runner.called:
		if got != sess.ID {
			t.Errorf("provisioned %q, want %q", got, sess.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Provision was not called in the background")
	}
}

func TestPlayFailureDropsRoute(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	runner := &fakeRunner{called: make(chan string, 1), failWith: errBoom}
	m := newTestManager(runner, reg)

	sess, _ := m.Play("fixture")
	<-runner.called

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get(sess.ID); !ok {
			return // route dropped after the failed boot — correct
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("route should be removed after a boot failure")
}

func TestPlayHandlerServesJSON(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	runner := &fakeRunner{called: make(chan string, 1)}
	m := newTestManager(runner, reg)

	rec := httptest.NewRecorder()
	PlayHandler(m, "fixture").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/play", nil))
	<-runner.called

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var sess Session
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if sess.ID == "" || sess.URL == "" {
		t.Errorf("incomplete session: %+v", sess)
	}
}
