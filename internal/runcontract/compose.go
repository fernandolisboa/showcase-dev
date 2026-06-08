package runcontract

import "gopkg.in/yaml.v3"

// ToCompose renders the ExecutionPlan as a Docker Compose v2 document — the
// artifact the Runner writes to disk and runs with `docker compose -p <project>
// up` (ADR-0009). Map keys are emitted in sorted order, so the output is
// deterministic for a given plan.
func (p ExecutionPlan) ToCompose() ([]byte, error) {
	file := composeFile{
		Name:     p.Project,
		Services: make(map[string]composeService, len(p.Services)),
		// Set the network's explicit name so Docker uses it verbatim (no project
		// prefix) — the Runner and proxy attach to it by that name (ADR-0009).
		Networks: map[string]composeNetwork{p.Network.Name: {Name: p.Network.Name, Internal: p.Network.Internal}},
	}
	if len(p.Volumes) > 0 {
		file.Volumes = make(map[string]composeVolume, len(p.Volumes))
		for _, v := range p.Volumes {
			file.Volumes[v.Name] = composeVolume{}
		}
	}

	for _, s := range p.Services {
		cs := composeService{
			Image:       s.Image,
			Runtime:     s.Runtime,
			MemLimit:    s.Resources.MemoryBytes,
			CPUs:        s.Resources.CPUs,
			PidsLimit:   s.Resources.PidsLimit,
			ReadOnly:    s.ReadOnlyRootFS,
			TmpFS:       s.TmpFS,
			User:        s.User,
			CapDrop:     s.CapDrop,
			SecurityOpt: s.SecurityOpt,
			Restart:     s.Restart,
			Environment: s.Env,
			Networks:    s.Networks,
			Command:     s.Command,
		}
		// Long-form volume syntax (type: volume) so a source can never be parsed
		// as a host bind mount — the structural backstop for the no-bind-mounts
		// invariant (ADR-0003), independent of any source-name validation.
		for _, v := range s.Volumes {
			cs.Volumes = append(cs.Volumes, composeVolumeMount{
				Type:   "volume",
				Source: v.Source,
				Target: v.Target,
			})
		}
		if len(s.DependsOn) > 0 {
			cs.DependsOn = make(map[string]composeDependency, len(s.DependsOn))
			for dep, cond := range s.DependsOn {
				cs.DependsOn[dep] = composeDependency{Condition: string(cond)}
			}
		}
		if s.Healthcheck != nil {
			cs.Healthcheck = &composeHealthcheck{
				Test:        s.Healthcheck.Test,
				Interval:    s.Healthcheck.Interval,
				Timeout:     s.Healthcheck.Timeout,
				Retries:     s.Healthcheck.Retries,
				StartPeriod: s.Healthcheck.StartPeriod,
			}
		}
		file.Services[s.Name] = cs
	}

	return yaml.Marshal(file)
}

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNetwork `yaml:"networks,omitempty"`
	Volumes  map[string]composeVolume  `yaml:"volumes,omitempty"`
}

type composeService struct {
	Image       string                       `yaml:"image"`
	Runtime     string                       `yaml:"runtime,omitempty"`
	MemLimit    int64                        `yaml:"mem_limit,omitempty"`
	CPUs        float64                      `yaml:"cpus,omitempty"`
	PidsLimit   int64                        `yaml:"pids_limit,omitempty"`
	ReadOnly    bool                         `yaml:"read_only,omitempty"`
	TmpFS       []string                     `yaml:"tmpfs,omitempty"`
	User        string                       `yaml:"user,omitempty"`
	CapDrop     []string                     `yaml:"cap_drop,omitempty"`
	SecurityOpt []string                     `yaml:"security_opt,omitempty"`
	Restart     string                       `yaml:"restart,omitempty"`
	Environment map[string]string            `yaml:"environment,omitempty"`
	Volumes     []composeVolumeMount         `yaml:"volumes,omitempty"`
	Networks    []string                     `yaml:"networks,omitempty"`
	DependsOn   map[string]composeDependency `yaml:"depends_on,omitempty"`
	Healthcheck *composeHealthcheck          `yaml:"healthcheck,omitempty"`
	Command     []string                     `yaml:"command,omitempty"`
}

// composeVolumeMount is the long-form Compose volume mount. Emitting an explicit
// `type: volume` (rather than the "source:target" short string) guarantees the
// source is treated as a named volume and never as a host path / bind mount.
type composeVolumeMount struct {
	Type   string `yaml:"type"`
	Source string `yaml:"source"`
	Target string `yaml:"target"`
}

type composeDependency struct {
	Condition string `yaml:"condition"`
}

type composeHealthcheck struct {
	Test        []string `yaml:"test,omitempty"`
	Interval    string   `yaml:"interval,omitempty"`
	Timeout     string   `yaml:"timeout,omitempty"`
	Retries     int      `yaml:"retries,omitempty"`
	StartPeriod string   `yaml:"start_period,omitempty"`
}

type composeNetwork struct {
	Name     string `yaml:"name,omitempty"`
	Internal bool   `yaml:"internal,omitempty"`
}

type composeVolume struct{}
