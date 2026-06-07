package runner

import (
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
)

// TestSessionBackendsSplitsUIAndAPI covers the multi-service derivation (#15): the
// single UI service becomes the catch-all backend; each API service becomes a
// path-prefixed backend, addressed by its per-Session container name.
func TestSessionBackendsSplitsUIAndAPI(t *testing.T) {
	proj := Project{Manifest: runcontract.Manifest{Services: []runcontract.Service{
		{Name: "web", Port: 8080, Role: runcontract.RoleUI},
		{Name: "api", Port: 9090, Role: runcontract.RoleAPI, PathPrefix: "/api"},
	}}}

	ui, apis := sessionBackends(proj, "sess")

	if want := "http://s-sess-web-1:8080"; ui.URL != want {
		t.Errorf("UI url = %q, want %q", ui.URL, want)
	}
	if ui.PathPrefix != "" {
		t.Errorf("UI should have no path prefix, got %q", ui.PathPrefix)
	}
	if len(apis) != 1 {
		t.Fatalf("expected 1 API backend, got %d", len(apis))
	}
	if apis[0].PathPrefix != "/api" {
		t.Errorf("API prefix = %q, want /api", apis[0].PathPrefix)
	}
	if want := "http://s-sess-api-1:9090"; apis[0].URL != want {
		t.Errorf("API url = %q, want %q", apis[0].URL, want)
	}
}
