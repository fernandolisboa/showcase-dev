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

// HealthChecker reports whether a Session's Stack is currently healthy. The
// production implementation is runner.Compose.Health (a `docker compose ps`).
type HealthChecker func(ctx context.Context, sessionID string) (bool, error)

// ReaperConfig holds the reaper's timing knobs (ADR-0006).
type ReaperConfig struct {
	// Idle tears a live Session down after this long with no Guest activity.
	Idle time.Duration
	// MaxRuntime is the absolute lifetime cap regardless of activity.
	MaxRuntime time.Duration
	// CrashGrace is how long a live Session may report unhealthy before being torn
	// down as crashed — the window for the restart policy to absorb a blip.
	CrashGrace time.Duration
	// FailureLinger is how long a Failed/Crashed route is kept (serving its failure
	// page) before removal.
	FailureLinger time.Duration
	// Interval is the scan period.
	Interval time.Duration
}

// Reaper enforces ADR-0006's Session end conditions: it tears a live Session down
// after IdleTimeout with no activity (the primary cost lever), at MaxRuntime
// regardless of activity (the absolute backstop), and as crashed when its Stack
// stops being healthy and doesn't recover within CrashGrace. It also removes the
// lingering route of a Failed/Crashed Session after FailureLinger. It scans on a
// ticker and is the only owner of timeout/crash teardown; boot-time (won't-start)
// failures are flagged by the Manager (session.go), then lingered+removed here.
//
// Idle is measured from Traefik's per-Session request counter via an
// ActivitySource: a count that hasn't moved since the previous tick is idle time
// accruing; any change (including a counter reset on Traefik restart) is treated
// as activity and resets the idle clock. When the ActivitySource errors, idle
// checks are skipped for that tick so a scrape blip never tears down an active
// Session — MaxRuntime still applies, since it needs no signal. Crash detection is
// likewise fail-safe: a health-check error skips the crash decision for that tick.
type Reaper struct {
	registry *proxy.Registry
	teardown func(ctx context.Context, sessionID string) error
	activity ActivitySource
	health   HealthChecker

	cfg ReaperConfig

	now    func() time.Time
	logger *slog.Logger

	tracked map[string]*sessionClock
}

// sessionClock is the reaper's per-Session bookkeeping. firstSeen anchors the
// max-runtime cap; lastActive anchors the idle cap; lastCount/seenCount track the
// request counter; unhealthySince anchors the crash grace; failedSince anchors the
// failure linger. Zero times mean "not yet observed in that condition".
type sessionClock struct {
	firstSeen      time.Time
	lastActive     time.Time
	lastCount      uint64
	seenCount      bool
	unhealthySince time.Time
	failedSince    time.Time
}

// NewReaper builds a Reaper. teardown is the Runner's Teardown (which drops the
// route and destroys the Stack); activity is the idle signal source; health is the
// crash signal source (nil disables crash detection).
func NewReaper(registry *proxy.Registry, teardown func(context.Context, string) error, activity ActivitySource, health HealthChecker, cfg ReaperConfig, logger *slog.Logger) *Reaper {
	if cfg.Interval < minReaperInterval {
		logger.Warn("reaper interval too low; clamping", "configured", cfg.Interval, "using", minReaperInterval)
		cfg.Interval = minReaperInterval
	}
	return &Reaper{
		registry: registry,
		teardown: teardown,
		activity: activity,
		health:   health,
		cfg:      cfg,
		now:      time.Now,
		logger:   logger,
		tracked:  map[string]*sessionClock{},
	}
}

// Run scans on the configured interval until ctx is cancelled. A cancelled ctx
// stops the loop; teardowns already issued this tick run on a context that
// survives the cancel, so a reap in flight at shutdown still completes.
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.Interval)
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
// Session either linger-remove a terminal (Failed/Crashed) route, or apply the
// max-runtime cap, crash detection, and the idle cap to an active one.
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

		// A terminal route's Stack is already gone; keep it only long enough for the
		// Guest's page to show the failure message, then drop it.
		if route.State.Terminal() {
			if clock.failedSince.IsZero() {
				clock.failedSince = now
			} else if now.Sub(clock.failedSince) >= r.cfg.FailureLinger {
				r.registry.Remove(id)
				delete(r.tracked, id)
			}
			continue
		}

		if now.Sub(clock.firstSeen) >= r.cfg.MaxRuntime {
			r.reap(ctx, id, "max-runtime")
			continue
		}

		// The remaining checks apply only to a live Session (a booting Session has
		// no request metrics or running Stack to crash; its slow boot is bounded by
		// the Manager's boot timeout).
		if route.State != proxy.Live {
			continue
		}

		// Crash: a Stack that reports unhealthy past the grace window didn't recover.
		if r.detectCrash(ctx, id, clock, now) {
			continue
		}

		if counts == nil {
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
		if now.Sub(clock.lastActive) >= r.cfg.Idle {
			r.reap(ctx, id, "idle")
		}
	}
}

// detectCrash polls the Stack's health and, if it has been unhealthy for at least
// CrashGrace, tears it down as crashed. It reports whether it handled (crashed)
// the Session, so the caller skips the idle check. A health-check error is
// fail-safe: the crash decision is skipped for the tick without resetting the
// grace clock. Returns false (no crash detection) when no HealthChecker is wired.
func (r *Reaper) detectCrash(ctx context.Context, id string, clock *sessionClock, now time.Time) bool {
	if r.health == nil {
		return false
	}
	healthy, err := r.health(ctx, id)
	if err != nil {
		r.logger.Warn("reaper: health check failed; crash check skipped this tick", "session", id, "err", err)
		return false
	}
	if healthy {
		clock.unhealthySince = time.Time{}
		return false
	}
	if clock.unhealthySince.IsZero() {
		clock.unhealthySince = now // start the grace window
		return false
	}
	if now.Sub(clock.unhealthySince) < r.cfg.CrashGrace {
		return false // still within grace — let the restart policy try
	}
	r.crash(ctx, id)
	return true
}

// crash tears down a crashed Session's Stack, then moves its route to Crashed so
// the Guest's page shows the "demo crashed" message until the linger expires. On
// teardown error the route is left as-is and retried next tick.
func (r *Reaper) crash(ctx context.Context, sessionID string) {
	r.logger.Error("session crashed; tearing down", "session", sessionID)
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reaperTeardownTimeout)
	defer cancel()
	if err := r.teardown(tctx, sessionID); err != nil {
		r.logger.Error("reaper: crash teardown failed; will retry next tick", "session", sessionID, "err", err)
		return
	}
	r.registry.Fail(sessionID, proxy.Crashed)
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
