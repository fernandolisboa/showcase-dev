package project

import (
	"errors"
	"fmt"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

// Form is the Owner-facing project configuration the API accepts. It is the public
// wire contract — explicit lowercase json tags — and is mapped to a
// runcontract.Manifest server-side. The internal run contract (runcontract.Manifest)
// carries no json tags, so going through Form means we never leak Go field names as
// the API and we curate which manifest features an Owner may set at this stage.
type Form struct {
	Name     string        `json:"name"`
	Services []FormService `json:"services"`
	DB       *FormDB       `json:"db,omitempty"`
	Env      []FormEnv     `json:"env,omitempty"`
	Seed     *FormSeed     `json:"seed,omitempty"`
	Egress   []FormEgress  `json:"egress,omitempty"`
}

// FormService is one container the Owner declares: a repo + Dockerfile + port and
// its role in the proxy topology. Building it from source happens at publish (a
// later slice); here it is only stored.
type FormService struct {
	Name        string   `json:"name"`
	Repo        string   `json:"repo"`
	Dockerfile  string   `json:"dockerfile"`
	Port        int      `json:"port"`
	Role        string   `json:"role"` // "ui" | "api"
	PathPrefix  string   `json:"pathPrefix,omitempty"`
	Healthcheck []string `json:"healthcheck,omitempty"`
}

// FormDB is the optional in-Stack database (Postgres only in the MVP).
type FormDB struct {
	Engine  string `json:"engine"`
	Version string `json:"version"`
}

// FormEnv is one environment variable. Source is "platform" (minted per Session) or
// "static" (carried in the manifest); owner-sourced secrets are deferred (ADR-0003)
// and rejected here so we never persist a feature the platform cannot honor yet.
// Secret is inert metadata in the MVP — there is no encryption for static values
// yet, so marking a static var secret does not change how it is stored.
type FormEnv struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

// FormSeed is the optional one-shot command run against the fresh DB before the app
// is ready.
type FormSeed struct {
	Service string   `json:"service"`
	Command []string `json:"command"`
}

// FormEgress is one allowed outbound destination for the runtime allow-list
// (ADR-0011): a bare host and a port.
type FormEgress struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// errEnvSourceUnsupported rejects env sources the MVP cannot honor at this stage.
// Owner-sourced secrets (EnvOwner) are deferred (ADR-0003): the compiler never
// injects them, so accepting one would silently drop the value at run time.
var errEnvSourceUnsupported = errors.New(`env source must be "platform" or "static"`)

// toManifest maps the Owner form to the internal run contract. It does not validate
// the manifest's structure — the caller runs runcontract.Manifest.Validate, the
// single source of manifest validation (don't reinvent it). It only enforces the
// curation this layer owns: the supported env sources.
func (f Form) toManifest() (runcontract.Manifest, error) {
	var m runcontract.Manifest

	for _, s := range f.Services {
		m.Services = append(m.Services, runcontract.Service{
			Name:        s.Name,
			Repo:        s.Repo,
			Dockerfile:  s.Dockerfile,
			Port:        s.Port,
			Role:        runcontract.Role(s.Role),
			PathPrefix:  s.PathPrefix,
			Healthcheck: s.Healthcheck,
		})
	}

	if f.DB != nil {
		m.DB = &runcontract.DB{Engine: f.DB.Engine, Version: f.DB.Version}
	}

	for _, e := range f.Env {
		switch e.Source {
		case string(runcontract.EnvPlatform), string(runcontract.EnvStatic):
		default:
			return runcontract.Manifest{}, fmt.Errorf("env %q: %w", e.Name, errEnvSourceUnsupported)
		}
		m.Env = append(m.Env, runcontract.EnvVar{
			Name:   e.Name,
			Source: runcontract.EnvSource(e.Source),
			Value:  e.Value,
			Secret: e.Secret,
		})
	}

	if f.Seed != nil {
		m.Seed = &runcontract.Seed{Service: f.Seed.Service, Command: f.Seed.Command}
	}

	for _, g := range f.Egress {
		m.Egress = append(m.Egress, runcontract.EgressRule{Host: g.Host, Port: g.Port})
	}

	return m, nil
}

// formFromManifest is the reverse of toManifest: it projects an internal run contract
// back onto the curated Owner Form so the API can echo a stored Project's configuration
// as an editable Form (the edit UI reads it, edits it, and PUTs it back — #51). It is a
// faithful inverse of toManifest at the Manifest level — every field toManifest carries,
// this restores — EXCEPT Form.Name, which is not a manifest field (the Project name is a
// separate column), so the caller sets it. It does no validation: toManifest re-validates
// on the way back in, and the source manifest was already validated when it was stored.
func formFromManifest(m runcontract.Manifest) Form {
	var f Form

	for _, s := range m.Services {
		f.Services = append(f.Services, FormService{
			Name:        s.Name,
			Repo:        s.Repo,
			Dockerfile:  s.Dockerfile,
			Port:        s.Port,
			Role:        string(s.Role),
			PathPrefix:  s.PathPrefix,
			Healthcheck: s.Healthcheck,
		})
	}

	if m.DB != nil {
		f.DB = &FormDB{Engine: m.DB.Engine, Version: m.DB.Version}
	}

	for _, e := range m.Env {
		f.Env = append(f.Env, FormEnv{
			Name:   e.Name,
			Source: string(e.Source),
			Value:  e.Value,
			Secret: e.Secret,
		})
	}

	if m.Seed != nil {
		f.Seed = &FormSeed{Service: m.Seed.Service, Command: m.Seed.Command}
	}

	for _, g := range m.Egress {
		f.Egress = append(f.Egress, FormEgress{Host: g.Host, Port: g.Port})
	}

	return f
}
