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

	"github.com/fernandolisboa/showcase-dev/internal/auth"
	"github.com/fernandolisboa/showcase-dev/internal/builder"
	"github.com/fernandolisboa/showcase-dev/internal/config"
	"github.com/fernandolisboa/showcase-dev/internal/fixture"
	"github.com/fernandolisboa/showcase-dev/internal/githubapp"
	"github.com/fernandolisboa/showcase-dev/internal/project"
	"github.com/fernandolisboa/showcase-dev/internal/proxy"
	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/runner"
	"github.com/fernandolisboa/showcase-dev/internal/server"
	"github.com/fernandolisboa/showcase-dev/internal/session"
	"github.com/fernandolisboa/showcase-dev/internal/store"
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

	// Control-plane persistence (ADR-0009). When DATABASE_URL is set we open the
	// pool and run migrations before serving — a misconfigured DB or a failed
	// migration is fatal, so the process never comes up half-migrated. When it is
	// unset (the dependency-free dev loop), persistence is disabled and /readyz
	// reports ready without a store; the Owner features built on it simply aren't
	// wired. `ready` is the /readyz DB probe; `dbStore` backs Owner sign-in.
	var ready func(context.Context) error
	var dbStore *store.Store
	if cfg.DatabaseURL != "" {
		st, err := store.Open(context.Background(), cfg.DatabaseURL)
		if err != nil {
			logger.Error("open database", "err", err)
			os.Exit(1)
		}
		defer st.Close()
		migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := st.Migrate(migrateCtx); err != nil {
			migrateCancel()
			logger.Error("run migrations", "err", err)
			os.Exit(1)
		}
		migrateCancel()
		dbStore = st
		ready = st.Ping
		logger.Info("control-plane persistence ready (migrations applied)")
	} else {
		logger.Warn("DATABASE_URL not set; control-plane persistence disabled (dev) — Owner sign-in is off")
	}

	// Owner sign-in (ADR-0008, issue #19). Needs persistence; the GitHub App
	// (issue #4) is registered by a human, so when its credentials are absent we
	// still build the Authenticator (sessions validate) but Login/Callback report
	// 501 — the dev loop runs without a real App.
	var authn *auth.Authenticator
	var projects *project.Handlers // built below, once the image builder exists
	var publisher *project.Publisher
	if dbStore != nil {
		var provider auth.IdentityProvider
		if cfg.GitHubClientID != "" && cfg.GitHubClientSecret != "" {
			callback := cfg.OAuthCallbackURL
			if callback == "" {
				callback = fmt.Sprintf("http://localhost:%d/auth/github/callback", cfg.Port)
			}
			provider = auth.NewGitHubProvider(cfg.GitHubClientID, cfg.GitHubClientSecret, callback)
			logger.Info("owner sign-in enabled (github app configured)")
		} else {
			logger.Warn("GitHub App credentials not set; Owner sign-in disabled (login returns 501) — see docs/ops/04-register-github-app.md")
		}
		authn = auth.New(provider, dbStore, cfg.Env == "prod")

		// Expired login sessions are already rejected at query time, but prune them
		// so the table doesn't grow unbounded. Runs for the process lifetime,
		// cancelled at shutdown.
		pruneCtx, stopPrune := context.WithCancel(context.Background())
		defer stopPrune()
		go pruneLoginSessions(pruneCtx, dbStore, logger)
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
		Handler:           proxy.BootingListenerHandler(registry.StateByHost),
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

	// The builder builds the Project's image(s) from source and caches them
	// (ADR-0003); the BuildingSource swaps the built tags into the Project the
	// Runner boots, replacing prebuilt refs. The MVP builds one in-repo fixture from
	// its embedded Dockerfile; #19 will resolve real Owner repos via SpecFunc. Every
	// build runs in a hardened, throwaway rootless-BuildKit sandbox (ADR-0010), so an
	// untrusted Owner's RUN steps never execute on the control-plane host.
	imageBuilder := builder.New(cfg.MaxCachedImages, logger, builder.WithSandbox(builder.Sandbox{
		Image:     cfg.BuildSandboxImage,
		Network:   cfg.BuildNetwork,
		MemoryMB:  cfg.BuildMemoryMB,
		CPUs:      cfg.BuildCPUs,
		PidsLimit: cfg.BuildPidsLimit,
		Timeout:   cfg.BuildTimeout,
	}))
	fixtureProject := runner.Project{Manifest: fixture.Manifest(), Images: map[string]string{}}

	// Owner repos are built from source at publish via a GitHub App installation token
	// (ADR-0008). The App's OWN credentials (numeric id + private key) are distinct
	// from the OAuth client used for sign-in; absent them, building Owner repos is
	// disabled (publish returns 501) but the demo still builds from the embedded
	// fixture. A bad key is logged and treated as absent, never fatal.
	var ghClient *githubapp.Client
	if cfg.GitHubAppID != "" && cfg.GitHubAppPrivateKey != "" {
		if c, err := githubapp.New(cfg.GitHubAppID, cfg.GitHubAppPrivateKey); err != nil {
			logger.Error("GitHub App credentials invalid; building Owner repos disabled", "err", err)
		} else {
			ghClient = c
			logger.Info("owner repo builds enabled (github app configured)")
		}
	}

	// specFunc resolves each service to its build spec. The fixture builds from its
	// embedded context; a real Owner Project (a uuid id) clones its repo at the pinned
	// commit (play) or the resolved default-branch HEAD (publish) via the App token.
	specFunc := func(ctx context.Context, projectID string, svc runcontract.Service, commit string) (builder.BuildSpec, error) {
		if projectID == fixtureProjectID {
			version, err := fixture.Version(svc.Name)
			if err != nil {
				return builder.BuildSpec{}, err
			}
			return builder.BuildSpec{
				ImageName:  "showcase/" + projectID + "-" + svc.Name,
				Version:    version,
				Dockerfile: fixture.Dockerfile,
				Context:    func() (string, func(), error) { return fixture.Extract(svc.Name) },
			}, nil
		}
		return gitBuildSpec(ctx, ghClient, projectID, svc, commit)
	}

	// The base source the builder wraps resolves a Project by id. Without a database
	// it is just the demo fixture (StaticSource ignores the id, #8). With one, it
	// resolves a published Owner Project from the DB first and falls back to the
	// fixture when the id isn't a published Project — so the hand-configured demo
	// keeps booting even with persistence wired (#19). The play build is pinned to the
	// published commit, so a Guest never waits on a rebuild.
	var base runner.ProjectSource = runner.StaticSource{P: fixtureProject}
	if dbStore != nil {
		base = runner.FallbackSource{
			Primary:   project.NewSource(dbStore),
			Secondary: base,
		}
	}
	source := builder.NewBuildingSource(base, imageBuilder, specFunc, logger)

	// Publishing builds an Owner's DRAFT directly from its manifest (the play-path
	// source resolves only PUBLISHED Projects, so it cannot build a draft), returning
	// the built commit to pin. Reuses the same builder + specFunc, so a later play is
	// a cache hit on the published image. nil builder => publish reports 501.
	if dbStore != nil {
		var pb project.ProjectBuilder
		if ghClient != nil {
			pb = builderFunc(func(ctx context.Context, _, projectID string, m runcontract.Manifest) (string, error) {
				if len(m.Services) == 0 {
					return "", fmt.Errorf("project has no services")
				}
				// Services build serially, each capped by the sandbox per-build timeout
				// (ADR-0010); bound the whole publish accordingly.
				bctx, cancel := context.WithTimeout(ctx, time.Duration(len(m.Services)+1)*cfg.BuildTimeout)
				defer cancel()
				// Resolve the repo's HEAD ONCE and pin every service to it (the handler
				// enforces a single repo before publishing), so the whole Project builds
				// at one atomic commit even under a concurrent push — and a later play,
				// pinned to this same commit, is a cache hit for every service.
				owner, repo, err := githubapp.ParseRepo(m.Services[0].Repo)
				if err != nil {
					return "", err
				}
				tok, _, err := ghClient.InstallationToken(bctx, owner, repo)
				if err != nil {
					return "", err
				}
				sha, err := githubapp.ResolveCommit(bctx, owner, repo, "HEAD", tok)
				if err != nil {
					return "", err
				}
				draft := runner.StaticSource{P: runner.Project{Manifest: m, Commit: sha}}
				if _, err := builder.NewBuildingSource(draft, imageBuilder, specFunc, logger).Project(bctx, projectID); err != nil {
					return "", err
				}
				return sha, nil
			})
		}
		// The Publisher runs publish builds on a background goroutine (#49) so the Owner's
		// request returns immediately; it settles build_state when the build finishes and
		// is drained at shutdown. nil builder => nil publisher => publish reports 501.
		if pb != nil {
			publisher = project.NewPublisher(dbStore, pb, logger)
		}
		projects = project.NewHandlers(dbStore, publisher)
	}

	// The Runner boots Sessions and keeps the proxy registry current; the session
	// Manager turns a Guest "play" into a live Session asynchronously (#10). The MVP
	// plays one hand-configured fixture Project.
	compose := runner.NewCompose(
		source,
		runner.WithRuntime(cfg.Runtime),
		runner.WithProxy(registry, cfg.TraefikContainer),
		runner.WithEgressProxyImage(cfg.EgressProxyImage),
	)
	sessions := session.NewManager(compose, registry, cfg.DemoScheme, cfg.MaxSessions, logger)

	// Warm the image cache at startup ("publish"): build now so a Guest's first play
	// never waits on a build (ADR-0003). A failure is logged, not fatal — demos then
	// surface "failed to start" (#13) until the build is fixed.
	warmCtx, warmCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	if _, err := source.Project(warmCtx, fixtureProjectID); err != nil {
		logger.Error("startup image build (publish) failed; demos will fail to start until fixed", "err", err)
	}
	warmCancel()

	// The reaper tears Sessions down on idle, at the max-runtime cap, and on crash,
	// and removes the lingering routes of failed Sessions (ADR-0006). Its idle
	// signal is Traefik's per-Session request counter, polled over host loopback so
	// the control plane observes Guest activity without sitting on the Guest data
	// path (ADR-0004); its crash signal is the Runner's per-Session health; the
	// Runner's Teardown destroys the Stack. It runs for the life of the process and
	// is cancelled+drained at shutdown (below).
	reaper := session.NewReaper(
		registry, compose.Teardown, proxy.NewTraefikMetrics(cfg.TraefikMetricsURL), compose.Health,
		session.ReaperConfig{
			Idle:          cfg.IdleTimeout,
			MaxRuntime:    cfg.MaxRuntime,
			CrashGrace:    cfg.CrashGrace,
			FailureLinger: cfg.FailureLinger,
			Interval:      cfg.ReaperInterval,
		},
		logger,
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

	// Recover Projects stranded in 'building' by a crash or restart mid-build (#49): sweep
	// once at startup (reclaiming the previous process's interrupted builds) and then
	// periodically. Runs for the process lifetime, cancelled at shutdown.
	if publisher != nil {
		buildReapCtx, stopBuildReap := context.WithCancel(context.Background())
		buildReapDone := make(chan struct{})
		go func() {
			defer close(buildReapDone)
			publisher.ReapStuckBuilds(buildReapCtx, cfg.BuildTimeout, cfg.BuildReapGrace)
		}()
		defer func() {
			stopBuildReap()
			<-buildReapDone
		}()
		logger.Info("build recovery sweep started", "per_service_timeout", cfg.BuildTimeout, "grace", cfg.BuildReapGrace)
	}

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           server.New(logger, cfg, session.PlayHandler(sessions, fixtureProjectID), ready, authn, projects),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shut down gracefully on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, stop, logger, srv, sessions, publisher, cfg.Addr(), cfg.Env); err != nil {
		os.Exit(1)
	}
}

// builderFunc adapts a function to project.ProjectBuilder.
type builderFunc func(ctx context.Context, ownerLogin, projectID string, m runcontract.Manifest) (string, error)

func (f builderFunc) BuildProject(ctx context.Context, ownerLogin, projectID string, m runcontract.Manifest) (string, error) {
	return f(ctx, ownerLogin, projectID, m)
}

// gitBuildSpec resolves an Owner service to a BuildSpec backed by a github.com clone
// (ADR-0008/0010). commit pins the revision (the play path); empty resolves the
// default-branch HEAD (publish). The installation token is minted lazily — eagerly
// only to resolve HEAD — so a cache-hit play does no network at all; the Context
// closure clones at exactly the SHA and strips .git, so the token never reaches the
// read-only build context.
func gitBuildSpec(ctx context.Context, gh *githubapp.Client, projectID string, svc runcontract.Service, commit string) (builder.BuildSpec, error) {
	if gh == nil {
		return builder.BuildSpec{}, fmt.Errorf("building owner repos is not configured")
	}
	owner, repo, err := githubapp.ParseRepo(svc.Repo)
	if err != nil {
		return builder.BuildSpec{}, err
	}
	sha, resolveTok := commit, ""
	if sha == "" {
		tok, _, err := gh.InstallationToken(ctx, owner, repo)
		if err != nil {
			return builder.BuildSpec{}, err
		}
		resolveTok = tok
		if sha, err = githubapp.ResolveCommit(ctx, owner, repo, "HEAD", tok); err != nil {
			return builder.BuildSpec{}, err
		}
	}
	return builder.BuildSpec{
		ImageName:  "showcase/" + projectID + "-" + svc.Name,
		Version:    sha,
		Dockerfile: svc.Dockerfile,
		Context: func() (string, func(), error) {
			tok := resolveTok
			if tok == "" {
				t, _, err := gh.InstallationToken(ctx, owner, repo)
				if err != nil {
					return "", nil, err
				}
				tok = t
			}
			return githubapp.CloneCommit(ctx, owner, repo, sha, tok)
		},
	}, nil
}

// pruneLoginSessions periodically deletes expired web login sessions until ctx is
// cancelled. Errors are logged, not fatal — a missed prune only delays cleanup,
// and expired sessions are already rejected at query time.
func pruneLoginSessions(ctx context.Context, st *store.Store, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if n, err := st.DeleteExpiredLoginSessions(ctx); err != nil {
			if ctx.Err() == nil {
				logger.Warn("prune expired login sessions", "err", err)
			}
		} else if n > 0 {
			logger.Info("pruned expired login sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// run serves srv until ctx is cancelled (signal) or the listener fails, then
// shuts down gracefully. It returns a non-nil error when the server failed to
// run — a failed bind (port in use) must surface as a non-zero exit, otherwise a
// control plane that never came up looks like a clean start to a supervisor.
func run(ctx context.Context, stop context.CancelFunc, logger *slog.Logger, srv *http.Server, sessions *session.Manager, publisher *project.Publisher, addr, env string) error {
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

	// Likewise cancel and drain any in-flight publish builds: an aborted build cleans up
	// its sandbox and is left in 'building' for the startup recovery sweep to reclaim (#49).
	if publisher != nil {
		if err := publisher.Shutdown(shutdownCtx); err != nil {
			logger.Error("publisher drain incomplete", "err", err)
		}
	}

	// A signal-driven shutdown is success; a shutdown forced by a server error is not.
	select {
	case err := <-serveErr:
		return err
	default:
		return nil
	}
}
