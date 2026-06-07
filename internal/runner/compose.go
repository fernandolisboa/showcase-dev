package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fernandolisboa/showcase-dev/internal/proxy"
	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

// Project is a Project's compiled inputs: the Owner's run-contract Manifest plus
// the prebuilt image ref per service (ADR-0003: build at publish, run from cache).
type Project struct {
	Manifest runcontract.Manifest
	Images   map[string]string
}

// ProjectSource resolves a Project by id. In the MVP this is a static fixture
// (#8); later it reads the Owner's configured Projects.
type ProjectSource interface {
	Project(ctx context.Context, projectID string) (Project, error)
}

// Compose is a Runner that boots each Session as a generated, locked-down Docker
// Compose Stack and tears it down by project name. It shells out to
// `docker compose` behind the ADR-0001 Runner interface (ADR-0009); the
// in-process Compose SDK is the documented upgrade path.
type Compose struct {
	source    ProjectSource
	runtime   string
	workdir   string
	bootLimit string          // --wait-timeout for `compose up`
	registry  *proxy.Registry // optional; nil = no proxy integration (e.g. #8 tests)
	traefik   string          // Traefik container to attach to each Session network
}

// Option configures a Compose runner.
type Option func(*Compose)

// WithRuntime sets the container runtime: "runsc" (gVisor, prod) or "runc"
// (local dev where gVisor is absent) — ADR-0002/0009.
func WithRuntime(runtime string) Option {
	return func(c *Compose) { c.runtime = runtime }
}

// WithWorkdir sets the base dir for generated compose files.
func WithWorkdir(dir string) Option {
	return func(c *Compose) { c.workdir = dir }
}

// WithProxy wires the Runner to a Traefik proxy: it keeps the registry's
// sessionId->backend map current (ADR-0004) and attaches the named Traefik
// container to each Session's network so Traefik can reach the live Stack.
func WithProxy(registry *proxy.Registry, traefikContainer string) Option {
	return func(c *Compose) {
		c.registry = registry
		c.traefik = traefikContainer
	}
}

// NewCompose builds a Compose runner over the given ProjectSource.
func NewCompose(source ProjectSource, opts ...Option) *Compose {
	c := &Compose{
		source:    source,
		runtime:   "runsc",
		workdir:   filepath.Join(os.TempDir(), "showcase-sessions"),
		bootLimit: "120",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

var _ Runner = (*Compose)(nil)

// ProjectName is the compose project name for a Session — its lifecycle handle.
func ProjectName(sessionID string) string { return "s-" + sessionID }

// NetworkName is the per-Session bridge network — the network the proxy attaches
// to in #9.
func NetworkName(sessionID string) string { return ProjectName(sessionID) + "-net" }

// Provision boots the Project's Stack for sessionID and returns the in-network
// URL of its UI. `up --wait` gates on healthchecks (ADR-0004); a failed boot is
// torn down so it leaks nothing (ADR-0006).
func (c *Compose) Provision(ctx context.Context, projectID, sessionID string) (string, error) {
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	proj, err := c.source.Project(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("resolve project %q: %w", projectID, err)
	}

	plan, url, secrets, err := c.buildPlan(proj, sessionID)
	if err != nil {
		return "", err
	}
	compose, err := plan.ToCompose()
	if err != nil {
		return "", fmt.Errorf("render compose: %w", err)
	}

	dir := filepath.Join(c.workdir, ProjectName(sessionID))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, compose, 0o600); err != nil {
		return "", fmt.Errorf("write compose: %w", err)
	}

	// Register the Session's backends so they're staged on the route and become
	// reachable the moment Promote flips it Live (ADR-0004). When a session.Manager
	// drives this Runner it has already Added the id as a placeholder (so the route
	// exists synchronously before Play returns); this Add overwrites that
	// placeholder while still Booting — a no-op for routing, since Booting routes
	// ignore backends. This Add is also what registers the route when the Runner is
	// driven standalone (no Manager — see the runner integration tests), so it
	// can't be removed without breaking that path.
	if c.registry != nil {
		ui, apis := sessionBackends(proj, sessionID)
		c.registry.Add(sessionID, ui, apis)
	}

	if out, err := c.compose(ctx, ProjectName(sessionID),
		"-f", file, "up", "-d", "--wait", "--wait-timeout", c.bootLimit); err != nil {
		// Best-effort cleanup so a half-booted Stack leaves nothing behind.
		_ = c.Teardown(context.WithoutCancel(ctx), sessionID)
		// Postgres/compose can echo env (incl. POSTGRES_PASSWORD) on an init
		// failure; ADR-0006 routes this output to the Owner, so scrub minted
		// secrets before surfacing.
		return "", fmt.Errorf("compose up: %w\n%s", err, scrubSecrets(string(out), secrets))
	}

	// The network now exists: attach the proxy to it and flip the Session live.
	if c.registry != nil {
		if err := c.attachProxy(ctx, sessionID); err != nil {
			_ = c.Teardown(context.WithoutCancel(ctx), sessionID)
			return "", fmt.Errorf("attach proxy: %w", err)
		}
		c.registry.Promote(sessionID)
	}
	return url, nil
}

// Teardown destroys the Session's Stack — containers, the network, and volumes —
// by project name, and removes its generated compose file.
func (c *Compose) Teardown(ctx context.Context, sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	project := ProjectName(sessionID)
	// Detach Traefik first: `down` removes the Session network, which fails while an
	// external endpoint (Traefik) is still connected.
	if c.registry != nil {
		c.detachProxy(ctx, sessionID)
	}
	out, err := c.compose(ctx, project, "down", "-v", "--remove-orphans")
	_ = os.RemoveAll(filepath.Join(c.workdir, project))
	if err != nil {
		// Leave the route in place on failure: a Session whose Stack did NOT come
		// down must stay visible (in the registry, and so to the reaper) to be
		// retried, rather than being dropped while its containers leak (ADR-0006).
		// The boot-failure path drops the route separately (session.Manager.boot),
		// so this only affects retryable teardowns.
		return fmt.Errorf("compose down: %w\n%s", err, out)
	}
	if c.registry != nil {
		c.registry.Remove(sessionID)
	}
	return nil
}

// Health reports whether the Session's Stack is healthy: every service container
// running, and any with a healthcheck reporting healthy. A missing, exited,
// restarting, or unhealthy container makes the Stack unhealthy — the crash signal
// the reaper acts on after a grace window (ADR-0006, #13).
func (c *Compose) Health(ctx context.Context, sessionID string) (bool, error) {
	if err := validateSessionID(sessionID); err != nil {
		return false, err
	}
	// `ps --all` so exited/dead containers show up (a plain `ps` hides them, which
	// would make a crashed Stack look healthy by omission). --format json so the
	// state is parseable rather than scraped from a table.
	out, err := c.compose(ctx, ProjectName(sessionID), "ps", "--all", "--format", "json")
	if err != nil {
		return false, fmt.Errorf("compose ps: %w\n%s", err, out)
	}
	services, err := parseComposePS(out)
	if err != nil {
		return false, fmt.Errorf("parse compose ps: %w", err)
	}
	return healthyFromPS(services), nil
}

// composePS is the subset of `docker compose ps --format json` the health check
// needs: per-container run state and healthcheck status.
type composePS struct {
	Service string `json:"Service"`
	State   string `json:"State"`  // running, exited, restarting, dead, created, paused
	Health  string `json:"Health"` // healthy, unhealthy, starting, or "" (no healthcheck)
}

// parseComposePS tolerates both shapes Compose has emitted: a single JSON array,
// and newline-delimited JSON objects (one per container). Blank lines and any
// non-JSON noise (e.g. a stray warning on the combined stream) are skipped.
func parseComposePS(out []byte) ([]composePS, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []composePS
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, err
		}
		return arr, nil
	}
	var services []composePS
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var s composePS
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return nil, err
		}
		services = append(services, s)
	}
	return services, nil
}

// healthyFromPS decides Stack health from the container list: no containers means
// the Stack is gone (unhealthy); otherwise every container must be running and not
// report an unhealthy/starting healthcheck. "starting" counts as not-yet-healthy
// so a flapping restart reads as unhealthy and the reaper's grace window decides.
func healthyFromPS(services []composePS) bool {
	if len(services) == 0 {
		return false
	}
	for _, s := range services {
		if s.State != "running" {
			return false
		}
		if s.Health == "unhealthy" || s.Health == "starting" {
			return false
		}
	}
	return true
}

// buildPlan mints any per-Session secrets and compiles the locked-down plan. It
// returns the plan, the UI's in-network URL, and the minted secret values so the
// caller can scrub them from any surfaced compose output.
func (c *Compose) buildPlan(proj Project, sessionID string) (runcontract.ExecutionPlan, string, []string, error) {
	opts := runcontract.Options{
		Project:     ProjectName(sessionID),
		NetworkName: NetworkName(sessionID),
		Runtime:     c.runtime,
		Images:      proj.Images,
	}

	var url string
	for _, s := range proj.Manifest.Services {
		if s.Role == runcontract.RoleUI {
			url = fmt.Sprintf("http://%s:%d", s.Name, s.Port)
		}
	}

	var secrets []string
	if proj.Manifest.DB != nil {
		password, err := randToken(16)
		if err != nil {
			return runcontract.ExecutionPlan{}, "", nil, fmt.Errorf("mint db password: %w", err)
		}
		creds := runcontract.DBCreds{User: "showcase", Password: password, Database: "showcase"}
		opts.DBCreds = creds
		secrets = append(secrets, password)
		platformEnv, err := resolvePlatformEnv(proj.Manifest, creds)
		if err != nil {
			return runcontract.ExecutionPlan{}, "", nil, err
		}
		opts.PlatformEnv = platformEnv
	}

	plan, err := runcontract.Compile(proj.Manifest, opts)
	if err != nil {
		return runcontract.ExecutionPlan{}, "", nil, err
	}
	return plan, url, secrets, nil
}

// resolvePlatformEnv mints values for the Manifest's platform-sourced env vars.
// Unknown platform vars are left unset so Compile surfaces the gap rather than
// injecting an empty value.
func resolvePlatformEnv(m runcontract.Manifest, creds runcontract.DBCreds) (map[string]string, error) {
	env := map[string]string{}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable",
		creds.User, creds.Password, dbHost, creds.Database)
	for _, e := range m.Env {
		if e.Source != runcontract.EnvPlatform {
			continue
		}
		switch e.Name {
		case "DATABASE_URL":
			env[e.Name] = dsn
		default:
			return nil, fmt.Errorf("no platform value known for env %q", e.Name)
		}
	}
	return env, nil
}

// dbHost is the in-Stack hostname of the database service (its compose name).
const dbHost = "db"

func validateSessionID(id string) error {
	if id == "" {
		return errors.New("session id is required")
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok {
			return fmt.Errorf("session id %q: only lowercase letters, digits, and '-' allowed", id)
		}
	}
	return nil
}

func (c *Compose) compose(ctx context.Context, project string, args ...string) ([]byte, error) {
	return dockerCmd(ctx, append([]string{"compose", "-p", project}, args...)...)
}

func dockerCmd(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// sessionBackends derives the proxy backends from the Manifest. Each container is
// reachable on the Session network by its compose container name
// (<project>-<service>-1), which is unique across Sessions — unlike the bare
// service name, which would collide when the proxy joins many Session networks.
func sessionBackends(proj Project, sessionID string) (proxy.Backend, []proxy.Backend) {
	project := ProjectName(sessionID)
	var ui proxy.Backend
	var apis []proxy.Backend
	for _, s := range proj.Manifest.Services {
		url := fmt.Sprintf("http://%s-%s-1:%d", project, s.Name, s.Port)
		switch s.Role {
		case runcontract.RoleUI:
			ui = proxy.Backend{URL: url}
		case runcontract.RoleAPI:
			apis = append(apis, proxy.Backend{PathPrefix: s.PathPrefix, URL: url})
		}
	}
	return ui, apis
}

func (c *Compose) attachProxy(ctx context.Context, sessionID string) error {
	if c.traefik == "" {
		return nil
	}
	if out, err := dockerCmd(ctx, "network", "connect", NetworkName(sessionID), c.traefik); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func (c *Compose) detachProxy(ctx context.Context, sessionID string) {
	if c.traefik == "" {
		return
	}
	_, _ = dockerCmd(ctx, "network", "disconnect", "-f", NetworkName(sessionID), c.traefik)
}

// scrubSecrets replaces every occurrence of each secret value in s with a
// redaction marker, so plaintext creds never reach a surfaced error or log.
func scrubSecrets(s string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	return s
}

// randToken returns n bytes of crypto/rand entropy, hex-encoded. It fails closed:
// a rand.Read error is propagated so a secret is never minted from a degraded
// (e.g. all-zero) buffer.
func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
