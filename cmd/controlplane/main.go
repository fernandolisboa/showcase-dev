// Command controlplane is the Showcase control-plane entrypoint. It serves the
// Owner admin + Portfolio UI (the embedded React build) and the control-plane
// API, and will host the Runner (ADR-0001) that provisions Sessions. Per
// ADR-0009 this is a single Go process co-located with Docker on one VM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/config"
	"github.com/fernandolisboa/showcase-dev/internal/proxy"
	"github.com/fernandolisboa/showcase-dev/internal/runner"
	"github.com/fernandolisboa/showcase-dev/internal/server"
	"github.com/fernandolisboa/showcase-dev/internal/session"
)

// fixtureProjectID is the single hand-configured Project the MVP plays (the
// StaticSource ignores the id). Owner-configured Projects arrive with #19.
const fixtureProjectID = "fixture"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := config.Load()

	// ADR-0004 mandates HTTPS for Session URLs (the .app TLD is HSTS-preloaded). A
	// prod deploy that forgets DEMO_SCHEME=https would mint http:// links; warn
	// loudly rather than silently hand out non-TLS Demo URLs.
	if cfg.Env == "prod" && cfg.DemoScheme != "https" {
		logger.Warn("DEMO_SCHEME is not https in prod; Session URLs will be non-TLS (ADR-0004 expects https)",
			"demo_scheme", cfg.DemoScheme)
	}

	// Traefik-facing surface, split across two listeners so a Demo can never reach
	// the control plane (ADR-0004). Both bind cfg.InternalHost (default 127.0.0.1),
	// off the public app port and off every Session-reachable interface; the Runner
	// shares this registry (wired in #10).
	//
	//   - config listener (InternalPort): serves /traefik. Traefik polls it
	//     directly over the host gateway; no Session is ever routed to it.
	//   - splash listener (SplashPort): serves the booting page ONLY. bootingURL
	//     points here, so a booting Session — whose full request path Traefik
	//     forwards — gets the splash even for /traefik, never the backend map.
	registry := proxy.NewRegistry(cfg.DemoDomain)
	bootingURL := cfg.BootingBackendURL
	if bootingURL == "" {
		bootingURL = fmt.Sprintf("http://host.docker.internal:%d", cfg.SplashPort)
	}
	configSrv := &http.Server{
		Addr:              net.JoinHostPort(cfg.InternalHost, strconv.Itoa(cfg.InternalPort)),
		Handler:           proxy.ConfigListenerHandler(registry, bootingURL, "web"),
		ReadHeaderTimeout: 10 * time.Second,
	}
	splashSrv := &http.Server{
		Addr:              net.JoinHostPort(cfg.InternalHost, strconv.Itoa(cfg.SplashPort)),
		Handler:           proxy.BootingListenerHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	for _, s := range []struct {
		name string
		srv  *http.Server
	}{{"config", configSrv}, {"splash", splashSrv}} {
		s := s
		go func() {
			logger.Info("internal traefik listener", "surface", s.name, "addr", s.srv.Addr)
			if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("internal listener error", "surface", s.name, "err", err)
			}
		}()
		defer func() {
			sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.srv.Shutdown(sc)
		}()
	}

	// The Runner boots Sessions and keeps the proxy registry current; the session
	// Manager turns a Guest "play" into a live Session asynchronously (#10). The MVP
	// plays one hand-configured fixture Project.
	compose := runner.NewCompose(
		runner.StaticSource{P: runner.FixtureProject()},
		runner.WithRuntime(cfg.Runtime),
		runner.WithProxy(registry, cfg.TraefikContainer),
	)
	sessions := session.NewManager(compose, registry, cfg.DemoScheme, logger)

	// The reaper tears Sessions down on idle and at the max-runtime cap (ADR-0006).
	// Its idle signal is Traefik's per-Session request counter, polled over host
	// loopback so the control plane observes Guest activity without sitting on the
	// Guest data path (ADR-0004); the Runner's Teardown destroys the Stack. It runs
	// for the life of the process and is cancelled+drained at shutdown (below).
	reaper := session.NewReaper(
		registry, compose.Teardown, proxy.NewTraefikMetrics(cfg.TraefikMetricsURL),
		cfg.IdleTimeout, cfg.MaxRuntime, cfg.ReaperInterval, logger,
	)
	reaperCtx, stopReaper := context.WithCancel(context.Background())
	reaperDone := make(chan struct{})
	go func() {
		defer close(reaperDone)
		reaper.Run(reaperCtx)
	}()
	defer func() {
		stopReaper()
		<-reaperDone
	}()
	logger.Info("session reaper started", "idle", cfg.IdleTimeout, "max_runtime", cfg.MaxRuntime, "interval", cfg.ReaperInterval)

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           server.New(logger, cfg, session.PlayHandler(sessions, fixtureProjectID)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shut down gracefully on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, stop, logger, srv, sessions, cfg.Addr(), cfg.Env); err != nil {
		os.Exit(1)
	}
}

// run serves srv until ctx is cancelled (signal) or the listener fails, then
// shuts down gracefully. It returns a non-nil error when the server failed to
// run — a failed bind (port in use) must surface as a non-zero exit, otherwise a
// control plane that never came up looks like a clean start to a supervisor.
func run(ctx context.Context, stop context.CancelFunc, logger *slog.Logger, srv *http.Server, sessions *session.Manager, addr, env string) error {
	// serveErr carries a fatal listen error back to the main path. It is buffered
	// so the goroutine never blocks, and the send happens-before stop(), which the
	// <-ctx.Done() below synchronizes on.
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("control plane listening", "addr", addr, "env", env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			serveErr <- err
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		return err
	}

	// The HTTP server is drained, so no new /api/play can start a boot. Now cancel
	// and drain the boots already in flight: each aborted `compose up` runs its
	// teardown, so SIGTERM mid-boot leaves no orphaned containers/networks
	// (ADR-0006). Bounded by the same shutdown deadline.
	if err := sessions.Shutdown(shutdownCtx); err != nil {
		logger.Error("session drain incomplete", "err", err)
	}

	// A signal-driven shutdown is success; a shutdown forced by a server error is not.
	select {
	case err := <-serveErr:
		return err
	default:
		return nil
	}
}
