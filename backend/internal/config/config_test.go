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
	t.Setenv("SOURCE_POLL_LEASE", "90s")
	t.Setenv("SOURCE_POLL_IDLE", "2s")
	t.Setenv("SOURCE_POLL_REAP_BATCH", "40")
	t.Setenv("ML_PIPELINE_VERSION", "test-v1")
	t.Setenv("ML_MAX_TEXT_BYTES", "1000")
	t.Setenv("ML_BASE_URL", "https://ml.example")
	t.Setenv("ML_SERVICE_TOKEN", "secret")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.DBMaxConns != 7 || cfg.DatabaseConnectTimeout != 3*time.Second || cfg.ReadinessTimeout != time.Second || cfg.ShutdownTimeout != 8*time.Second || cfg.SourcePollLease != 90*time.Second || cfg.SourcePollIdle != 2*time.Second || cfg.SourcePollReapBatch != 40 || cfg.MLPipelineVersion != "test-v1" || cfg.MLMaxTextBytes != 1000 || cfg.MLBaseURL != "https://ml.example" {
		t.Fatalf("unexpected configuration: %+v", cfg)
	}
}

func TestLoadRejectsIncompleteMLConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("ML_PIPELINE_VERSION", "test-v1")
	t.Setenv("ML_MAX_TEXT_BYTES", "")
	t.Setenv("ML_BASE_URL", "")
	t.Setenv("ML_SERVICE_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("pipeline without text limit was accepted")
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("DB_MAX_CONNS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid DB_MAX_CONNS to fail")
	}
}
