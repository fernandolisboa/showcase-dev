// Package config loads control-plane configuration from the environment. Per
// ADR-0009 the control plane is a single Go process on one VM, so config is
// plain environment variables; production secrets come from Azure Key Vault.
package config

import (
	"net"
	"os"
	"strconv"
	"time"
)

// Config is the resolved control-plane configuration.
type Config struct {
	// Host is the interface the HTTP server binds to ("" = all interfaces).
	Host string
	// Port is the HTTP port the control plane listens on.
	Port int
	// Env names the runtime environment ("dev", "prod").
	Env string
	// DatabaseURL is the Postgres DSN. Optional during scaffolding — the write
	// model lands in a later slice.
	DatabaseURL string
	// DemoDomain is the registrable demo domain (ADR-0004); Sessions are served
	// at s-<id>.run.<DemoDomain>. Dev defaults to "localhost".
	DemoDomain string
	// BootingBackendURL is the address Traefik routes a booting Session to (the
	// control-plane splash). Empty lets the caller derive a dev default.
	BootingBackendURL string
	// InternalHost is the interface the Traefik-facing internal listeners (the
	// /traefik config endpoint and the booting splash) bind to. It defaults to
	// 127.0.0.1 so the control-plane surface is never published on a
	// Session-reachable interface (ADR-0004). In prod Traefik is co-located on the
	// same VM and reaches it over loopback; the dev Traefik runs in a container and
	// must override this to the Docker bridge gateway (see README).
	InternalHost string
	// InternalPort is the config listener (Traefik's /traefik dynamic config);
	// kept off the public app port (ADR-0004).
	InternalPort int
	// SplashPort is the booting-splash listener a Session is routed to while its
	// Stack boots. It is a SEPARATE listener from InternalPort with NO /traefik
	// route, so a Demo forwarded here can never read the backend map (ADR-0004).
	SplashPort int
	// Runtime is the container runtime the Runner boots Sessions under: "runsc"
	// (gVisor, prod) or "runc" (local dev where gVisor is absent) — ADR-0002/0009.
	Runtime string
	// TraefikContainer is the Traefik container the Runner attaches to each Session
	// network so it can reach the live Stack (#9).
	TraefikContainer string
	// EgressProxyImage is the platform-owned forward-proxy image for a Session's
	// egress allow-list sidecar (ADR-0011). Used only when a Project declares an
	// allow-list; the compiler errors if a list is declared without it.
	EgressProxyImage string
	// GitHubClientID / GitHubClientSecret are the GitHub App's user-to-server OAuth
	// credentials for Owner sign-in (ADR-0008, issue #4). When either is empty,
	// sign-in is disabled (the dev loop runs without a registered App); the App is
	// registered by a human and the secret comes from Key Vault in prod.
	GitHubClientID     string
	GitHubClientSecret string
	// OAuthCallbackURL is the absolute URL GitHub redirects back to after sign-in.
	// It MUST match a callback URL configured on the GitHub App exactly. Empty
	// derives a dev default from the listen address.
	OAuthCallbackURL string
	// DemoScheme is the URL scheme for Session links ("http" dev, "https" prod).
	DemoScheme string
	// IdleTimeout tears down a Session after this long with no Guest activity —
	// the primary cost lever (ADR-0006). Activity is Traefik per-Session request
	// counts (TraefikMetricsURL); when metrics are unavailable, idle teardown is
	// skipped that tick (MaxRuntime still applies).
	IdleTimeout time.Duration
	// MaxRuntime is the hard absolute cap on a Session's lifetime regardless of
	// activity, so a forgotten tab can't run forever (ADR-0006). It is the
	// unconditional backstop and needs no activity signal.
	MaxRuntime time.Duration
	// ReaperInterval is how often the teardown reaper scans Sessions. It bounds the
	// granularity of both timeouts above.
	ReaperInterval time.Duration
	// TraefikMetricsURL is the Traefik Prometheus metrics endpoint the reaper polls
	// for per-Session request counts (the idle activity signal). It must be reached
	// off the Guest data path — see cmd/controlplane and deploy/traefik (ADR-0004).
	TraefikMetricsURL string
	// CrashGrace is how long a live Session may report unhealthy before it's torn
	// down as crashed — the window for the restart policy to absorb a transient
	// blip (ADR-0006, #13).
	CrashGrace time.Duration
	// FailureLinger is how long a Failed/Crashed Session's route is kept so the
	// Guest's page lands on the failure message before the reaper removes it (#13).
	FailureLinger time.Duration
	// MaxCachedImages caps how many built Project images the builder keeps before
	// LRU-evicting the coldest (ADR-0003). Image storage is cheap; this just bounds
	// disk growth across many Projects/commits.
	MaxCachedImages int
	// BuildSandboxImage is the rootless image builder run per untrusted Owner build
	// (ADR-0010): a daemonless BuildKit that exports an OCI tar the platform loads.
	// Defaults to moby/buildkit:rootless.
	BuildSandboxImage string
	// BuildNetwork is the dedicated bridge each build joins — internet egress for
	// dependencies (ADR-0007) but a separate segment with no route to the control
	// plane or Sessions (ADR-0010). Created on demand.
	BuildNetwork string
	// BuildMemoryMB, BuildCPUs and BuildPidsLimit cap a build's resources so a
	// runaway RUN (fork bomb, memory hog) is bounded (ADR-0010), mirroring the
	// runtime caps of ADR-0002.
	BuildMemoryMB  int
	BuildCPUs      float64
	BuildPidsLimit int
	// BuildTimeout is the wall-clock cap per build; on expiry the build container is
	// force-removed (ADR-0010).
	BuildTimeout time.Duration
	// MaxSessions is the global cap on concurrent Sessions (ADR-0006), sized to host
	// capacity ÷ per-Session budget (ADR-0002). At the cap a play request is
	// rejected with an "at capacity" response rather than queued. A value <= 0
	// disables the cap (unlimited) — an explicit operator escape hatch, not the
	// default.
	MaxSessions int
}

// Load reads configuration from the environment, applying defaults.
func Load() Config {
	return Config{
		Host:               getenv("HOST", ""),
		Port:               getenvInt("PORT", 8080),
		Env:                getenv("ENV", "dev"),
		DatabaseURL:        getenv("DATABASE_URL", ""),
		DemoDomain:         getenv("DEMO_DOMAIN", "localhost"),
		BootingBackendURL:  getenv("PROXY_BOOTING_URL", ""),
		InternalHost:       getenv("INTERNAL_HOST", "127.0.0.1"),
		InternalPort:       getenvInt("INTERNAL_PORT", 8081),
		SplashPort:         getenvInt("SPLASH_PORT", 8082),
		Runtime:            getenv("RUNTIME", "runsc"),
		TraefikContainer:   getenv("TRAEFIK_CONTAINER", "showcase-traefik"),
		EgressProxyImage:   getenv("EGRESS_PROXY_IMAGE", "showcase-dev/egress-proxy:latest"),
		GitHubClientID:     getenv("GITHUB_APP_CLIENT_ID", ""),
		GitHubClientSecret: getenv("GITHUB_APP_CLIENT_SECRET", ""),
		OAuthCallbackURL:   getenv("OAUTH_CALLBACK_URL", ""),
		DemoScheme:         getenv("DEMO_SCHEME", "http"),
		IdleTimeout:        getenvDuration("IDLE_TIMEOUT", 15*time.Minute),
		MaxRuntime:         getenvDuration("MAX_RUNTIME", 45*time.Minute),
		ReaperInterval:     getenvDuration("REAPER_INTERVAL", 30*time.Second),
		TraefikMetricsURL:  getenv("TRAEFIK_METRICS_URL", "http://127.0.0.1:8084/metrics"),
		CrashGrace:         getenvDuration("CRASH_GRACE", 30*time.Second),
		FailureLinger:      getenvDuration("FAILURE_LINGER", 2*time.Minute),
		MaxCachedImages:    getenvInt("MAX_CACHED_IMAGES", 20),
		MaxSessions:        getenvInt("MAX_SESSIONS", 10),
		BuildSandboxImage:  getenv("BUILD_SANDBOX_IMAGE", "moby/buildkit:rootless"),
		BuildNetwork:       getenv("BUILD_NETWORK", "showcase-build"),
		BuildMemoryMB:      getenvInt("BUILD_MEMORY_MB", 2048),
		BuildCPUs:          getenvFloat("BUILD_CPUS", 2.0),
		BuildPidsLimit:     getenvInt("BUILD_PIDS_LIMIT", 2048),
		BuildTimeout:       getenvDuration("BUILD_TIMEOUT", 5*time.Minute),
	}
}

// Addr returns the host:port the server binds to.
func (c Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getenvFloat(key string, fallback float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

// getenvDuration parses a Go duration string (e.g. "15m", "45s"); a missing or
// malformed value falls back, so a typo can't silently disable a teardown timer.
func getenvDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
