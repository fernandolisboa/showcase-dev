package config

import (
	"testing"
	"time"
)

func TestTeardownTunablesParseAndDefault(t *testing.T) {
	t.Setenv("IDLE_TIMEOUT", "12m")
	t.Setenv("MAX_RUNTIME", "90m")
	t.Setenv("REAPER_INTERVAL", "garbage") // malformed must fall back, not disable

	cfg := Load()
	if cfg.IdleTimeout != 12*time.Minute {
		t.Errorf("IdleTimeout = %v, want 12m", cfg.IdleTimeout)
	}
	if cfg.MaxRuntime != 90*time.Minute {
		t.Errorf("MaxRuntime = %v, want 90m", cfg.MaxRuntime)
	}
	if cfg.ReaperInterval != 30*time.Second {
		t.Errorf("ReaperInterval = %v, want default 30s on garbage input", cfg.ReaperInterval)
	}
}

func TestFailureTunablesParseAndDefault(t *testing.T) {
	t.Setenv("CRASH_GRACE", "45s")
	t.Setenv("FAILURE_LINGER", "junk") // malformed must fall back

	cfg := Load()
	if cfg.CrashGrace != 45*time.Second {
		t.Errorf("CrashGrace = %v, want 45s", cfg.CrashGrace)
	}
	if cfg.FailureLinger != 2*time.Minute {
		t.Errorf("FailureLinger = %v, want default 2m on garbage", cfg.FailureLinger)
	}
}

func TestMaxSessionsParsesAndDefaults(t *testing.T) {
	t.Setenv("MAX_SESSIONS", "3")
	if got := Load().MaxSessions; got != 3 {
		t.Errorf("MaxSessions = %d, want 3", got)
	}

	t.Setenv("MAX_SESSIONS", "")
	if got := Load().MaxSessions; got != 10 {
		t.Errorf("MaxSessions = %d, want default 10", got)
	}
}

func TestMaxCachedImagesParsesAndDefaults(t *testing.T) {
	t.Setenv("MAX_CACHED_IMAGES", "5")
	if got := Load().MaxCachedImages; got != 5 {
		t.Errorf("MaxCachedImages = %d, want 5", got)
	}

	t.Setenv("MAX_CACHED_IMAGES", "")
	if got := Load().MaxCachedImages; got != 20 {
		t.Errorf("MaxCachedImages = %d, want default 20", got)
	}
}

func TestEgressProxyImageParsesAndDefaults(t *testing.T) {
	t.Setenv("EGRESS_PROXY_IMAGE", "ghcr.io/me/egress-proxy:pinned")
	if got := Load().EgressProxyImage; got != "ghcr.io/me/egress-proxy:pinned" {
		t.Errorf("EgressProxyImage = %q, want override", got)
	}
	t.Setenv("EGRESS_PROXY_IMAGE", "")
	if got := Load().EgressProxyImage; got != "showcase-dev/egress-proxy:latest" {
		t.Errorf("EgressProxyImage = %q, want default", got)
	}
}

func TestBuildSandboxTunablesParseAndDefault(t *testing.T) {
	t.Setenv("BUILD_SANDBOX_IMAGE", "ghcr.io/me/buildkit:pinned")
	t.Setenv("BUILD_NETWORK", "custom-build-net")
	t.Setenv("BUILD_MEMORY_MB", "512")
	t.Setenv("BUILD_CPUS", "1.5")
	t.Setenv("BUILD_PIDS_LIMIT", "256")
	t.Setenv("BUILD_TIMEOUT", "90s")

	cfg := Load()
	if cfg.BuildSandboxImage != "ghcr.io/me/buildkit:pinned" {
		t.Errorf("BuildSandboxImage = %q", cfg.BuildSandboxImage)
	}
	if cfg.BuildNetwork != "custom-build-net" {
		t.Errorf("BuildNetwork = %q", cfg.BuildNetwork)
	}
	if cfg.BuildMemoryMB != 512 {
		t.Errorf("BuildMemoryMB = %d, want 512", cfg.BuildMemoryMB)
	}
	if cfg.BuildCPUs != 1.5 {
		t.Errorf("BuildCPUs = %v, want 1.5", cfg.BuildCPUs)
	}
	if cfg.BuildPidsLimit != 256 {
		t.Errorf("BuildPidsLimit = %d, want 256", cfg.BuildPidsLimit)
	}
	if cfg.BuildTimeout != 90*time.Second {
		t.Errorf("BuildTimeout = %v, want 90s", cfg.BuildTimeout)
	}
}

func TestBuildCPUsFallsBackOnGarbage(t *testing.T) {
	// A malformed cap must fall back to the default, never 0 — a 0 cap would
	// silently disable the CPU bound on untrusted builds (ADR-0010 AC2).
	t.Setenv("BUILD_CPUS", "not-a-float")
	if got := Load().BuildCPUs; got != 2.0 {
		t.Errorf("BuildCPUs = %v, want default 2.0 on garbage (the cap must stay on)", got)
	}
}

func TestLoadReadsEnv(t *testing.T) {
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("PORT", "9090")
	t.Setenv("ENV", "prod")
	t.Setenv("DATABASE_URL", "postgres://x")

	cfg := Load()

	if cfg.Host != "127.0.0.1" {
		t.Errorf("Host = %q, want 127.0.0.1", cfg.Host)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.Port)
	}
	if cfg.Env != "prod" {
		t.Errorf("Env = %q, want prod", cfg.Env)
	}
	if got, want := cfg.Addr(), "127.0.0.1:9090"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}

func TestPortFallsBackOnGarbage(t *testing.T) {
	t.Setenv("PORT", "not-a-number")
	if got := Load().Port; got != 8080 {
		t.Errorf("Port = %d, want default 8080", got)
	}
}

func TestAddrDefaultBindsAllInterfaces(t *testing.T) {
	c := Config{Port: 8080}
	if got, want := c.Addr(), ":8080"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}
