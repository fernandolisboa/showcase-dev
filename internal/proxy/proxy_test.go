package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// sessionIDCharset mirrors the Runner's validateSessionID allowance.
var sessionIDCharset = regexp.MustCompile(`^[a-z0-9-]+$`)

func TestNewSessionIDUnguessableAndValid(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID: %v", err)
		}
		if !sessionIDCharset.MatchString(id) {
			t.Fatalf("session id %q has chars outside [a-z0-9-]", id)
		}
		if len(id) < 20 {
			t.Fatalf("session id %q too short to be unguessable", id)
		}
		if seen[id] {
			t.Fatalf("collision on %q", id)
		}
		seen[id] = true
	}
}

func TestRegistryLifecycle(t *testing.T) {
	reg := NewRegistry("showcasedemo.app")
	if got := reg.Host("abc"); got != "s-abc.run.showcasedemo.app" {
		t.Errorf("Host = %q", got)
	}

	reg.Add("abc", Backend{URL: "http://web:8080"}, nil)
	if rts := reg.snapshot(); len(rts) != 1 || rts[0].State != Booting {
		t.Fatalf("expected one Booting route, got %+v", rts)
	}
	if !reg.Promote("abc") {
		t.Fatal("Promote should report the session present")
	}
	if reg.snapshot()[0].State != Live {
		t.Error("route should be Live after Promote")
	}
	if reg.Promote("ghost") {
		t.Error("Promote of an unknown session should report false")
	}
	reg.Remove("abc")
	if len(reg.snapshot()) != 0 {
		t.Error("route should be gone after Remove")
	}
}

func TestBuildBootingRoutesToSplash(t *testing.T) {
	reg := NewRegistry("demo.app")
	reg.Add("abc", Backend{URL: "http://web:8080"}, nil)
	cfg := NewConfigHandler(reg, "http://control:9000", "web").build()

	rtr, ok := cfg.HTTP.Routers["s-abc"]
	if !ok {
		t.Fatalf("missing booting router; routers=%v", cfg.HTTP.Routers)
	}
	if rtr.Rule != "Host(`s-abc.run.demo.app`)" {
		t.Errorf("rule = %q", rtr.Rule)
	}
	if rtr.Service != bootingService {
		t.Errorf("booting must route to splash service, got %q", rtr.Service)
	}
	if svc := cfg.HTTP.Services[bootingService]; svc.LoadBalancer.Servers[0].URL != "http://control:9000" {
		t.Errorf("booting service backend = %+v", svc)
	}
}

func TestBuildLiveSameOriginRouting(t *testing.T) {
	reg := NewRegistry("demo.app")
	reg.Add("abc",
		Backend{URL: "http://web:8080"},
		[]Backend{{PathPrefix: "/api", URL: "http://api:8080"}},
	)
	reg.Promote("abc")
	cfg := NewConfigHandler(reg, "http://control:9000", "web").build()

	ui, ok := cfg.HTTP.Routers["s-abc-ui"]
	if !ok {
		t.Fatal("missing UI router")
	}
	api, ok := cfg.HTTP.Routers["s-abc-api-0"]
	if !ok {
		t.Fatal("missing API router")
	}
	// Same host, but the API prefix must out-prioritise the catch-all UI.
	if ui.Rule != "Host(`s-abc.run.demo.app`)" {
		t.Errorf("ui rule = %q", ui.Rule)
	}
	if api.Rule != "Host(`s-abc.run.demo.app`) && (Path(`/api`) || PathPrefix(`/api/`))" {
		t.Errorf("api rule = %q", api.Rule)
	}
	if !(api.Priority > ui.Priority) {
		t.Errorf("api priority %d must exceed ui priority %d", api.Priority, ui.Priority)
	}
	if cfg.HTTP.Services["s-abc-ui"].LoadBalancer.Servers[0].URL != "http://web:8080" {
		t.Error("ui service backend wrong")
	}
	if cfg.HTTP.Services["s-abc-api-0"].LoadBalancer.Servers[0].URL != "http://api:8080" {
		t.Error("api service backend wrong")
	}
	// A live Session needs no booting service.
	if _, present := cfg.HTTP.Services[bootingService]; present {
		t.Error("no booting service expected for a fully-live config")
	}
}

// TestAPIPathRuleMatchesSegmentNotPrefix is the m1 regression: PathPrefix(`/api`)
// is a raw string prefix that wrongly swallows `/apidocs`/`/apiary`. The rule must
// match only the `/api` segment (Path) and its subpaths (PathPrefix with a
// trailing slash), so a sibling UI route like `/apiary` falls through to the UI.
func TestAPIPathRuleMatchesSegmentNotPrefix(t *testing.T) {
	got := apiPathRule("/api")
	want := "(Path(`/api`) || PathPrefix(`/api/`))"
	if got != want {
		t.Fatalf("apiPathRule(/api) = %q, want %q", got, want)
	}
	// A raw-prefix sibling must NOT be expressible as a subpath of /api: the rule
	// only matches `/api` exactly or `/api/...`, never `/apidocs` or `/apiary`.
	if strings.Contains(got, "PathPrefix(`/api`)") {
		t.Error("rule still uses the raw string prefix PathPrefix(`/api`) — /apidocs would match")
	}
	// A declared trailing slash collapses to the same rule (no double slash).
	if slashed := apiPathRule("/api/"); slashed != want {
		t.Errorf("apiPathRule(/api/) = %q, want %q", slashed, want)
	}
}

func TestConfigHandlerServesValidJSON(t *testing.T) {
	reg := NewRegistry("demo.app")
	reg.Add("abc", Backend{URL: "http://web:8080"}, nil)
	rec := httptest.NewRecorder()
	NewConfigHandler(reg, "http://control:9000", "web").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var cfg dynamicConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("served config is not valid JSON: %v", err)
	}
	if len(cfg.HTTP.Routers) == 0 {
		t.Error("expected at least one router")
	}
}

func TestSplashHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	SplashHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "http-equiv=\"refresh\"") {
		t.Error("splash must auto-refresh so it flips to the live Demo")
	}
}

func TestConfigListenerServesConfigAndSplash(t *testing.T) {
	reg := NewRegistry("demo.app")
	reg.Add("abc", Backend{URL: "http://web:8080"}, nil)
	h := ConfigListenerHandler(reg, "http://control/booting", "web")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/traefik", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("/traefik content-type = %q, want JSON config", ct)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything/at/all", nil))
	if !strings.Contains(rec.Body.String(), "Starting your demo") {
		t.Error("non-/traefik paths on the config listener must serve the booting splash")
	}
}

// TestBootingListenerNeverServesBackendMap is the B1 (ADR-0004) regression test:
// the splash surface a booting Session is forwarded to must return the splash for
// EVERY path — including /traefik — never the control-plane backend map. Traefik
// forwards the Guest's full path here, so a compromised Demo asking for /traefik
// must not be able to read the dynamic config.
func TestBootingListenerNeverServesBackendMap(t *testing.T) {
	// A Live Session whose backend URL must never leak through the splash surface.
	reg := NewRegistry("demo.app")
	reg.Add("victim", Backend{URL: "http://secret-ui:8080"}, []Backend{{PathPrefix: "/api", URL: "http://secret-api:8080"}})
	reg.Promote("victim")

	h := BootingListenerHandler()

	for _, path := range []string{"/traefik", "/", "/api", "/anything"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		body := rec.Body.String()
		if !strings.Contains(body, "Starting your demo") {
			t.Errorf("path %q on the booting surface must serve the splash, got %q", path, body)
		}
		if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "application/json") {
			t.Errorf("path %q on the booting surface returned JSON config content-type %q — backend map leak (ADR-0004)", path, ct)
		}
		// The backend map must not appear in the body under any path.
		if strings.Contains(body, "secret-ui") || strings.Contains(body, "secret-api") || strings.Contains(body, "loadBalancer") {
			t.Errorf("path %q on the booting surface leaked the backend map: %q", path, body)
		}
	}
}
