package runcontract

import (
	"reflect"
	"testing"
)

func fixture() (Manifest, Options) {
	m := validManifest()
	m.Env = []EnvVar{
		{Name: "LOG_LEVEL", Source: EnvStatic, Value: "info"},
		{Name: "DATABASE_URL", Source: EnvPlatform},
		{Name: "STRIPE_KEY", Source: EnvOwner, Secret: true},
	}
	m.Seed = &Seed{Service: "api", Command: []string{"./seed"}}
	opts := Options{
		Project:     "s-abc123",
		Images:      map[string]string{"web": "web:cached", "api": "api:cached"},
		DBCreds:     DBCreds{User: "u", Password: "p", Database: "d"},
		PlatformEnv: map[string]string{"DATABASE_URL": "postgres://u:p@db:5432/d"},
	}
	return m, opts
}

func find(p ExecutionPlan, name string) (ServiceSpec, bool) {
	for _, s := range p.Services {
		if s.Name == name {
			return s, true
		}
	}
	return ServiceSpec{}, false
}

func TestCompileDBEngineUsedAsIs(t *testing.T) {
	m, opts := fixture() // validManifest declares DB{postgres, 17}
	plan, err := Compile(m, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	db, ok := find(plan, dbServiceName)
	if !ok {
		t.Fatal("expected a platform db service in the plan")
	}
	// The declared engine:version must be used verbatim — never substituted (#16).
	if want := m.DB.Engine + ":" + m.DB.Version; db.Image != want {
		t.Errorf("db image = %q, want %q (declared engine used as-is)", db.Image, want)
	}
}

func TestCompilePresentInvariants(t *testing.T) {
	m, opts := fixture()
	plan, err := Compile(m, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if !plan.Network.Internal {
		t.Error("session network must be internal (default-deny egress)")
	}
	if plan.Network.Name != "s-abc123-net" {
		t.Errorf("network name = %q", plan.Network.Name)
	}

	web, ok := find(plan, "web")
	if !ok {
		t.Fatal("web service missing")
	}
	if web.Runtime != "runsc" {
		t.Errorf("runtime = %q, want runsc (gVisor)", web.Runtime)
	}
	if !web.ReadOnlyRootFS {
		t.Error("app service must have a read-only rootfs")
	}
	if web.User != "1000:1000" {
		t.Errorf("user = %q, want non-root 1000:1000", web.User)
	}
	if !reflect.DeepEqual(web.CapDrop, []string{"ALL"}) {
		t.Errorf("cap_drop = %v, want [ALL]", web.CapDrop)
	}
	if !reflect.DeepEqual(web.SecurityOpt, []string{"no-new-privileges:true"}) {
		t.Errorf("security_opt = %v", web.SecurityOpt)
	}
	if !reflect.DeepEqual(web.Networks, []string{"s-abc123-net"}) {
		t.Errorf("networks = %v", web.Networks)
	}
	if web.Resources.MemoryBytes == 0 || web.Resources.CPUs == 0 || web.Resources.PidsLimit == 0 {
		t.Errorf("resource caps must all be set: %+v", web.Resources)
	}
}

func TestCompileScopesEnvAndDefersOwnerSecrets(t *testing.T) {
	m, opts := fixture()
	plan, _ := Compile(m, opts)
	web, _ := find(plan, "web")

	if web.Env["LOG_LEVEL"] != "info" {
		t.Errorf("static env not injected: %v", web.Env)
	}
	if web.Env["DATABASE_URL"] != "postgres://u:p@db:5432/d" {
		t.Errorf("platform env not injected: %v", web.Env)
	}
	if _, present := web.Env["STRIPE_KEY"]; present {
		t.Error("owner secret must NOT be injected in the MVP (ADR-0003)")
	}
}

func TestCompileDBService(t *testing.T) {
	m, opts := fixture()
	plan, _ := Compile(m, opts)

	db, ok := find(plan, "db")
	if !ok {
		t.Fatal("db service missing")
	}
	if db.Image != "postgres:17" {
		t.Errorf("db image = %q", db.Image)
	}
	if db.ReadOnlyRootFS {
		t.Error("db needs a writable rootfs for its data dir")
	}
	if db.Healthcheck == nil {
		t.Error("db must have a healthcheck to gate readiness")
	}
	if len(db.Volumes) != 1 || db.Volumes[0].Source != "s-abc123-pgdata" || db.Volumes[0].Target != "/var/lib/postgresql/data" {
		t.Errorf("db volume = %v", db.Volumes)
	}
	if len(plan.Volumes) != 1 || plan.Volumes[0].Name != "s-abc123-pgdata" {
		t.Errorf("plan volumes = %v", plan.Volumes)
	}
}

func TestCompileSeedOrdering(t *testing.T) {
	m, opts := fixture()
	plan, _ := Compile(m, opts)

	seed, ok := find(plan, "seed")
	if !ok {
		t.Fatal("seed service missing")
	}
	if seed.Restart != "no" {
		t.Errorf("seed restart = %q, want no (one-shot)", seed.Restart)
	}
	if !reflect.DeepEqual(seed.Command, []string{"./seed"}) {
		t.Errorf("seed command = %v", seed.Command)
	}
	if seed.DependsOn["db"] != DependHealthy {
		t.Errorf("seed must wait for db healthy: %v", seed.DependsOn)
	}
	web, _ := find(plan, "web")
	if web.DependsOn["seed"] != DependCompleted {
		t.Errorf("app must wait for seed completion: %v", web.DependsOn)
	}
}

func TestAppServicesExcludesPlatformServices(t *testing.T) {
	m, opts := fixture()
	plan, _ := Compile(m, opts)

	names := map[string]bool{}
	for _, s := range plan.AppServices() {
		names[s.Name] = true
	}
	if !names["web"] || !names["api"] {
		t.Errorf("app services missing web/api: %v", names)
	}
	if names["db"] || names["seed"] {
		t.Errorf("app services must exclude db/seed: %v", names)
	}
}

func TestCompileRuntimeOverrideForLocalDev(t *testing.T) {
	m, opts := fixture()
	opts.Runtime = "runc"
	plan, _ := Compile(m, opts)
	web, _ := find(plan, "web")
	if web.Runtime != "runc" {
		t.Errorf("runtime override = %q, want runc", web.Runtime)
	}
}

func TestCompileIsDeterministic(t *testing.T) {
	m, opts := fixture()
	a, err1 := Compile(m, opts)
	b, err2 := Compile(m, opts)
	if err1 != nil || err2 != nil {
		t.Fatalf("compile errors: %v %v", err1, err2)
	}
	if !reflect.DeepEqual(a, b) {
		t.Error("compile must be deterministic for the same inputs")
	}
}

func TestCompileErrors(t *testing.T) {
	cases := map[string]func(*Manifest, *Options){
		"no project":           func(_ *Manifest, o *Options) { o.Project = "" },
		"missing image":        func(_ *Manifest, o *Options) { delete(o.Images, "api") },
		"missing platform env": func(_ *Manifest, o *Options) { delete(o.PlatformEnv, "DATABASE_URL") },
		"db without creds":     func(_ *Manifest, o *Options) { o.DBCreds = DBCreds{} },
		"invalid manifest":     func(m *Manifest, _ *Options) { m.Services[1].Role = RoleUI },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m, opts := fixture()
			mutate(&m, &opts)
			if _, err := Compile(m, opts); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}
