package runcontract

// ExecutionPlan is the locked-down Stack the Runner boots for one Session. It is
// the platform-owned artifact (ADR-0003): the Owner declares the Manifest, the
// platform generates this. Every security invariant lives here structurally.
type ExecutionPlan struct {
	// Project name passed to `docker compose -p`; the Session's lifecycle handle.
	Project string
	// Network is the single isolated per-Session bridge. Internal == default-deny
	// egress (ADR-0007).
	Network NetworkSpec
	// Volumes are platform-managed (named) volumes only — never host bind mounts.
	Volumes []VolumeSpec
	// Services are the containers in dependency order is expressed via DependsOn.
	Services []ServiceSpec
}

// NetworkSpec is the per-Session bridge network.
type NetworkSpec struct {
	Name string
	// Internal, when true, gives the network no route to the internet
	// (default-deny egress, ADR-0007).
	Internal bool
}

// VolumeSpec is a platform-managed named volume (e.g. the ephemeral DB data
// dir), torn down with the Session.
type VolumeSpec struct {
	Name string
}

// ServiceSpec is one fully-locked-down container.
type ServiceSpec struct {
	Name  string
	Image string
	// Runtime is the container runtime: "runsc" (gVisor) in prod, overridable to
	// "runc" for local dev where gVisor is absent (ADR-0002/0009).
	Runtime   string
	Resources Resources
	// Networks the container joins (always exactly the per-Session network).
	Networks []string
	// Env is the resolved, scoped environment — static + platform values only;
	// never owner secrets, never platform secrets (ADR-0003).
	Env map[string]string
	// User forces a non-root uid:gid for app services ("" leaves the image's own
	// non-root user, used for the DB image which drops privileges itself).
	User string
	// ReadOnlyRootFS makes the root filesystem read-only (writes go to TmpFS).
	ReadOnlyRootFS bool
	// TmpFS are in-memory writable mounts for an otherwise read-only container.
	TmpFS []string
	// Volumes are named-volume mounts ("volume:/path"); never host bind mounts.
	Volumes []string
	// CapDrop drops Linux capabilities ("ALL").
	CapDrop []string
	// SecurityOpt carries hardening flags (e.g. no-new-privileges).
	SecurityOpt []string
	// Restart is the restart policy ("on-failure" for the transient-blip grace
	// window of ADR-0006; "no" for one-shots).
	Restart string
	// Healthcheck gates readiness (ADR-0004); nil leaves the image's HEALTHCHECK.
	Healthcheck *Healthcheck
	// Command overrides the image entrypoint args (used by the seed one-shot).
	Command []string
	// DependsOn expresses ordering; the value is the required upstream condition.
	DependsOn map[string]DependCondition
}

// DependCondition is a Compose depends_on condition.
type DependCondition string

const (
	// DependHealthy waits for the dependency's healthcheck to pass.
	DependHealthy DependCondition = "service_healthy"
	// DependCompleted waits for the dependency to exit successfully (the seed).
	DependCompleted DependCondition = "service_completed_successfully"
)

// Resources are the per-container caps that bound blast radius (ADR-0002).
type Resources struct {
	MemoryBytes int64
	CPUs        float64
	PidsLimit   int64
}

// Healthcheck is a container readiness probe.
type Healthcheck struct {
	Test     []string
	Interval string
	Timeout  string
	Retries  int
}

// AppServices returns the UI and API services (everything the platform builds
// from Owner repos), excluding the platform-provided DB and seed one-shot.
func (p ExecutionPlan) AppServices() []ServiceSpec {
	out := make([]ServiceSpec, 0, len(p.Services))
	for _, s := range p.Services {
		if s.Name == dbServiceName || s.Name == seedServiceName {
			continue
		}
		out = append(out, s)
	}
	return out
}
