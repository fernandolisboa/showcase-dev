package runner

import (
	"strings"
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

	plan, url, secrets, err := c.buildPlan(FixtureProject(), "abc123")
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	if len(secrets) != 1 || secrets[0] == "" {
		t.Errorf("secrets = %v, want one non-empty minted db password", secrets)
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

func TestPerSessionDBCredsAreUnique(t *testing.T) {
	c := NewCompose(StaticSource{P: FixtureProject()}, WithRuntime("runc"))

	_, _, s1, err := c.buildPlan(FixtureProject(), "sess1")
	if err != nil {
		t.Fatalf("buildPlan 1: %v", err)
	}
	_, _, s2, err := c.buildPlan(FixtureProject(), "sess2")
	if err != nil {
		t.Fatalf("buildPlan 2: %v", err)
	}
	if len(s1) != 1 || len(s2) != 1 {
		t.Fatalf("expected one minted secret each, got %d and %d", len(s1), len(s2))
	}
	if s1[0] == s2[0] {
		t.Error("each Session must get unique generated DB credentials (#16)")
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
	a, err := randToken(16)
	if err != nil {
		t.Fatalf("randToken: %v", err)
	}
	b, err := randToken(16)
	if err != nil {
		t.Fatalf("randToken: %v", err)
	}
	if len(a) != 32 {
		t.Errorf("len = %d, want 32 hex chars", len(a))
	}
	if a == b {
		t.Error("two tokens should differ")
	}
}

func TestScrubSecretsRedactsMintedValues(t *testing.T) {
	out := "POSTGRES_PASSWORD=deadbeefcafef00d failed to init\nother line"
	got := scrubSecrets(out, []string{"deadbeefcafef00d", ""})
	if strings.Contains(got, "deadbeefcafef00d") {
		t.Errorf("secret not redacted: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("missing redaction marker: %q", got)
	}
	if !strings.Contains(got, "other line") {
		t.Errorf("non-secret text dropped: %q", got)
	}
}
