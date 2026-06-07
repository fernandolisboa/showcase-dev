package config

import "testing"

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
