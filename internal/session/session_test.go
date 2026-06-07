package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
)

var errBoom = errors.New("boom")

// quietRunner provisions instantly and never blocks, so cap tests can drive many
// Plays without juggling channels; it leaves the registry to the Manager, so a
// played Session's placeholder route persists (counting toward the cap) until
// explicitly removed.
type quietRunner struct{}

func (quietRunner) Provision(_ context.Context, _, sessionID string) (string, error) {
	return "http://" + sessionID, nil
}
func (quietRunner) Teardown(context.Context, string) error { return nil }

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

// blockingRunner models a Provisioner whose Provision is mid-boot: it blocks until
// its boot context is cancelled, then (like Compose's failure path) tears down on a
// context that survives the cancel and records that it did so. Used to prove
// Shutdown both cancels in-flight boots and drains them.
type blockingRunner struct {
	started   chan struct{}
	cancelled chan struct{}
	tornDown  chan string
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{
		started:   make(chan struct{}, 1),
		cancelled: make(chan struct{}, 1),
		tornDown:  make(chan string, 1),
	}
}

func (b *blockingRunner) Provision(ctx context.Context, _, sessionID string) (string, error) {
	b.started <- struct{}{}
	<-ctx.Done() // block mid-boot until the boot context is cancelled
	b.cancelled <- struct{}{}
	// Compose tears down on a WithoutCancel context so an aborted boot still cleans
	// up; mimic that here and only return once teardown has run.
	_ = b.Teardown(context.WithoutCancel(ctx), sessionID)
	return "", ctx.Err()
}

func (b *blockingRunner) Teardown(_ context.Context, sessionID string) error {
	b.tornDown <- sessionID
	return nil
}

func newTestManager(runner Provisioner, reg *proxy.Registry) *Manager {
	return NewManager(runner, reg, "https", 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func newCappedManager(runner Provisioner, reg *proxy.Registry, maxSessions int) *Manager {
	return NewManager(runner, reg, "https", maxSessions, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func TestPlayFailureMovesRouteToFailed(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	runner := &fakeRunner{called: make(chan string, 1), failWith: errBoom}
	m := newTestManager(runner, reg)

	sess, _ := m.Play("fixture")
	<-runner.called

	// A won't-start boot moves the route to Failed (not removed) so the Guest's
	// auto-refreshing page lands on the "failed to start" message; the reaper
	// removes the lingering route later (#13).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if route, ok := reg.Get(sess.ID); ok && route.State == proxy.Failed {
			return // failed route lingering — correct
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("route should be in Failed state after a boot failure")
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

func TestPlayRejectsAtCapacity(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	m := newCappedManager(quietRunner{}, reg, 2)

	if _, err := m.Play("fixture"); err != nil {
		t.Fatalf("first play: %v", err)
	}
	if _, err := m.Play("fixture"); err != nil {
		t.Fatalf("second play: %v", err)
	}
	if _, err := m.Play("fixture"); !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("third play err = %v, want ErrAtCapacity", err)
	}
	// The rejected play minted no id and registered no route: still exactly the cap.
	if got := reg.Len(); got != 2 {
		t.Errorf("registry has %d sessions, want 2 (rejected play must not register)", got)
	}
}

func TestCapacityFreesAfterTeardown(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	m := newCappedManager(quietRunner{}, reg, 1)

	first, err := m.Play("fixture")
	if err != nil {
		t.Fatalf("first play: %v", err)
	}
	if _, err := m.Play("fixture"); !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("play at cap err = %v, want ErrAtCapacity", err)
	}

	reg.Remove(first.ID) // a teardown (reaper / boot failure) frees the slot

	if _, err := m.Play("fixture"); err != nil {
		t.Errorf("play after a slot freed: %v, want success", err)
	}
}

func TestCapDisabledWhenNonPositive(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	m := newCappedManager(quietRunner{}, reg, 0) // 0 = unlimited

	for i := 0; i < 25; i++ {
		if _, err := m.Play("fixture"); err != nil {
			t.Fatalf("play %d with cap disabled: %v", i, err)
		}
	}
	if got := reg.Len(); got != 25 {
		t.Errorf("registry has %d sessions, want 25 (cap disabled)", got)
	}
}

// TestConcurrentPlaysNeverExceedCap proves the admission decision is atomic: under
// a stampede, exactly cap plays win and the rest are rejected — never an overshoot.
// Run with -race.
func TestConcurrentPlaysNeverExceedCap(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	const cap, attempts = 5, 64
	m := newCappedManager(quietRunner{}, reg, cap)

	var admitted, rejected atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch _, err := m.Play("fixture"); {
			case err == nil:
				admitted.Add(1)
			case errors.Is(err, ErrAtCapacity):
				rejected.Add(1)
			default:
				t.Errorf("unexpected play error: %v", err)
			}
		}()
	}
	wg.Wait()

	if admitted.Load() != cap {
		t.Errorf("admitted %d, want exactly %d", admitted.Load(), cap)
	}
	if rejected.Load() != attempts-cap {
		t.Errorf("rejected %d, want %d", rejected.Load(), attempts-cap)
	}
	if got := reg.Len(); got != cap {
		t.Errorf("registry has %d sessions, want %d", got, cap)
	}
}

func TestPlayHandlerAtCapacityReturns503(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	m := newCappedManager(quietRunner{}, reg, 1)
	reg.Add("taken", proxy.Backend{}, nil) // already at the cap of 1

	rec := httptest.NewRecorder()
	PlayHandler(m, "fixture").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/play", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header on an at-capacity response")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if body["error"] != "at_capacity" {
		t.Errorf("error = %q, want at_capacity", body["error"])
	}
}

// TestShutdownCancelsAndDrainsInflightBoots is the M1 regression: a Session mid-boot
// at SIGTERM must be cancelled and drained (its teardown run) before the process
// exits, so nothing leaks (ADR-0006).
func TestShutdownCancelsAndDrainsInflightBoots(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	runner := newBlockingRunner()
	m := newTestManager(runner, reg)

	sess, err := m.Play("fixture")
	if err != nil {
		t.Fatalf("play: %v", err)
	}

	// Wait until the boot is actually in flight (blocked on its context).
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("boot never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should drain within deadline, got %v", err)
	}

	// Shutdown cancelled the boot context...
	select {
	case <-runner.cancelled:
	default:
		t.Error("Shutdown did not cancel the in-flight boot")
	}
	// ...and the boot's teardown ran for this session before Shutdown returned.
	select {
	case got := <-runner.tornDown:
		if got != sess.ID {
			t.Errorf("torn down %q, want %q", got, sess.ID)
		}
	default:
		t.Error("Shutdown returned before the aborted boot drained its teardown")
	}
	// A boot cancelled by shutdown is NOT a demo failure: the route is dropped, not
	// left lingering in Failed (#13 shutdown-vs-failure distinction).
	if _, ok := reg.Get(sess.ID); ok {
		t.Error("a shutdown-cancelled boot should drop its route, not leave it Failed")
	}
}

// TestShutdownDeadlineExceededWhenBootHangs proves the drain is bounded: if a boot
// ignores cancellation, Shutdown returns ctx.Err() rather than blocking forever.
func TestShutdownDeadlineExceededWhenBootHangs(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	hang := make(chan struct{})
	runner := &hangingRunner{started: make(chan struct{}, 1), release: hang}
	m := newTestManager(runner, reg)

	if _, err := m.Play("fixture"); err != nil {
		t.Fatalf("play: %v", err)
	}
	<-runner.started

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown err = %v, want DeadlineExceeded", err)
	}
	close(hang) // let the goroutine exit so the test doesn't leak it
}

// hangingRunner ignores its context entirely and blocks until released — a
// pathological boot used to exercise Shutdown's bounded deadline.
type hangingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (h *hangingRunner) Provision(_ context.Context, _, _ string) (string, error) {
	h.started <- struct{}{}
	<-h.release
	return "", nil
}

func (h *hangingRunner) Teardown(context.Context, string) error { return nil }
