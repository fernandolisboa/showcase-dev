// Package config loads control-plane configuration from the environment. Per
// ADR-0009 the control plane is a single Go process on one VM, so config is
// plain environment variables; production secrets come from Azure Key Vault.
package config

import (
	"net"
	"os"
	"strconv"
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
}

// Load reads configuration from the environment, applying defaults.
func Load() Config {
	return Config{
		Host:        getenv("HOST", ""),
		Port:        getenvInt("PORT", 8080),
		Env:         getenv("ENV", "dev"),
		DatabaseURL: getenv("DATABASE_URL", ""),
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
