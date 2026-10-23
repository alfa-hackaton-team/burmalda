package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("DB_MAX_CONNS", "7")
	t.Setenv("DB_CONNECT_TIMEOUT", "3s")
	t.Setenv("READINESS_TIMEOUT", "1s")
	t.Setenv("SHUTDOWN_TIMEOUT", "8s")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.DBMaxConns != 7 || cfg.DatabaseConnectTimeout != 3*time.Second || cfg.ReadinessTimeout != time.Second || cfg.ShutdownTimeout != 8*time.Second {
		t.Fatalf("unexpected configuration: %+v", cfg)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("DB_MAX_CONNS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid DB_MAX_CONNS to fail")
	}
}
