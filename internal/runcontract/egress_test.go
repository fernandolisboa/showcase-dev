package runcontract

import (
	"strings"
	"testing"
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

	yaml := string(out)
	if strings.Contains(yaml, "network_mode") {
		t.Errorf("rendered Compose must never carry network_mode (host networking is unrepresentable):\n%s", yaml)
	}
	// And the sealed network is actually rendered as internal.
	if !strings.Contains(yaml, "internal: true") {
		t.Errorf("rendered Compose must mark the Session network internal: true:\n%s", yaml)
	}
}
