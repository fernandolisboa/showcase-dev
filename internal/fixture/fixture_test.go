package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

func TestManifestIsValidUIPlusAPI(t *testing.T) {
	m := Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("fixture manifest must be valid: %v", err)
	}

	var ui, api *runcontract.Service
	for i := range m.Services {
		switch m.Services[i].Role {
		case runcontract.RoleUI:
			ui = &m.Services[i]
		case runcontract.RoleAPI:
			api = &m.Services[i]
		}
	}
	if ui == nil || api == nil {
		t.Fatalf("expected one UI and one API service, got %+v", m.Services)
	}
	if api.PathPrefix != "/api" {
		t.Errorf("API pathPrefix = %q, want /api (same-origin)", api.PathPrefix)
	}

	// #16: a Postgres DB and a platform-sourced DATABASE_URL for the API to connect.
	if m.DB == nil || m.DB.Engine != "postgres" {
		t.Errorf("expected a postgres DB, got %+v", m.DB)
	}
	var hasDBURL bool
	for _, e := range m.Env {
		if e.Name == "DATABASE_URL" && e.Source == runcontract.EnvPlatform {
			hasDBURL = true
		}
	}
	if !hasDBURL {
		t.Error("expected a platform-sourced DATABASE_URL env")
	}
}

func TestVersionPerServiceStableAndDistinct(t *testing.T) {
	web1, err := Version("web")
	if err != nil {
		t.Fatalf("version web: %v", err)
	}
	web2, _ := Version("web")
	if web1 != web2 {
		t.Errorf("version not stable: %q vs %q", web1, web2)
	}
	api, err := Version("api")
	if err != nil {
		t.Fatalf("version api: %v", err)
	}
	if web1 == api {
		t.Error("distinct services should hash to distinct versions")
	}
	if _, err := Version("nope"); err == nil {
		t.Error("unknown service should error")
	}
}

func TestExtractPerService(t *testing.T) {
	for _, svc := range []string{"web", "api"} {
		dir, cleanup, err := Extract(svc)
		if err != nil {
			t.Fatalf("extract %s: %v", svc, err)
		}
		if _, err := os.Stat(filepath.Join(dir, Dockerfile)); err != nil {
			t.Errorf("%s: expected Dockerfile in context: %v", svc, err)
		}
		cleanup()
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s: cleanup should remove the temp dir", svc)
		}
	}
}
