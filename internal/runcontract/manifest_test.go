package runcontract

import (
	"strings"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		Services: []Service{
			{Name: "web", Repo: "o/web", Dockerfile: "Dockerfile", Port: 80, Role: RoleUI},
			{Name: "api", Repo: "o/api", Dockerfile: "Dockerfile", Port: 8080, Role: RoleAPI, PathPrefix: "/api"},
		},
		DB:  &DB{Engine: "postgres", Version: "17"},
		Env: []EnvVar{{Name: "LOG_LEVEL", Source: EnvStatic, Value: "info"}},
	}
}

func TestValidateAcceptsAGoodManifest(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Manifest)
		want   string
	}{
		"no services":        {func(m *Manifest) { m.Services = nil }, "at least one service"},
		"no ui":              {func(m *Manifest) { m.Services[0].Role = RoleAPI; m.Services[0].PathPrefix = "/x" }, "exactly one ui"},
		"two ui":             {func(m *Manifest) { m.Services[1].Role = RoleUI }, "exactly one ui"},
		"bad role":           {func(m *Manifest) { m.Services[1].Role = "worker" }, "role must be"},
		"api no prefix":      {func(m *Manifest) { m.Services[1].PathPrefix = "" }, "needs a pathPrefix"},
		"prefix no slash":    {func(m *Manifest) { m.Services[1].PathPrefix = "api" }, "must start with /"},
		"missing repo":       {func(m *Manifest) { m.Services[0].Repo = "" }, "repo is required"},
		"missing dockerfile": {func(m *Manifest) { m.Services[0].Dockerfile = "" }, "dockerfile is required"},
		"port range":         {func(m *Manifest) { m.Services[0].Port = 0 }, "out of range"},
		"dup name":           {func(m *Manifest) { m.Services[1].Name = "web"; m.Services[1].Role = RoleAPI }, "duplicate service name"},
		"bad db engine":      {func(m *Manifest) { m.DB.Engine = "mysql" }, "unsupported"},
		"db no version":      {func(m *Manifest) { m.DB.Version = "" }, "db.version is required"},
		"bad env source":     {func(m *Manifest) { m.Env = []EnvVar{{Name: "X", Source: "magic"}} }, "source must be"},
		"env no name":        {func(m *Manifest) { m.Env = []EnvVar{{Source: EnvStatic}} }, "name is required"},
		"seed no db":         {func(m *Manifest) { m.DB = nil; m.Seed = &Seed{Service: "api", Command: []string{"x"}} }, "seed requires a db"},
		"seed bad service":   {func(m *Manifest) { m.Seed = &Seed{Service: "ghost", Command: []string{"x"}} }, "not a declared service"},
		"seed no command":    {func(m *Manifest) { m.Seed = &Seed{Service: "api"} }, "seed.command is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			tc.mutate(&m)
			err := m.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestValidateReportsAllProblemsAtOnce(t *testing.T) {
	m := Manifest{Services: []Service{{Role: RoleAPI}}} // missing name, repo, dockerfile, port, prefix, no ui
	err := m.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"name is required", "repo is required", "exactly one ui"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error missing %q in:\n%s", want, err.Error())
		}
	}
}

func TestValidateServiceNameCharset(t *testing.T) {
	// Rejected, including reserved-guard near-misses ("DB", "db ").
	for _, name := range []string{"API", "DB", "my_api", "has space", "db ", "-lead", "sym$bol"} {
		m := validManifest()
		m.Services[1].Name = name
		if err := m.Validate(); err == nil {
			t.Errorf("service name %q should be rejected", name)
		}
	}
	// Valid names pass.
	for _, name := range []string{"api", "api-2", "web3"} {
		m := validManifest()
		m.Services[1].Name = name
		if err := m.Validate(); err != nil {
			t.Errorf("service name %q should be valid: %v", name, err)
		}
	}
}
