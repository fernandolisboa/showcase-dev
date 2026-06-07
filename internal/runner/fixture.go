package runner

import (
	"context"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

// FixtureProject is the hardcoded Project used to exercise the Runner end-to-end
// without an Owner-configured Project (#8). It pairs a tiny prebuilt HTTP echo
// image (traefik/whoami, set to a non-privileged port so it runs as a non-root,
// all-caps-dropped user) with a fresh Postgres — so a boot exercises an app
// service, the platform DB (healthcheck + ephemeral volume), resource caps, the
// isolated network, and dependency ordering (the app waits for the DB healthy).
func FixtureProject() Project {
	return Project{
		Manifest: runcontract.Manifest{
			Services: []runcontract.Service{
				{Name: "web", Repo: "internal/fixture", Dockerfile: "Dockerfile", Port: 8080, Role: runcontract.RoleUI},
			},
			DB: &runcontract.DB{Engine: "postgres", Version: "17"},
			Env: []runcontract.EnvVar{
				{Name: "WHOAMI_PORT_NUMBER", Source: runcontract.EnvStatic, Value: "8080"},
			},
		},
		Images: map[string]string{"web": "traefik/whoami:latest"},
	}
}

// StaticSource is a ProjectSource that always returns the same Project.
type StaticSource struct{ P Project }

// Project implements ProjectSource.
func (s StaticSource) Project(context.Context, string) (Project, error) { return s.P, nil }
