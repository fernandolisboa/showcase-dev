package runner

import (
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

func TestProjectAndNetworkName(t *testing.T) {
	if got := ProjectName("abc123"); got != "s-abc123" {
		t.Errorf("ProjectName = %q", got)
	}
	if got := NetworkName("abc123"); got != "s-abc123-net" {
		t.Errorf("NetworkName = %q", got)
	}
}

func TestValidateSessionID(t *testing.T) {
	valid := []string{"abc123", "a1b2c3", "with-dash", "0"}
	for _, id := range valid {
		if err := validateSessionID(id); err != nil {
			t.Errorf("validateSessionID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{"", "UPPER", "under_score", "has space", "sym$bol", "slash/x"}
	for _, id := range invalid {
		if err := validateSessionID(id); err == nil {
			t.Errorf("validateSessionID(%q) = nil, want error", id)
		}
	}
}

func TestBuildPlanWiring(t *testing.T) {
	c := NewCompose(StaticSource{P: FixtureProject()}, WithRuntime("runc"))

	plan, url, err := c.buildPlan(FixtureProject(), "abc123")
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	if plan.Project != "s-abc123" {
		t.Errorf("project = %q", plan.Project)
	}
	if plan.Network.Name != "s-abc123-net" || !plan.Network.Internal {
		t.Errorf("network = %+v, want named + internal", plan.Network)
	}
	if url != "http://web:8080" {
		t.Errorf("url = %q, want http://web:8080", url)
	}

	var web *runcontract.ServiceSpec
	for i := range plan.Services {
		if plan.Services[i].Name == "web" {
			web = &plan.Services[i]
		}
	}
	if web == nil {
		t.Fatal("web service missing from plan")
	}
	if web.Runtime != "runc" {
		t.Errorf("runtime = %q, want runc (override for local dev)", web.Runtime)
	}
}

func TestResolvePlatformEnv(t *testing.T) {
	creds := runcontract.DBCreds{User: "u", Password: "p", Database: "d"}

	m := runcontract.Manifest{Env: []runcontract.EnvVar{{Name: "DATABASE_URL", Source: runcontract.EnvPlatform}}}
	env, err := resolvePlatformEnv(m, creds)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got, want := env["DATABASE_URL"], "postgres://u:p@db:5432/d?sslmode=disable"; got != want {
		t.Errorf("DATABASE_URL = %q, want %q", got, want)
	}

	unknown := runcontract.Manifest{Env: []runcontract.EnvVar{{Name: "MYSTERY", Source: runcontract.EnvPlatform}}}
	if _, err := resolvePlatformEnv(unknown, creds); err == nil {
		t.Error("expected error for an unknown platform env var")
	}
}

func TestRandTokenIsRandomHex(t *testing.T) {
	a, b := randToken(16), randToken(16)
	if len(a) != 32 {
		t.Errorf("len = %d, want 32 hex chars", len(a))
	}
	if a == b {
		t.Error("two tokens should differ")
	}
}
