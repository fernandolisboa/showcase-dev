package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFailMovesRouteToTerminalAndClearsBackends(t *testing.T) {
	reg := NewRegistry("demo.app")
	reg.Add("abc", Backend{URL: "http://web:8080"}, []Backend{{PathPrefix: "/api", URL: "http://api:8080"}})
	reg.Promote("abc")

	reg.Fail("abc", Failed)

	route, ok := reg.Get("abc")
	if !ok {
		t.Fatal("failed route should still exist (lingering)")
	}
	if route.State != Failed {
		t.Errorf("state = %v, want Failed", route.State)
	}
	if route.UI.URL != "" || route.APIs != nil {
		t.Errorf("backends should be cleared on Fail, got UI=%+v APIs=%+v", route.UI, route.APIs)
	}
}

func TestFailCreatesRouteWhenAbsent(t *testing.T) {
	// A won't-start teardown removes the route before the Manager flags it Failed,
	// so Fail must (re)create it.
	reg := NewRegistry("demo.app")
	reg.Fail("gone", Failed)

	route, ok := reg.Get("gone")
	if !ok {
		t.Fatal("Fail must create the route when absent")
	}
	if route.State != Failed || route.Host != "s-gone.run.demo.app" {
		t.Errorf("unexpected route %+v", route)
	}
}

func TestStateByHost(t *testing.T) {
	reg := NewRegistry("demo.app")
	reg.Add("abc", Backend{}, nil)
	reg.Fail("dead", Crashed)

	if st, ok := reg.StateByHost("s-abc.run.demo.app"); !ok || st != Booting {
		t.Errorf("abc host: state=%v ok=%v, want Booting,true", st, ok)
	}
	if st, ok := reg.StateByHost("s-dead.run.demo.app:443"); !ok || st != Crashed {
		t.Errorf("dead host (with port): state=%v ok=%v, want Crashed,true", st, ok)
	}
	// Hostnames are case-insensitive: a mixed-case Host must still resolve.
	if st, ok := reg.StateByHost("S-ABC.RUN.DEMO.APP"); !ok || st != Booting {
		t.Errorf("mixed-case host: state=%v ok=%v, want Booting,true", st, ok)
	}
	if _, ok := reg.StateByHost("s-missing.run.demo.app"); ok {
		t.Error("unknown session should report ok=false")
	}
	if _, ok := reg.StateByHost("evil.example.com"); ok {
		t.Error("a non-demo host should report ok=false")
	}
}

func TestBuildTerminalStatesRouteToStatusService(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state State
	}{{"failed", Failed}, {"crashed", Crashed}} {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry("demo.app")
			reg.Fail("abc", tc.state)
			cfg := NewConfigHandler(reg, "http://control:9000", "web").build()

			rtr, ok := cfg.HTTP.Routers["s-abc"]
			if !ok {
				t.Fatalf("missing status router; routers=%v", cfg.HTTP.Routers)
			}
			if rtr.Service != statusService {
				t.Errorf("%s must route to the status service, got %q", tc.name, rtr.Service)
			}
			if svc := cfg.HTTP.Services[statusService]; svc.LoadBalancer.Servers[0].URL != "http://control:9000" {
				t.Errorf("status service backend = %+v", svc)
			}
		})
	}
}

func TestStatusHandlerRendersPerState(t *testing.T) {
	states := map[string]State{
		"s-boot.run.demo.app":  Booting,
		"s-fail.run.demo.app":  Failed,
		"s-crash.run.demo.app": Crashed,
	}
	lookup := func(host string) (State, bool) {
		st, ok := states[host]
		return st, ok
	}
	h := StatusHandler(lookup)

	cases := []struct {
		host        string
		wantText    string
		wantRefresh bool
	}{
		{"s-boot.run.demo.app", "Starting your demo", true},
		{"s-fail.run.demo.app", "failed to start", false},
		{"s-crash.run.demo.app", "crashed", false},
		{"s-unknown.run.demo.app", "ended", false},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = tc.host
		h.ServeHTTP(rec, req)

		if body := rec.Body.String(); !strings.Contains(strings.ToLower(body), strings.ToLower(tc.wantText)) {
			t.Errorf("host %q: body missing %q; got %q", tc.host, tc.wantText, body)
		}
		hasRefresh := rec.Header().Get("Retry-After") != ""
		if hasRefresh != tc.wantRefresh {
			t.Errorf("host %q: auto-refresh = %v, want %v", tc.host, hasRefresh, tc.wantRefresh)
		}
		// A terminal page must not auto-refresh (it would reload a dead route forever).
		if !tc.wantRefresh && strings.Contains(rec.Body.String(), "http-equiv=\"refresh\"") {
			t.Errorf("host %q: terminal page must not contain a refresh meta", tc.host)
		}
	}
}
