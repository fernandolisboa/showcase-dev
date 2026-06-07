package runcontract

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These are the adversarial "Seam 3" cases: manifests/options an attacker
// actually writes to bypass the platform invariants. Each must be rejected by
// the compiler or rendered safe. They are the regression tests for the bypasses
// found in PR #22 review (issue #6).

// B1: an Owner service named "db" or "seed" must be rejected by Validate, so it
// can never overwrite the trusted platform Postgres / seed one-shot in the
// name-keyed Compose map.
func TestValidateRejectsReservedServiceNames(t *testing.T) {
	for _, reserved := range []string{dbServiceName, seedServiceName} {
		t.Run(reserved, func(t *testing.T) {
			m := validManifest()
			// Rename the API service to the reserved name (keep exactly one ui).
			m.Services[1].Name = reserved
			err := m.Validate()
			if err == nil {
				t.Fatalf("Validate accepted an Owner service named %q (would overwrite the platform service)", reserved)
			}
			if !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("error %q does not mention the reserved-name rule", err.Error())
			}
		})
	}
}

// B1, end-to-end: compiling a manifest whose service collides with "db" must
// fail rather than silently replacing the trusted Postgres with an Owner image.
func TestCompileRejectsServiceCollidingWithPlatformDB(t *testing.T) {
	m, opts := fixture()
	m.Services[1].Name = dbServiceName // api -> "db"
	opts.Images[dbServiceName] = "attacker/db:latest"
	delete(opts.Images, "api")
	// The seed referenced "api"; point it at the ui service so the only failure
	// under test is the reserved-name collision, not a dangling seed reference.
	m.Seed = &Seed{Service: "web", Command: []string{"./seed"}}

	if _, err := Compile(m, opts); err == nil {
		t.Fatal("Compile accepted an Owner service named \"db\" — the platform DB could be overwritten")
	}
}

// B2: a Project (or NetworkName) carrying ":" or "/" must be rejected before it
// can be concatenated into a volume source and reinterpreted as a host bind.
func TestCompileRejectsUnsafeProjectAndNetworkName(t *testing.T) {
	cases := map[string]func(*Options){
		"project with bind-mount injection": func(o *Options) { o.Project = "/etc:/host" },
		"project with slash":                func(o *Options) { o.Project = "a/b" },
		"project with colon":                func(o *Options) { o.Project = "a:b" },
		"project uppercase":                 func(o *Options) { o.Project = "S-ABC" },
		"network name with slash":           func(o *Options) { o.NetworkName = "/etc:/host" },
		"network name with colon":           func(o *Options) { o.NetworkName = "a:b" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m, opts := fixture()
			mutate(&opts)
			if _, err := Compile(m, opts); err == nil {
				t.Fatal("Compile accepted an unsafe Project/NetworkName")
			}
		})
	}
}

// B2, defense in depth: even if a crafted source name reached the renderer, the
// long-form volume syntax keeps Compose from parsing it as a host bind. We
// inspect the rendered DB volume and assert it is type:volume with a structured
// source (never a "src:dst" string that Compose splits on ":").
func TestComposeVolumesAreLongFormNeverBind(t *testing.T) {
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
		t.Fatalf("invalid YAML: %v", err)
	}
	services, _ := doc["services"].(map[string]any)
	db, _ := services["db"].(map[string]any)
	vols, _ := db["volumes"].([]any)
	if len(vols) != 1 {
		t.Fatalf("db should have exactly one volume, got %v", vols)
	}
	vm, ok := vols[0].(map[string]any)
	if !ok {
		t.Fatalf("db volume is not long-form (type:volume): %v", vols[0])
	}
	if vm["type"] != "volume" {
		t.Errorf("db volume type = %v, want volume", vm["type"])
	}
	if vm["source"] != "s-abc123-pgdata" {
		t.Errorf("db volume source = %v, want named volume", vm["source"])
	}
	// No short-form "source:target" string anywhere in the rendered document.
	if strings.Contains(string(out), "pgdata:/var/lib/postgresql/data") {
		t.Errorf("rendered compose still uses fragile short-form volume syntax:\n%s", out)
	}
}

// M2: the seed one-shot runs untrusted Owner code, so it must carry the same
// read-only rootfs + tmpfs hardening as app services.
func TestSeedIsHardenedLikeAppServices(t *testing.T) {
	m, opts := fixture()
	plan, err := Compile(m, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	seed, ok := find(plan, "seed")
	if !ok {
		t.Fatal("seed service missing")
	}
	if !seed.ReadOnlyRootFS {
		t.Error("seed runs untrusted Owner code and must have a read-only rootfs")
	}
	if len(seed.TmpFS) == 0 {
		t.Error("seed needs a tmpfs for its writable scratch (e.g. /tmp)")
	}
	if len(seed.CapDrop) == 0 {
		t.Errorf("seed must drop capabilities: %v", seed.CapDrop)
	}
}
