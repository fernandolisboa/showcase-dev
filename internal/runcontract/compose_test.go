package runcontract

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func compiledCompose(t *testing.T) (string, map[string]any) {
	t.Helper()
	m, opts := fixture()
	plan, err := Compile(m, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := plan.ToCompose()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("rendered compose is not valid YAML: %v\n%s", err, out)
	}
	return string(out), doc
}

func TestComposePresentInvariants(t *testing.T) {
	text, doc := compiledCompose(t)

	services, _ := doc["services"].(map[string]any)
	web, _ := services["web"].(map[string]any)
	if web == nil {
		t.Fatalf("web service not rendered:\n%s", text)
	}
	if web["runtime"] != "runsc" {
		t.Errorf("web.runtime = %v, want runsc", web["runtime"])
	}
	if web["read_only"] != true {
		t.Errorf("web.read_only = %v, want true", web["read_only"])
	}
	for _, key := range []string{"mem_limit", "pids_limit", "cpus", "cap_drop", "security_opt"} {
		if _, ok := web[key]; !ok {
			t.Errorf("web service missing %q:\n%s", key, text)
		}
	}

	networks, _ := doc["networks"].(map[string]any)
	net, _ := networks["s-abc123-net"].(map[string]any)
	if net == nil || net["internal"] != true {
		t.Errorf("session network must be internal: %v", networks)
	}
}

func TestComposeAbsentInvariants(t *testing.T) {
	text, doc := compiledCompose(t)

	// String-level: none of the forbidden constructs appear anywhere.
	for _, forbidden := range []string{"privileged", "network_mode", "docker.sock", "/var/run", "cap_add"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("rendered compose must not contain %q:\n%s", forbidden, text)
		}
	}

	// Structural: every volume is long-form type:volume (never a bind mount), and
	// no source is a host path. Long-form removes the "source:target" string that
	// Compose would otherwise split on ":" into a host bind.
	services, _ := doc["services"].(map[string]any)
	for name, raw := range services {
		svc, _ := raw.(map[string]any)
		vols, _ := svc["volumes"].([]any)
		for _, v := range vols {
			vm, ok := v.(map[string]any)
			if !ok {
				t.Errorf("service %q volume %v is not long-form (type:volume) — short strings can be reparsed as host binds", name, v)
				continue
			}
			if vm["type"] != "volume" {
				t.Errorf("service %q volume type = %v, want volume (no bind mounts, ADR-0003)", name, vm["type"])
			}
			src, _ := vm["source"].(string)
			if strings.HasPrefix(src, "/") || strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
				t.Errorf("service %q has a host-path volume source %q (only named volumes allowed)", name, src)
			}
		}
		if _, ok := svc["privileged"]; ok {
			t.Errorf("service %q must not set privileged", name)
		}
	}
}

func TestComposeRenderIsDeterministic(t *testing.T) {
	m, opts := fixture()
	plan, _ := Compile(m, opts)
	a, _ := plan.ToCompose()
	b, _ := plan.ToCompose()
	if string(a) != string(b) {
		t.Error("compose rendering must be deterministic")
	}
}
