// Package runcontract compiles a Project's run contract (the Owner-declared
// Manifest, ADR-0003) into a locked-down ExecutionPlan: the Compose Stack the
// Runner boots. The compiler is pure — Manifest + Options in, Plan out, no I/O
// and no randomness — so it is exhaustively testable at "Seam 3" without Docker.
// It bakes ADR-0002/0003/0004/0007 invariants in structurally, so an Owner
// cannot opt out of gVisor, resource caps, network isolation, or non-root.
package runcontract

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// serviceNameRE constrains Owner service names to a safe charset so a name can
// never evade the reserved-name guard (e.g. "DB", "db ") or break the generated
// compose. (PR #22 re-review follow-up.)
var serviceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Role is a service's role in the Stack.
type Role string

const (
	// RoleUI is the single user-facing service the proxy serves at "/".
	RoleUI Role = "ui"
	// RoleAPI is a backend the proxy serves under a path prefix.
	RoleAPI Role = "api"
)

// EnvSource declares where an env var's value comes from (ADR-0003).
type EnvSource string

const (
	// EnvPlatform values are minted per Session by the platform (DB creds, etc.).
	EnvPlatform EnvSource = "platform"
	// EnvStatic values are plaintext config carried in the Manifest.
	EnvStatic EnvSource = "static"
	// EnvOwner values are encrypted, test-only, and deferred in the MVP — the
	// compiler never injects them (ADR-0003).
	EnvOwner EnvSource = "owner"
)

// Manifest is the run contract: what the Owner declares about a Project.
type Manifest struct {
	Services []Service
	DB       *DB
	Env      []EnvVar
	// Seed is an optional one-shot run against the fresh DB before readiness.
	Seed *Seed
}

// Service is one container in the Stack, built from the Owner's repo.
type Service struct {
	Name       string
	Repo       string
	Dockerfile string
	Port       int
	Role       Role
	// PathPrefix is where the proxy mounts an API (e.g. "/api"). UI services
	// ignore it (they are served at "/").
	PathPrefix string
}

// DB declares an in-Stack database the platform provisions fresh per Session.
type DB struct {
	Engine  string // only "postgres" in the MVP
	Version string
}

// EnvVar is one environment variable a service needs.
type EnvVar struct {
	Name   string
	Source EnvSource
	// Value is used only for EnvStatic.
	Value  string
	Secret bool
}

// Seed is a one-shot command run against the fresh DB before the app is ready.
type Seed struct {
	// Service names the Stack service whose image carries the seed command.
	Service string
	Command []string
}

// supportedDBEngines are the engines the platform can provision in the MVP.
var supportedDBEngines = map[string]bool{"postgres": true}

// reservedServiceNames are the names the compiler generates for platform-owned
// services (the trusted Postgres and the seed one-shot). An Owner service may
// not use them: ToCompose keys services by name, so a collision would let an
// Owner-controlled image silently overwrite a trusted platform service — the
// "no untrusted code in trusted slots" invariant (ADR-0003). Validate rejects
// them so the collision can never reach the renderer.
var reservedServiceNames = map[string]bool{
	dbServiceName:   true,
	seedServiceName: true,
}

// Validate reports every problem with the Manifest at once (ADR-0003: the
// platform builds strictly from an explicit, well-formed contract).
func (m Manifest) Validate() error {
	var errs []error

	uiCount := 0
	seen := map[string]bool{}
	prefixes := map[string]string{}
	for i, s := range m.Services {
		where := fmt.Sprintf("service[%d]", i)
		if s.Name != "" {
			where = fmt.Sprintf("service %q", s.Name)
		}
		if s.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", where))
		} else if seen[s.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate service name", where))
		} else if reservedServiceNames[s.Name] {
			errs = append(errs, fmt.Errorf("%s: name is reserved for a platform service", where))
		} else if !serviceNameRE.MatchString(s.Name) {
			errs = append(errs, fmt.Errorf("%s: name must match [a-z0-9-] and start alphanumeric", where))
		}
		seen[s.Name] = true

		if s.Repo == "" {
			errs = append(errs, fmt.Errorf("%s: repo is required", where))
		}
		if s.Dockerfile == "" {
			errs = append(errs, fmt.Errorf("%s: dockerfile is required", where))
		}
		if s.Port < 1 || s.Port > 65535 {
			errs = append(errs, fmt.Errorf("%s: port %d out of range 1-65535", where, s.Port))
		}

		switch s.Role {
		case RoleUI:
			uiCount++
		case RoleAPI:
			prefix := s.PathPrefix
			if prefix == "" {
				errs = append(errs, fmt.Errorf("%s: api service needs a pathPrefix", where))
			} else if !strings.HasPrefix(prefix, "/") {
				errs = append(errs, fmt.Errorf("%s: pathPrefix %q must start with /", where, prefix))
			} else if other, dup := prefixes[prefix]; dup {
				errs = append(errs, fmt.Errorf("%s: pathPrefix %q collides with service %q", where, prefix, other))
			} else {
				prefixes[prefix] = s.Name
			}
		default:
			errs = append(errs, fmt.Errorf("%s: role must be %q or %q, got %q", where, RoleUI, RoleAPI, s.Role))
		}
	}

	if len(m.Services) == 0 {
		errs = append(errs, errors.New("at least one service is required"))
	} else if uiCount != 1 {
		errs = append(errs, fmt.Errorf("exactly one ui service is required, got %d", uiCount))
	}

	if m.DB != nil {
		if m.DB.Engine == "" {
			errs = append(errs, errors.New("db.engine is required when db is set"))
		} else if !supportedDBEngines[m.DB.Engine] {
			errs = append(errs, fmt.Errorf("db.engine %q is unsupported (MVP: postgres)", m.DB.Engine))
		}
		if m.DB.Version == "" {
			errs = append(errs, errors.New("db.version is required when db is set"))
		}
	}

	for i, e := range m.Env {
		where := fmt.Sprintf("env[%d]", i)
		if e.Name != "" {
			where = fmt.Sprintf("env %q", e.Name)
		}
		if e.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", where))
		}
		switch e.Source {
		case EnvPlatform, EnvStatic, EnvOwner:
		default:
			errs = append(errs, fmt.Errorf("%s: source must be platform|static|owner, got %q", where, e.Source))
		}
	}

	if m.Seed != nil {
		if m.Seed.Service == "" {
			errs = append(errs, errors.New("seed.service is required when seed is set"))
		} else if !seen[m.Seed.Service] {
			errs = append(errs, fmt.Errorf("seed.service %q is not a declared service", m.Seed.Service))
		}
		if len(m.Seed.Command) == 0 {
			errs = append(errs, errors.New("seed.command is required when seed is set"))
		}
		if m.DB == nil {
			errs = append(errs, errors.New("seed requires a db"))
		}
	}

	return errors.Join(errs...)
}
