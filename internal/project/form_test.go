package project

import (
	"reflect"
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

// richManifest exercises every field the Form/Manifest mapping carries, so the
// round-trip test proves formFromManifest restores all of them (not just the common
// single-service case).
func richManifest() runcontract.Manifest {
	return runcontract.Manifest{
		Services: []runcontract.Service{
			{Name: "web", Repo: "github.com/me/app", Dockerfile: "ui/Dockerfile", Port: 8080, Role: runcontract.RoleUI},
			{
				Name: "api", Repo: "github.com/me/app", Dockerfile: "api/Dockerfile", Port: 9090,
				Role: runcontract.RoleAPI, PathPrefix: "/api",
				Healthcheck: []string{"CMD", "curl", "-f", "http://localhost:9090/health"},
			},
		},
		DB: &runcontract.DB{Engine: "postgres", Version: "17"},
		Env: []runcontract.EnvVar{
			{Name: "DATABASE_URL", Source: runcontract.EnvPlatform},
			{Name: "LOG_LEVEL", Source: runcontract.EnvStatic, Value: "info", Secret: false},
		},
		Seed:   &runcontract.Seed{Service: "api", Command: []string{"./seed.sh"}},
		Egress: []runcontract.EgressRule{{Host: "api.stripe.com", Port: 443}},
	}
}

// TestManifestFormRoundTrip proves formFromManifest is a faithful inverse of toManifest
// at the Manifest level: mapping a manifest to a Form and back yields the same manifest,
// so the edit UI can echo a stored Project and PUT it back losslessly (#51).
func TestManifestFormRoundTrip(t *testing.T) {
	m := richManifest()

	back, err := formFromManifest(m).toManifest()
	if err != nil {
		t.Fatalf("toManifest after formFromManifest: %v", err)
	}
	if !reflect.DeepEqual(m, back) {
		t.Errorf("round-trip lost data:\n  orig = %+v\n  back = %+v", m, back)
	}

	// Sanity: the Form maps back to a well-formed contract.
	if err := back.Validate(); err != nil {
		t.Errorf("round-tripped manifest no longer validates: %v", err)
	}
}
