package runcontract

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	defaultRuntime    = "runsc"
	defaultAppUser    = "1000:1000"
	dbServiceName     = "db"
	seedServiceName   = "seed"
	egressServiceName = "egress"
	// egressProxyPort is the port the egress-proxy sidecar listens on, in-Stack.
	egressProxyPort = 8888
)

// safeNameRE bounds the platform-supplied Project / NetworkName to a strict
// charset. Project flows into the compose project name, the network name, and
// the DB volume source; without this guard a value like "/etc:/host" could be
// concatenated into a volume string and reinterpreted by Compose as a host bind
// mount (defeating the no-bind-mounts invariant, ADR-0003). Defense in depth:
// long-form volume syntax (below) is the structural backstop; this is the gate.
var safeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var (
	defaultAppResources    = Resources{MemoryBytes: 512 << 20, CPUs: 1.0, PidsLimit: 256}
	defaultDBResources     = Resources{MemoryBytes: 256 << 20, CPUs: 1.0, PidsLimit: 256}
	defaultEgressResources = Resources{MemoryBytes: 64 << 20, CPUs: 0.5, PidsLimit: 64}
)

// DBCreds are the per-Session database credentials the platform mints (ADR-0003)
// and the caller passes in — keeping the compiler pure (no randomness).
type DBCreds struct {
	User     string
	Password string
	Database string
}

// Options carry the per-Session, caller-provided inputs the pure compiler needs:
// the built image refs, generated DB creds, resolved platform env values, and
// the runtime. Randomness and secret minting happen in the Runner, not here.
type Options struct {
	// Project is the compose project name (the Session handle, e.g. "s-<id>").
	Project string
	// NetworkName overrides the per-Session network name (default Project+"-net").
	NetworkName string
	// Runtime is "runsc" (default) or "runc" for local dev.
	Runtime string
	// AppUser is the non-root uid:gid for app services (default "1000:1000").
	AppUser string
	// Resources caps app services; zero fields fall back to defaults.
	Resources Resources
	// DBResources caps the DB service; zero fields fall back to defaults.
	DBResources Resources
	// Images maps each app service name (and the seed service) to its prebuilt
	// image ref (ADR-0003: build at publish, run from cache).
	Images map[string]string
	// DBCreds are required when the Manifest declares a db.
	DBCreds DBCreds
	// PlatformEnv resolves every platform-sourced env var by name.
	PlatformEnv map[string]string
	// EgressProxyImage is the platform-owned forward-proxy image used for the
	// egress-proxy sidecar (ADR-0011). Required only when the Manifest declares a
	// non-empty Egress allow-list; it is platform-owned, not a per-Project image.
	EgressProxyImage string
	// EgressNetworkName overrides the second (non-internal) network's name
	// (default Project+"-egress-net"). Only used when an allow-list is declared.
	EgressNetworkName string
	// EgressResources caps the egress-proxy sidecar; zero fields fall back to defaults.
	EgressResources Resources
}

// Compile turns a validated Manifest into a locked-down ExecutionPlan. It is
// pure and deterministic. It enforces the platform invariants structurally; an
// Owner cannot disable gVisor, resource caps, isolation, or non-root.
func Compile(m Manifest, opts Options) (ExecutionPlan, error) {
	if err := m.Validate(); err != nil {
		return ExecutionPlan{}, fmt.Errorf("invalid manifest: %w", err)
	}
	if opts.Project == "" {
		return ExecutionPlan{}, errors.New("options: project is required")
	}
	if !safeNameRE.MatchString(opts.Project) {
		return ExecutionPlan{}, fmt.Errorf("options: project %q must match %s", opts.Project, safeNameRE)
	}
	if opts.NetworkName != "" && !safeNameRE.MatchString(opts.NetworkName) {
		return ExecutionPlan{}, fmt.Errorf("options: networkName %q must match %s", opts.NetworkName, safeNameRE)
	}
	if opts.EgressNetworkName != "" && !safeNameRE.MatchString(opts.EgressNetworkName) {
		return ExecutionPlan{}, fmt.Errorf("options: egressNetworkName %q must match %s", opts.EgressNetworkName, safeNameRE)
	}

	runtime := orDefault(opts.Runtime, defaultRuntime)
	appUser := orDefault(opts.AppUser, defaultAppUser)
	netName := orDefault(opts.NetworkName, opts.Project+"-net")
	appRes := opts.Resources.orDefault(defaultAppResources)
	dbRes := opts.DBResources.orDefault(defaultDBResources)

	plan := ExecutionPlan{
		Project: opts.Project,
		// Internal: true is the structural form of default-deny egress (ADR-0007):
		// the Session network has no route to the internet.
		Network: NetworkSpec{Name: netName, Internal: true},
	}

	// Resolve the Owner-declared env once; injected into every app service.
	// Owner-sourced secrets are deferred and never injected (ADR-0003).
	appEnv, err := resolveAppEnv(m.Env, opts.PlatformEnv)
	if err != nil {
		return ExecutionPlan{}, err
	}

	// Egress allow-list (ADR-0011): add the second (non-internal) network and the
	// dual-homed proxy sidecar, and point Owner services at it. Done before the
	// services are appended so the proxy env lands in their shared appEnv map. With
	// no allow-list this whole block is skipped — the Stack is the sealed,
	// default-deny Stack of ADR-0007, byte for byte.
	if len(m.Egress) > 0 {
		if opts.EgressProxyImage == "" {
			return ExecutionPlan{}, errors.New("options: egressProxyImage is required when the manifest declares an egress allow-list")
		}
		egressNet := orDefault(opts.EgressNetworkName, opts.Project+"-egress-net")
		plan.EgressNetwork = &NetworkSpec{Name: egressNet, Internal: false}
		injectProxyEnv(appEnv, m.Services)
		plan.Services = append(plan.Services, egressProxyService(
			opts.EgressProxyImage, runtime, opts.EgressResources.orDefault(defaultEgressResources),
			netName, egressNet, m.Egress))
	}

	// Ordering target the app services wait on: the seed if present, else the DB.
	appDeps := map[string]DependCondition{}
	if m.Seed != nil {
		appDeps[seedServiceName] = DependCompleted
	} else if m.DB != nil {
		appDeps[dbServiceName] = DependHealthy
	}

	// DB first so it appears before its dependents.
	if m.DB != nil {
		if err := requireDBCreds(opts.DBCreds); err != nil {
			return ExecutionPlan{}, err
		}
		volName := opts.Project + "-pgdata"
		plan.Volumes = append(plan.Volumes, VolumeSpec{Name: volName})
		plan.Services = append(plan.Services, dbService(m.DB, opts.DBCreds, runtime, dbRes, netName, volName))
	}

	if m.Seed != nil {
		img, ok := opts.Images[m.Seed.Service]
		if !ok {
			return ExecutionPlan{}, fmt.Errorf("options: no image for seed service %q", m.Seed.Service)
		}
		// The seed runs Owner-authored (untrusted, per ADR-0002) m.Seed.Command on
		// the app image, so it gets the same hardening as app services — including
		// a read-only rootfs + tmpfs. It writes to the DB over the network and to
		// /tmp, never to its own rootfs.
		plan.Services = append(plan.Services, ServiceSpec{
			Name:           seedServiceName,
			Image:          img,
			Runtime:        runtime,
			Resources:      appRes,
			Networks:       []string{netName},
			Env:            appEnv,
			User:           appUser,
			ReadOnlyRootFS: true,
			TmpFS:          []string{"/tmp"},
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			Restart:        "no",
			Command:        m.Seed.Command,
			DependsOn:      map[string]DependCondition{dbServiceName: DependHealthy},
		})
	}

	for _, s := range m.Services {
		img, ok := opts.Images[s.Name]
		if !ok {
			return ExecutionPlan{}, fmt.Errorf("options: no image for service %q", s.Name)
		}
		plan.Services = append(plan.Services, ServiceSpec{
			Name:           s.Name,
			Image:          img,
			Runtime:        runtime,
			Resources:      appRes,
			Networks:       []string{netName},
			Env:            appEnv,
			User:           appUser,
			ReadOnlyRootFS: true,
			TmpFS:          []string{"/tmp"},
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			Restart:        "on-failure",
			Healthcheck:    appHealthcheck(s.Healthcheck),
			DependsOn:      cloneDeps(appDeps),
		})
	}

	return plan, nil
}

// appHealthcheck bakes an Owner-declared readiness probe (exec-form argv) into a
// Compose healthcheck so `up --wait` gates the Session on the app actually
// serving — not merely started (ADR-0004/0006, #17). The platform prefixes the
// Docker "CMD" sentinel so the Owner supplies only the argv, never a shell
// string. No probe declared leaves the image's own HEALTHCHECK untouched (nil).
func appHealthcheck(argv []string) *Healthcheck {
	if len(argv) == 0 {
		return nil
	}
	return &Healthcheck{
		Test:        append([]string{"CMD"}, argv...),
		Interval:    "2s",
		Timeout:     "3s",
		Retries:     15,
		StartPeriod: "10s",
	}
}

// dbService builds the platform-provided database. It is a trusted platform
// image (not Owner code), so it keeps a writable data volume and the image's own
// privilege-dropping rather than a read-only rootfs / cap-drop-all; it still gets
// resource caps, the isolated network, no host networking, no bind mount, no
// socket, and no privileged — the invariants that bound an untrusted neighbour.
func dbService(db *DB, creds DBCreds, runtime string, res Resources, net, volume string) ServiceSpec {
	return ServiceSpec{
		Name:      dbServiceName,
		Image:     db.Engine + ":" + db.Version,
		Runtime:   runtime,
		Resources: res,
		Networks:  []string{net},
		Env: map[string]string{
			"POSTGRES_USER":     creds.User,
			"POSTGRES_PASSWORD": creds.Password,
			"POSTGRES_DB":       creds.Database,
		},
		Volumes:     []VolumeMount{{Source: volume, Target: "/var/lib/postgresql/data"}},
		SecurityOpt: []string{"no-new-privileges:true"},
		Restart:     "on-failure",
		Healthcheck: &Healthcheck{
			Test:     []string{"CMD-SHELL", "pg_isready -U " + creds.User},
			Interval: "5s",
			Timeout:  "5s",
			Retries:  5,
		},
	}
}

// injectProxyEnv points Owner services at the egress-proxy sidecar (ADR-0011).
// HTTP(S)_PROXY is the convenience path for well-behaved apps; NO_PROXY keeps
// intra-Stack traffic (app→db, ui→api) off the proxy. Validate has already
// rejected any Owner-set proxy key, so these never clobber a declared value.
// Both cases are set since stdlib clients vary on which they read.
func injectProxyEnv(env map[string]string, services []Service) {
	proxyURL := fmt.Sprintf("http://%s:%d", egressServiceName, egressProxyPort)
	noProxy := egressNoProxy(services)
	for k, v := range map[string]string{
		"HTTP_PROXY":  proxyURL,
		"HTTPS_PROXY": proxyURL,
		"http_proxy":  proxyURL,
		"https_proxy": proxyURL,
		"NO_PROXY":    noProxy,
		"no_proxy":    noProxy,
	} {
		env[k] = v
	}
}

// egressNoProxy lists the intra-Stack destinations that must never traverse the
// proxy: loopback, the DB host, and every Owner service name.
func egressNoProxy(services []Service) string {
	hosts := []string{"localhost", "127.0.0.1", dbServiceName}
	for _, s := range services {
		hosts = append(hosts, s.Name)
	}
	return strings.Join(hosts, ",")
}

// egressProxyService builds the dual-homed egress-proxy sidecar. It is trusted
// platform code but reachable by untrusted Owner code, so it carries the same
// hardening baseline as an app service (non-root, cap-drop ALL, read-only rootfs,
// no-new-privileges) plus a small resource cap and a self-dialing healthcheck. It
// joins BOTH the sealed internal net (where apps reach it) and the egress net
// (the only route out) — the single bridge between routeless apps and the
// outside. The validated allow-list is rendered into EGRESS_ALLOWLIST.
func egressProxyService(image, runtime string, res Resources, internalNet, egressNet string, rules []EgressRule) ServiceSpec {
	return ServiceSpec{
		Name:           egressServiceName,
		Image:          image,
		Runtime:        runtime,
		Resources:      res,
		Networks:       []string{internalNet, egressNet},
		Env:            map[string]string{"EGRESS_ALLOWLIST": renderAllowlist(rules)},
		User:           defaultAppUser,
		ReadOnlyRootFS: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges:true"},
		Restart:        "on-failure",
		Healthcheck: &Healthcheck{
			Test:        []string{"CMD", "/egress-proxy", "-healthcheck"},
			Interval:    "5s",
			Timeout:     "3s",
			Retries:     5,
			StartPeriod: "3s",
		},
	}
}

// renderAllowlist serializes the validated rules to the EGRESS_ALLOWLIST wire
// form the proxy reads: comma-separated host:port. The contract is the source of
// truth (already validated); the proxy only enforces this rendered list.
func renderAllowlist(rules []EgressRule) string {
	parts := make([]string, len(rules))
	for i, r := range rules {
		parts[i] = fmt.Sprintf("%s:%d", r.Host, r.Port)
	}
	return strings.Join(parts, ",")
}

// resolveAppEnv resolves static and platform env, skipping deferred owner env.
func resolveAppEnv(vars []EnvVar, platform map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range vars {
		switch e.Source {
		case EnvStatic:
			out[e.Name] = e.Value
		case EnvPlatform:
			v, ok := platform[e.Name]
			if !ok {
				return nil, fmt.Errorf("options: no platform value for env %q", e.Name)
			}
			out[e.Name] = v
		case EnvOwner:
			// Declared-but-deferred: never injected in the MVP (ADR-0003).
		}
	}
	return out, nil
}

func requireDBCreds(c DBCreds) error {
	if c.User == "" || c.Password == "" || c.Database == "" {
		return errors.New("options: db is declared but DBCreds are incomplete")
	}
	return nil
}

func (r Resources) orDefault(d Resources) Resources {
	if r.MemoryBytes == 0 {
		r.MemoryBytes = d.MemoryBytes
	}
	if r.CPUs == 0 {
		r.CPUs = d.CPUs
	}
	if r.PidsLimit == 0 {
		r.PidsLimit = d.PidsLimit
	}
	return r
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func cloneDeps(in map[string]DependCondition) map[string]DependCondition {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]DependCondition, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
