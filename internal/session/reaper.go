package session

import (
	"context"
	"log/slog"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
)

// ActivitySource reports the cumulative Guest request count per Session id. The
// reaper diffs successive readings to tell active Sessions from idle ones; the
// production implementation is proxy.TraefikMetrics (a side-channel poll of
// Traefik, off the Guest data path — ADR-0004/0006).
type ActivitySource interface {
	RequestCounts(ctx context.Context) (map[string]uint64, error)
}

// reaperTeardownTimeout bounds a single Session teardown so one stuck `compose
// down` can't wedge the reap loop.
const reaperTeardownTimeout = 30 * time.Second

// minReaperInterval is the floor for the scan interval. A non-positive interval
// would panic time.NewTicker and silently kill the reaper goroutine — and with it
// all teardown — so a misconfigured (or zero) interval is clamped, not honored.
const minReaperInterval = time.Second

// Reaper enforces ADR-0006's two end conditions on running Sessions: it tears a
// Session down after IdleTimeout with no activity (the primary cost lever) and at
// MaxRuntime regardless of activity (the absolute backstop). It scans on a ticker
// and is the only owner of timeout teardown; boot-time failures are handled by the
// Manager (session.go).
//
// Idle is measured from Traefik's per-Session request counter via an
// ActivitySource: a count that hasn't moved since the previous tick is idle time
// accruing; any change (including a counter reset on Traefik restart) is treated
// as activity and resets the idle clock. When the ActivitySource errors, idle
// checks are skipped for that tick so a scrape blip never tears down an active
// Session — MaxRuntime still applies, since it needs no signal.
type Reaper struct {
	registry *proxy.Registry
	teardown func(ctx context.Context, sessionID string) error
	activity ActivitySource

	idle     time.Duration
	maxRun   time.Duration
	interval time.Duration

	now    func() time.Time
	logger *slog.Logger

	tracked map[string]*sessionClock
}

// sessionClock is the reaper's per-Session bookkeeping. firstSeen anchors the
// max-runtime cap; lastActive anchors the idle cap; lastCount/seenCount track the
// request counter across ticks.
type sessionClock struct {
	firstSeen  time.Time
	lastActive time.Time
	lastCount  uint64
	seenCount  bool
}

// NewReaper builds a Reaper. teardown is the Runner's Teardown (which drops the
// route and destroys the Stack); activity is the idle signal source.
func NewReaper(registry *proxy.Registry, teardown func(context.Context, string) error, activity ActivitySource, idle, maxRun, interval time.Duration, logger *slog.Logger) *Reaper {
	if interval < minReaperInterval {
		logger.Warn("reaper interval too low; clamping", "configured", interval, "using", minReaperInterval)
		interval = minReaperInterval
	}
	return &Reaper{
		registry: registry,
		teardown: teardown,
		activity: activity,
		idle:     idle,
		maxRun:   maxRun,
		interval: interval,
		now:      time.Now,
		logger:   logger,
		tracked:  map[string]*sessionClock{},
	}
}

// Run scans on the configured interval until ctx is cancelled. A cancelled ctx
// stops the loop; teardowns already issued this tick run on a context that
// survives the cancel, so a reap in flight at shutdown still completes.
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// tick performs one scan: prune state for gone Sessions, then for each present
// Session apply the max-runtime cap and (for live Sessions, when activity is
// available) the idle cap.
func (r *Reaper) tick(ctx context.Context) {
	present := map[string]proxy.Route{}
	for _, route := range r.registry.List() {
		present[route.SessionID] = route
	}
	for id := range r.tracked {
		if _, ok := present[id]; !ok {
			delete(r.tracked, id)
		}
	}

	// Idle needs activity; max-runtime does not. A scrape failure must never reap
	// a live Session, so on error we leave counts nil and skip idle this tick.
	counts, err := r.activity.RequestCounts(ctx)
	if err != nil {
		r.logger.Warn("reaper: activity scrape failed; idle checks skipped this tick", "err", err)
		counts = nil
	}

	now := r.now()
	for id, route := range present {
		clock, ok := r.tracked[id]
		if !ok {
			clock = &sessionClock{firstSeen: now, lastActive: now}
			r.tracked[id] = clock
		}

		if now.Sub(clock.firstSeen) >= r.maxRun {
			r.reap(ctx, id, "max-runtime")
			continue
		}

		// Idle only applies once a Session is live (a booting Session has no
		// request metrics; its slow boot is bounded by the Manager's boot timeout).
		if route.State != proxy.Live || counts == nil {
			continue
		}
		count := counts[id]
		if !clock.seenCount {
			clock.lastCount = count
			clock.seenCount = true
			clock.lastActive = now
			continue
		}
		if count != clock.lastCount {
			// Any change is activity. A drop (count < lastCount) means Traefik
			// restarted and reset its counters; re-baseline rather than reap.
			clock.lastCount = count
			clock.lastActive = now
			continue
		}
		if now.Sub(clock.lastActive) >= r.idle {
			r.reap(ctx, id, "idle")
		}
	}
}

// reap tears a Session down on a context that survives the reaper's own
// cancellation, so a reap that starts just before shutdown still finishes. On
// teardown error the Session is left tracked so the next tick retries.
func (r *Reaper) reap(ctx context.Context, sessionID, reason string) {
	r.logger.Info("reaping session", "session", sessionID, "reason", reason)
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reaperTeardownTimeout)
	defer cancel()
	if err := r.teardown(tctx, sessionID); err != nil {
		r.logger.Error("reaper: teardown failed; will retry next tick", "session", sessionID, "reason", reason, "err", err)
		return
	}
	delete(r.tracked, sessionID)
}
