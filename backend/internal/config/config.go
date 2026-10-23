package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL            string
	HTTPAddr               string
	DBMaxConns             int32
	DatabaseConnectTimeout time.Duration
	ReadinessTimeout       time.Duration
	ShutdownTimeout        time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.HTTPAddr == "" {
		return Config{}, fmt.Errorf("HTTP_ADDR must not be empty")
	}

	maxConns, err := strconv.ParseInt(getenv("DB_MAX_CONNS", "10"), 10, 32)
	if err != nil || maxConns < 1 {
		return Config{}, fmt.Errorf("DB_MAX_CONNS must be a positive integer")
	}
	cfg.DBMaxConns = int32(maxConns)

	for _, item := range []struct {
		name  string
		value *time.Duration
		def   string
	}{
		{"DB_CONNECT_TIMEOUT", &cfg.DatabaseConnectTimeout, "5s"},
		{"READINESS_TIMEOUT", &cfg.ReadinessTimeout, "2s"},
		{"SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout, "10s"},
	} {
		duration, err := time.ParseDuration(getenv(item.name, item.def))
		if err != nil || duration <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration", item.name)
		}
		*item.value = duration
	}

	return cfg, nil
}

func getenv(name, fallback string) string {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	return value
}
