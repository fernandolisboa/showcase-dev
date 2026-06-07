package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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
	bootLimit string // --wait-timeout for `compose up`
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

	plan, url, err := c.buildPlan(proj, sessionID)
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

	if out, err := c.compose(ctx, ProjectName(sessionID),
		"-f", file, "up", "-d", "--wait", "--wait-timeout", c.bootLimit); err != nil {
		// Best-effort cleanup so a half-booted Stack leaves nothing behind.
		_ = c.Teardown(context.WithoutCancel(ctx), sessionID)
		return "", fmt.Errorf("compose up: %w\n%s", err, out)
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
	out, err := c.compose(ctx, project, "down", "-v", "--remove-orphans")
	_ = os.RemoveAll(filepath.Join(c.workdir, project))
	if err != nil {
		return fmt.Errorf("compose down: %w\n%s", err, out)
	}
	return nil
}

// buildPlan mints any per-Session secrets and compiles the locked-down plan. It
// returns the plan and the UI's in-network URL.
func (c *Compose) buildPlan(proj Project, sessionID string) (runcontract.ExecutionPlan, string, error) {
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

	if proj.Manifest.DB != nil {
		creds := runcontract.DBCreds{User: "showcase", Password: randToken(16), Database: "showcase"}
		opts.DBCreds = creds
		platformEnv, err := resolvePlatformEnv(proj.Manifest, creds)
		if err != nil {
			return runcontract.ExecutionPlan{}, "", err
		}
		opts.PlatformEnv = platformEnv
	}

	plan, err := runcontract.Compile(proj.Manifest, opts)
	if err != nil {
		return runcontract.ExecutionPlan{}, "", err
	}
	return plan, url, nil
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
	full := append([]string{"compose", "-p", project}, args...)
	return exec.CommandContext(ctx, "docker", full...).CombinedOutput()
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
