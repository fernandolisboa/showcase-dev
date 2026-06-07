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
		Networks: map[string]composeNetwork{p.Network.Name: {Internal: p.Network.Internal}},
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
			Volumes:     s.Volumes,
			Networks:    s.Networks,
			Command:     s.Command,
		}
		if len(s.DependsOn) > 0 {
			cs.DependsOn = make(map[string]composeDependency, len(s.DependsOn))
			for dep, cond := range s.DependsOn {
				cs.DependsOn[dep] = composeDependency{Condition: string(cond)}
			}
		}
		if s.Healthcheck != nil {
			cs.Healthcheck = &composeHealthcheck{
				Test:     s.Healthcheck.Test,
				Interval: s.Healthcheck.Interval,
				Timeout:  s.Healthcheck.Timeout,
				Retries:  s.Healthcheck.Retries,
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
	Volumes     []string                     `yaml:"volumes,omitempty"`
	Networks    []string                     `yaml:"networks,omitempty"`
	DependsOn   map[string]composeDependency `yaml:"depends_on,omitempty"`
	Healthcheck *composeHealthcheck          `yaml:"healthcheck,omitempty"`
	Command     []string                     `yaml:"command,omitempty"`
}

type composeDependency struct {
	Condition string `yaml:"condition"`
}

type composeHealthcheck struct {
	Test     []string `yaml:"test,omitempty"`
	Interval string   `yaml:"interval,omitempty"`
	Timeout  string   `yaml:"timeout,omitempty"`
	Retries  int      `yaml:"retries,omitempty"`
}

type composeNetwork struct {
	Internal bool `yaml:"internal,omitempty"`
}

type composeVolume struct{}
