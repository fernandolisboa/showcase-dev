package runcontract

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These tests pin the runtime network invariants of ADR-0007 at the pure-compile
// layer (no Docker), so a future compile.go/compose.go change can't silently
// weaken default-deny egress or open up host networking. The end-to-end proof
// that the booted Stack genuinely can't reach the internet lives in the runner
// integration test; these lock the structure that makes that true.

// TestCompileEnforcesDefaultDenyEgress: every Session is sealed onto its own
// internal bridge and joins no other network, so it has no route to the internet
// (ADR-0007 AC1) while intra-Stack traffic over that shared bridge still works
// (AC2).
func TestCompileEnforcesDefaultDenyEgress(t *testing.T) {
	m, opts := fixture()
	plan, err := Compile(m, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if !plan.Network.Internal {
		t.Error("per-Session network must be internal: true (the structural form of default-deny egress)")
	}

	net := plan.Network.Name
	if len(plan.Services) == 0 {
		t.Fatal("expected services in the plan")
	}
	for _, s := range plan.Services {
		if len(s.Networks) != 1 || s.Networks[0] != net {
			t.Errorf("service %q joins %v, want exactly [%q] — a second network would be a route out", s.Name, s.Networks, net)
		}
	}
}

// TestNoHostNetworking: the generated Compose can never request host networking.
// An Owner supplies only a Manifest and the platform renders the Compose, so there
// must be no path to `network_mode: host` (which would bypass the per-Session
// bridge and its default-deny egress entirely, ADR-0007).
func TestNoHostNetworking(t *testing.T) {
	m, opts := fixture()
	plan, err := Compile(m, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := plan.ToCompose()
	if err != nil {
		t.Fatalf("render compose: %v", err)
	}

	// No service can carry network_mode: it's not a field on the rendered struct,
	// so the token must not appear anywhere in the document.
	if strings.Contains(string(out), "network_mode") {
		t.Errorf("rendered Compose must never carry network_mode (host networking is unrepresentable):\n%s", out)
	}

	// In the no-egress case there is exactly one network and it must be internal —
	// checked structurally (parsed under the networks key) rather than by
	// substring, so a renamed key or requoting can't give a false pass. The
	// opted-in egress case adds a non-internal net joined ONLY by the proxy; that
	// is asserted separately by TestToComposeRendersEgressNetworkNonInternal.
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("rendered Compose is not valid YAML: %v", err)
	}
	networks, ok := doc["networks"].(map[string]any)
	if !ok || len(networks) == 0 {
		t.Fatalf("rendered Compose has no networks block: %s", out)
	}
	for name, v := range networks {
		net, _ := v.(map[string]any)
		if internal, _ := net["internal"].(bool); !internal {
			t.Errorf("network %q must be internal: true (default-deny egress), got %v", name, net["internal"])
		}
	}
}

// egressFixture is the standard fixture plus a declared allow-list and the
// platform proxy image — the opted-in egress case of ADR-0011.
func egressFixture() (Manifest, Options) {
	m, opts := fixture()
	m.Egress = []EgressRule{{Host: "api.stripe.com", Port: 443}, {Host: "example.com", Port: 80}}
	opts.EgressProxyImage = "showcase-dev/egress-proxy:test"
	return m, opts
}

// TestCompileNoEgressKeepsStackSealed: with no allow-list, the plan is the
// sealed default-deny Stack — no second network, no proxy sidecar, and no proxy
// env leaks into the app services (ADR-0011: egress is purely additive).
func TestCompileNoEgressKeepsStackSealed(t *testing.T) {
	m, opts := fixture() // fixture declares no Egress
	plan, err := Compile(m, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if plan.EgressNetwork != nil {
		t.Error("no allow-list must leave EgressNetwork nil")
	}
	if _, ok := find(plan, egressServiceName); ok {
		t.Error("no allow-list must add no egress sidecar")
	}
	for _, s := range plan.AppServices() {
		if _, ok := s.Env["HTTP_PROXY"]; ok {
			t.Errorf("service %q must not get HTTP_PROXY when no allow-list is declared", s.Name)
		}
	}
}

// TestCompileEgressAllowListTopology: a declared allow-list adds the second
// (non-internal) network and a dual-homed proxy sidecar, points Owner services
// at it via proxy env — and crucially keeps the app services routeless on the
// internal net only. The sidecar is the sole member of the egress net.
func TestCompileEgressAllowListTopology(t *testing.T) {
	m, opts := egressFixture()
	plan, err := Compile(m, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Second network exists and is NOT internal — the one route out.
	if plan.EgressNetwork == nil {
		t.Fatal("an allow-list must add the egress network")
	}
	if plan.EgressNetwork.Internal {
		t.Error("the egress network must be non-internal (it is the route out)")
	}
	egressNet := plan.EgressNetwork.Name
	internalNet := plan.Network.Name

	// The sidecar is dual-homed; allow-list rendered; hardened like an app service.
	proxy, ok := find(plan, egressServiceName)
	if !ok {
		t.Fatal("an allow-list must add the egress sidecar")
	}
	if got := proxy.Networks; len(got) != 2 || got[0] != internalNet || got[1] != egressNet {
		t.Errorf("egress sidecar joins %v, want [%q %q]", got, internalNet, egressNet)
	}
	if want := "api.stripe.com:443,example.com:80"; proxy.Env["EGRESS_ALLOWLIST"] != want {
		t.Errorf("EGRESS_ALLOWLIST = %q, want %q", proxy.Env["EGRESS_ALLOWLIST"], want)
	}
	if proxy.Image != opts.EgressProxyImage {
		t.Errorf("sidecar image = %q, want platform image %q", proxy.Image, opts.EgressProxyImage)
	}
	if proxy.User != defaultAppUser || !proxy.ReadOnlyRootFS || len(proxy.CapDrop) == 0 || len(proxy.SecurityOpt) == 0 {
		t.Error("egress sidecar must carry the app hardening baseline (non-root, read-only, cap-drop, no-new-privs)")
	}
	if proxy.Healthcheck == nil || proxy.Resources.MemoryBytes == 0 {
		t.Error("egress sidecar must have a healthcheck and a resource cap")
	}

	// Every Owner service stays routeless on the internal net only, and gets the
	// proxy env pointed at the sidecar.
	wantProxy := "http://egress:8888"
	for _, s := range plan.Services {
		if s.Name == egressServiceName {
			continue
		}
		if len(s.Networks) != 1 || s.Networks[0] != internalNet {
			t.Errorf("service %q joins %v, want only [%q] — only the sidecar may reach the egress net", s.Name, s.Networks, internalNet)
		}
	}
	// Owner code services (apps + seed) get the convenience proxy env.
	for _, name := range []string{"web", "api", seedServiceName} {
		s, ok := find(plan, name)
		if !ok {
			t.Fatalf("expected service %q in plan", name)
		}
		if s.Env["HTTP_PROXY"] != wantProxy || s.Env["HTTPS_PROXY"] != wantProxy {
			t.Errorf("service %q proxy env = %q/%q, want %q", name, s.Env["HTTP_PROXY"], s.Env["HTTPS_PROXY"], wantProxy)
		}
		if np := s.Env["NO_PROXY"]; !strings.Contains(np, "db") || !strings.Contains(np, "web") || !strings.Contains(np, "localhost") {
			t.Errorf("service %q NO_PROXY = %q, want intra-stack hosts (db, web, localhost)", name, np)
		}
	}
}

// TestCompileEgressRequiresProxyImage: declaring an allow-list without supplying
// the platform proxy image is a compile error, not a silently sealed Stack.
func TestCompileEgressRequiresProxyImage(t *testing.T) {
	m, opts := egressFixture()
	opts.EgressProxyImage = ""
	if _, err := Compile(m, opts); err == nil {
		t.Fatal("expected an error when an allow-list is declared without an egress proxy image")
	}
}

// TestToComposeRendersEgressNetworkNonInternal: the rendered Compose carries the
// session net as internal and the egress net as non-internal, and the sidecar is
// the only service on the egress net.
func TestToComposeRendersEgressNetworkNonInternal(t *testing.T) {
	m, opts := egressFixture()
	plan, err := Compile(m, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := plan.ToCompose()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(string(out), "network_mode") {
		t.Errorf("rendered Compose must never carry network_mode:\n%s", out)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	networks, _ := doc["networks"].(map[string]any)
	if len(networks) != 2 {
		t.Fatalf("want 2 networks (session + egress), got %d: %s", len(networks), out)
	}
	internalNet := plan.Network.Name
	egressNet := plan.EgressNetwork.Name
	if net, _ := networks[internalNet].(map[string]any); net["internal"] != true {
		t.Errorf("session network %q must be internal: true, got %v", internalNet, net["internal"])
	}
	// A non-internal network omits the key (omitempty) — it must not be true.
	if net, _ := networks[egressNet].(map[string]any); net["internal"] == true {
		t.Errorf("egress network %q must be non-internal, got internal: true", egressNet)
	}

	services, _ := doc["services"].(map[string]any)
	for name, v := range services {
		svc, _ := v.(map[string]any)
		nets, _ := svc["networks"].([]any)
		onEgress := false
		for _, n := range nets {
			if n == egressNet {
				onEgress = true
			}
		}
		if onEgress && name != egressServiceName {
			t.Errorf("service %q is on the egress net; only %q may be", name, egressServiceName)
		}
	}
}
