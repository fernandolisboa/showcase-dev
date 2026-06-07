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
	"github.com/fernandolisboa/showcase-dev/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := config.Load()

	// Traefik-facing internal listener: the Session route registry serves Traefik's
	// dynamic config and the booting splash. Kept off the public app port so a Demo
	// can never reach it (ADR-0004). The Runner shares this registry (wired in #10).
	registry := proxy.NewRegistry(cfg.DemoDomain)
	bootingURL := cfg.BootingBackendURL
	if bootingURL == "" {
		bootingURL = fmt.Sprintf("http://host.docker.internal:%d", cfg.InternalPort)
	}
	internalSrv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.InternalPort)),
		Handler:           proxy.InternalHandler(registry, bootingURL, "web"),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logger.Info("internal traefik listener", "addr", internalSrv.Addr)
		if err := internalSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("internal listener error", "err", err)
		}
	}()
	defer func() {
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = internalSrv.Shutdown(sc)
	}()

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           server.New(logger, cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shut down gracefully on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, stop, logger, srv, cfg.Addr(), cfg.Env); err != nil {
		os.Exit(1)
	}
}

// run serves srv until ctx is cancelled (signal) or the listener fails, then
// shuts down gracefully. It returns a non-nil error when the server failed to
// run — a failed bind (port in use) must surface as a non-zero exit, otherwise a
// control plane that never came up looks like a clean start to a supervisor.
func run(ctx context.Context, stop context.CancelFunc, logger *slog.Logger, srv *http.Server, addr, env string) error {
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

	// A signal-driven shutdown is success; a shutdown forced by a server error is not.
	select {
	case err := <-serveErr:
		return err
	default:
		return nil
	}
}
