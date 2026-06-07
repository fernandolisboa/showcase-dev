package session

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
)

// fakeClock is a manually-advanced clock so reaper tests are deterministic and
// don't sleep for real timeouts.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// fakeActivity returns a settable per-Session request count, or an error to
// simulate an unreachable metrics endpoint.
type fakeActivity struct {
	mu     sync.Mutex
	counts map[string]uint64
	err    error
}

func (f *fakeActivity) set(id string, n uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts == nil {
		f.counts = map[string]uint64{}
	}
	f.counts[id] = n
}

func (f *fakeActivity) RequestCounts(context.Context) (map[string]uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]uint64{}
	for k, v := range f.counts {
		out[k] = v
	}
	return out, nil
}

// recordingTeardown mimics the Runner: it records the torn-down id and drops the
// route from the registry, as runner.Teardown does.
type recordingTeardown struct {
	mu   sync.Mutex
	reg  *proxy.Registry
	ids  []string
	fail error
}

func (r *recordingTeardown) teardown(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		// Mirror runner.Compose.Teardown: a failed `compose down` leaves the route
		// in the registry (the Session stays visible for retry).
		return r.fail
	}
	r.ids = append(r.ids, id)
	r.reg.Remove(id)
	return nil
}

func (r *recordingTeardown) setFail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = err
}

func (r *recordingTeardown) tornDown() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func newTestReaper(reg *proxy.Registry, td func(context.Context, string) error, act ActivitySource, idle, maxRun time.Duration, clk *fakeClock) *Reaper {
	r := NewReaper(reg, td, act, idle, maxRun, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.now = clk.now
	return r
}

// liveSession registers a Session and promotes it to live (the only state the
// idle check acts on).
func liveSession(reg *proxy.Registry, id string) {
	reg.Add(id, proxy.Backend{URL: "http://ui"}, nil)
	reg.Promote(id)
}

func TestReaperTearsDownIdleSession(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	liveSession(reg, "sleepy")
	act := &fakeActivity{}
	act.set("sleepy", 5) // a request count that never moves again
	td := &recordingTeardown{reg: reg}
	clk := &fakeClock{t: time.Unix(0, 0)}
	r := newTestReaper(reg, td.teardown, act, 10*time.Minute, time.Hour, clk)

	r.tick(context.Background()) // first sighting: baseline the counter, start idle clock
	clk.advance(11 * time.Minute)
	r.tick(context.Background()) // counter unchanged for >idle: reap

	if got := td.tornDown(); len(got) != 1 || got[0] != "sleepy" {
		t.Fatalf("torn down = %v, want [sleepy]", got)
	}
	if _, ok := reg.Get("sleepy"); ok {
		t.Error("idle session should be removed from the registry")
	}
}

func TestReaperActivityResetsIdleClock(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	liveSession(reg, "busy")
	act := &fakeActivity{}
	act.set("busy", 1)
	td := &recordingTeardown{reg: reg}
	clk := &fakeClock{t: time.Unix(0, 0)}
	r := newTestReaper(reg, td.teardown, act, 10*time.Minute, time.Hour, clk)

	// Each tick the count grows, so the session is never idle even past the window.
	for i := 2; i < 8; i++ {
		r.tick(context.Background())
		clk.advance(9 * time.Minute)
		act.set("busy", uint64(i))
	}
	if got := td.tornDown(); len(got) != 0 {
		t.Fatalf("active session should not be torn down, got %v", got)
	}
}

func TestReaperMaxRuntimeTearsDownDespiteActivity(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	liveSession(reg, "marathon")
	act := &fakeActivity{}
	act.set("marathon", 1)
	td := &recordingTeardown{reg: reg}
	clk := &fakeClock{t: time.Unix(0, 0)}
	r := newTestReaper(reg, td.teardown, act, 10*time.Minute, 30*time.Minute, clk)

	r.tick(context.Background()) // first sighting anchors max-runtime
	for i := 2; i < 6; i++ {     // stays active the whole time
		clk.advance(9 * time.Minute)
		act.set("marathon", uint64(i))
		r.tick(context.Background())
	}
	if got := td.tornDown(); len(got) != 1 || got[0] != "marathon" {
		t.Fatalf("torn down = %v, want [marathon] at the max-runtime cap", got)
	}
}

func TestReaperLeavesFreshSessionAlone(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	liveSession(reg, "fresh")
	act := &fakeActivity{}
	act.set("fresh", 0)
	td := &recordingTeardown{reg: reg}
	clk := &fakeClock{t: time.Unix(0, 0)}
	r := newTestReaper(reg, td.teardown, act, 10*time.Minute, time.Hour, clk)

	r.tick(context.Background())
	clk.advance(5 * time.Minute) // below both thresholds
	r.tick(context.Background())
	if got := td.tornDown(); len(got) != 0 {
		t.Fatalf("session below thresholds should be left alone, got %v", got)
	}
}

func TestReaperSkipsIdleWhenMetricsUnavailable(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	liveSession(reg, "blind")
	act := &fakeActivity{err: context.DeadlineExceeded}
	td := &recordingTeardown{reg: reg}
	clk := &fakeClock{t: time.Unix(0, 0)}
	r := newTestReaper(reg, td.teardown, act, 10*time.Minute, 30*time.Minute, clk)

	r.tick(context.Background())
	clk.advance(11 * time.Minute) // past idle, but no activity signal
	r.tick(context.Background())
	if got := td.tornDown(); len(got) != 0 {
		t.Fatalf("idle must not fire without an activity signal, got %v", got)
	}

	// Max-runtime still applies even though idle is blind.
	clk.advance(30 * time.Minute)
	r.tick(context.Background())
	if got := td.tornDown(); len(got) != 1 || got[0] != "blind" {
		t.Fatalf("max-runtime should still reap, got %v", got)
	}
}

func TestReaperCounterResetTreatedAsActivity(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	liveSession(reg, "restarted")
	act := &fakeActivity{}
	act.set("restarted", 100)
	td := &recordingTeardown{reg: reg}
	clk := &fakeClock{t: time.Unix(0, 0)}
	r := newTestReaper(reg, td.teardown, act, 10*time.Minute, time.Hour, clk)

	r.tick(context.Background()) // baseline at 100
	clk.advance(6 * time.Minute)
	act.set("restarted", 3) // Traefik restarted: counter reset (3 < 100)
	r.tick(context.Background())
	clk.advance(6 * time.Minute) // total 12m since baseline, but reset re-anchored idle
	r.tick(context.Background())
	if got := td.tornDown(); len(got) != 0 {
		t.Fatalf("a counter reset is activity, not idle; got %v", got)
	}
}

func TestReaperIgnoresBootingSessionForIdle(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	reg.Add("booting", proxy.Backend{}, nil) // left Booting, never promoted
	act := &fakeActivity{}
	td := &recordingTeardown{reg: reg}
	clk := &fakeClock{t: time.Unix(0, 0)}
	r := newTestReaper(reg, td.teardown, act, 10*time.Minute, time.Hour, clk)

	r.tick(context.Background())
	clk.advance(11 * time.Minute)
	r.tick(context.Background())
	if got := td.tornDown(); len(got) != 0 {
		t.Fatalf("idle does not apply to a Booting session, got %v", got)
	}
}

func TestReaperPrunesStateForGoneSessions(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	liveSession(reg, "ephemeral")
	act := &fakeActivity{}
	td := &recordingTeardown{reg: reg}
	clk := &fakeClock{t: time.Unix(0, 0)}
	r := newTestReaper(reg, td.teardown, act, 10*time.Minute, time.Hour, clk)

	r.tick(context.Background())
	if len(r.tracked) != 1 {
		t.Fatalf("expected 1 tracked session, got %d", len(r.tracked))
	}
	reg.Remove("ephemeral") // session ended by another path
	r.tick(context.Background())
	if len(r.tracked) != 0 {
		t.Errorf("tracked state should be pruned for a gone session, got %d", len(r.tracked))
	}
}

func TestReaperRetriesAfterTeardownFailure(t *testing.T) {
	reg := proxy.NewRegistry("demo.app")
	liveSession(reg, "stubborn")
	act := &fakeActivity{}
	act.set("stubborn", 1)
	td := &recordingTeardown{reg: reg, fail: errBoom}
	clk := &fakeClock{t: time.Unix(0, 0)}
	r := newTestReaper(reg, td.teardown, act, 10*time.Minute, time.Hour, clk)

	r.tick(context.Background())
	clk.advance(11 * time.Minute)
	r.tick(context.Background()) // teardown fails: session stays tracked AND present
	if _, ok := r.tracked["stubborn"]; !ok {
		t.Fatal("a session whose teardown failed should remain tracked for retry")
	}
	if _, ok := reg.Get("stubborn"); !ok {
		t.Fatal("a failed teardown must leave the route in the registry so the reap is retried")
	}

	// Next tick the teardown succeeds: the reap is retried and the session removed.
	td.setFail(nil)
	r.tick(context.Background())
	if got := td.tornDown(); len(got) != 1 || got[0] != "stubborn" {
		t.Fatalf("retry should tear the session down, got %v", got)
	}
	if _, ok := r.tracked["stubborn"]; ok {
		t.Error("a successfully retried session should be untracked")
	}
}
