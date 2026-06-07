// Package runner is the spine seam from ADR-0001: every Session's lifecycle —
// provisioning, supervising, teardown — goes through this narrow interface, so
// the execution mechanism (a generated, locked-down Docker Compose Stack driven
// by shelling out to `docker compose`, per ADR-0009) can change without
// touching callers, routing, or the run contract. The real implementation
// lands in issue #8; this package defines only the interface and a stub.
package runner

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by the placeholder Runner until issue #8.
var ErrNotImplemented = errors.New("runner: not implemented")

// Runner provisions and tears down a Session's Stack.
type Runner interface {
	// Provision boots the Project's Stack for sessionID and returns the URL at
	// which the Session is reachable once its healthcheck passes.
	Provision(ctx context.Context, projectID, sessionID string) (url string, err error)

	// Teardown destroys the Session's Stack and releases its resources.
	Teardown(ctx context.Context, sessionID string) error
}

// Stub is a no-op Runner used by the scaffold; it satisfies the interface and
// always reports ErrNotImplemented. Replaced by the Compose-driven Runner in #8.
type Stub struct{}

var _ Runner = Stub{}

// Provision implements Runner.
func (Stub) Provision(context.Context, string, string) (string, error) {
	return "", ErrNotImplemented
}

// Teardown implements Runner.
func (Stub) Teardown(context.Context, string) error {
	return ErrNotImplemented
}
