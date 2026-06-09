package project

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/githubapp"
	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

// Publisher runs Project builds asynchronously, off the Owner's request, so publish
// returns immediately while the build proceeds in the background — mirroring how
// session.Manager boots a Session (a server-scoped baseCtx + an inflight WaitGroup that
// Shutdown cancels and drains). When a build finishes it settles the Project's lifecycle
// (#49): success → published at the built commit, failure → 'failed' with a bounded log
// tail. The caller (the Publish handler) has already transitioned the Project to
// 'building' via store.StartBuild, so a crash or shutdown before the build settles leaves
// a 'building' row the recovery sweep (ReapStuckBuilds) reclaims.
type Publisher struct {
	store   Store
	builder ProjectBuilder
	logger  *slog.Logger

	baseCtx  context.Context
	cancel   context.CancelFunc
	inflight sync.WaitGroup
}

// NewPublisher builds a Publisher over the store and image builder. It owns a
// server-scoped base context; call Shutdown to cancel in-flight builds and drain them
// before the process exits.
func NewPublisher(s Store, b ProjectBuilder, logger *slog.Logger) *Publisher {
	baseCtx, cancel := context.WithCancel(context.Background())
	return &Publisher{store: s, builder: b, logger: logger, baseCtx: baseCtx, cancel: cancel}
}

// Start launches the background build of a Project whose row the caller has already moved
// to 'building'. It returns immediately; the build runs on the Publisher's server-scoped
// context (the Owner's request has returned) and settles the lifecycle when it finishes.
func (p *Publisher) Start(ownerID int64, ownerLogin, projectID string, m runcontract.Manifest) {
	// Increment before spawning so a concurrent Shutdown either sees this build in the
	// WaitGroup or runs after it (mirrors session.Manager.Play).
	p.inflight.Add(1)
	go p.build(ownerID, ownerLogin, projectID, m)
}

// build runs the (synchronous) image build and records the outcome. The settle write uses
// a cancel-resistant context so a build that finished just as the server began shutting
// down still records its result, matching the codebase's after-write pattern (migrate.go,
// reaper.go, compose.go).
func (p *Publisher) build(ownerID int64, ownerLogin, projectID string, m runcontract.Manifest) {
	defer p.inflight.Done()

	commits, err := p.builder.BuildProject(p.baseCtx, ownerLogin, projectID, m)
	settleCtx := context.WithoutCancel(p.baseCtx)

	if err != nil {
		// A Shutdown cancelled the build mid-flight: this isn't a real build failure, so
		// leave the row 'building' for the recovery sweep to reclaim on restart rather than
		// recording a misleading error for an aborted build (mirrors session.Manager.boot).
		if p.baseCtx.Err() != nil {
			return
		}
		msg := "build failed:\n" + lastBytes(err.Error(), 4<<10)
		if errors.Is(err, githubapp.ErrNoInstallation) {
			msg = "the Showcase GitHub App is not installed on that repository"
		}
		if err := p.store.MarkBuildFailed(settleCtx, ownerID, projectID, msg); err != nil {
			p.logger.Error("failed to record build failure", "project", projectID, "err", err)
		}
		return
	}

	// Persist the per-service commits (#50) plus the headline (the UI service's commit)
	// for the single-commit API/display. json.Marshal of a map[string]string never fails.
	serviceCommits, _ := json.Marshal(commits)
	if err := p.store.PublishProject(settleCtx, ownerID, projectID, headlineCommit(m, commits), serviceCommits); err != nil {
		// The build succeeded but recording the publish failed; settle out of 'building' so
		// the Owner can retry (a retry rebuilds from cache, so it is cheap).
		p.logger.Error("build succeeded but recording the publish failed", "project", projectID, "err", err)
		_ = p.store.MarkBuildFailed(settleCtx, ownerID, projectID, "build succeeded but publishing failed; please retry")
	}
}

// headlineCommit picks the commit shown as the Project's single headline commit_sha (for
// the existing API/display): the UI service's commit (manifest validation guarantees
// exactly one ui service), falling back to any non-empty commit.
func headlineCommit(m runcontract.Manifest, commits map[string]string) string {
	for _, svc := range m.Services {
		if svc.Role == runcontract.RoleUI {
			return commits[svc.Name]
		}
	}
	for _, svc := range m.Services {
		if c := commits[svc.Name]; c != "" {
			return c
		}
	}
	return ""
}

// ReapStuckBuilds reclaims builds stranded in 'building' (by a crash or a restart
// mid-build) — once at startup to recover the previous process's interrupted builds, then
// periodically as a backstop — settling them to 'failed' so the Owner can retry. It runs
// until ctx is cancelled. perServiceTimeout (the per-service build cap) and grace size each
// Project's staleness window in the store; the tick cadence is grace (floored at 1m), so
// the periodic backstop catches a stranded build within roughly one grace period of it
// crossing its bound.
func (p *Publisher) ReapStuckBuilds(ctx context.Context, perServiceTimeout, grace time.Duration) {
	p.reapOnce(ctx, perServiceTimeout, grace)

	interval := grace
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reapOnce(ctx, perServiceTimeout, grace)
		}
	}
}

func (p *Publisher) reapOnce(ctx context.Context, perServiceTimeout, grace time.Duration) {
	n, err := p.store.ReclaimStuckBuilds(ctx, perServiceTimeout, grace)
	if err != nil {
		p.logger.Error("reclaim stuck builds failed", "err", err)
		return
	}
	if n > 0 {
		p.logger.Warn("reclaimed builds stranded in 'building'", "count", n)
	}
}

// Shutdown cancels every in-flight build and waits for them to drain (each aborted build
// settles or, if it was cancelled mid-flight, is left for the recovery sweep), bounded by
// ctx. Returns ctx.Err() if the drain deadline is hit, nil once all builds have drained.
func (p *Publisher) Shutdown(ctx context.Context) error {
	p.cancel()

	done := make(chan struct{})
	go func() {
		p.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// wait blocks until all in-flight builds finish. Test-only seam (production drains via
// Shutdown); it does not cancel the base context, so a build settles normally.
func (p *Publisher) wait() { p.inflight.Wait() }
