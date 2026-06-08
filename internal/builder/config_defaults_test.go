package builder

import (
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/config"
)

// TestConfigDefaultsMatchSandboxDefaults guards against the build-sandbox defaults
// in internal/config drifting from the builder's own defaults. With no build env
// set, config.Load() must yield exactly defaultSandbox(), so the security-relevant
// caps (ADR-0010) can never silently disagree between the two default sources —
// production wires the builder via WithSandbox(cfg...), while New() without options
// (the integration tests) uses defaultSandbox() directly.
func TestConfigDefaultsMatchSandboxDefaults(t *testing.T) {
	for _, k := range []string{"BUILD_SANDBOX_IMAGE", "BUILD_NETWORK", "BUILD_MEMORY_MB", "BUILD_CPUS", "BUILD_PIDS_LIMIT", "BUILD_TIMEOUT"} {
		t.Setenv(k, "")
	}
	cfg := config.Load()
	fromConfig := Sandbox{
		Image:     cfg.BuildSandboxImage,
		Network:   cfg.BuildNetwork,
		MemoryMB:  cfg.BuildMemoryMB,
		CPUs:      cfg.BuildCPUs,
		PidsLimit: cfg.BuildPidsLimit,
		Timeout:   cfg.BuildTimeout,
	}
	if fromConfig != defaultSandbox() {
		t.Errorf("config build defaults drift from builder.defaultSandbox():\n config  = %+v\n builder = %+v", fromConfig, defaultSandbox())
	}
}
