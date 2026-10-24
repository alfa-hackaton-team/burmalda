package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ConfigFile             string
	Runtime                Runtime
	RuntimeBase            Runtime
	DatabaseURL            string
	HTTPAddr               string
	DBMaxConns             int32
	DatabaseConnectTimeout time.Duration
	ReadinessTimeout       time.Duration
	ShutdownTimeout        time.Duration
	SourcePollLease        time.Duration
	SourcePollIdle         time.Duration
	SourcePollReapBatch    int
	MLPipelineVersion      string
	MLMaxTextBytes         int
	MLBaseURL              string
	MLServiceToken         string
	RawStorageBackend      string
	S3Bucket               string
	S3Region               string
	S3Endpoint             string
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		ConfigFile:  os.Getenv("BACKEND_CONFIG_FILE"),
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
		{"SOURCE_POLL_LEASE", &cfg.SourcePollLease, "2m"},
		{"SOURCE_POLL_IDLE", &cfg.SourcePollIdle, "5s"},
	} {
		duration, err := time.ParseDuration(getenv(item.name, item.def))
		if err != nil || duration <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration", item.name)
		}
		*item.value = duration
	}
	if cfg.SourcePollLease < 3*time.Second {
		return Config{}, fmt.Errorf("SOURCE_POLL_LEASE must be at least 3s")
	}
	reapBatch, err := strconv.Atoi(getenv("SOURCE_POLL_REAP_BATCH", "100"))
	if err != nil || reapBatch < 1 || reapBatch > 1000 {
		return Config{}, fmt.Errorf("SOURCE_POLL_REAP_BATCH must be between 1 and 1000")
	}
	cfg.SourcePollReapBatch = reapBatch
	cfg.RuntimeBase = DefaultRuntime()
	cfg.RuntimeBase.Database.MaxConns = int(cfg.DBMaxConns)
	cfg.RuntimeBase.Database.ConnectTimeout = Duration(cfg.DatabaseConnectTimeout)
	cfg.RuntimeBase.Server.ReadinessTimeout = Duration(cfg.ReadinessTimeout)
	cfg.RuntimeBase.Server.ShutdownTimeout = Duration(cfg.ShutdownTimeout)
	cfg.RuntimeBase.SourcePoll.Lease = Duration(cfg.SourcePollLease)
	cfg.RuntimeBase.SourcePoll.Idle = Duration(cfg.SourcePollIdle)
	cfg.RuntimeBase.SourcePoll.ReapBatch = cfg.SourcePollReapBatch
	cfg.Runtime, err = readRuntime(cfg.ConfigFile, cfg.RuntimeBase)
	if err != nil {
		return Config{}, fmt.Errorf("load BACKEND_CONFIG_FILE: %w", err)
	}
	cfg.DBMaxConns = int32(cfg.Runtime.Database.MaxConns)
	cfg.DatabaseConnectTimeout = cfg.Runtime.Database.ConnectTimeout.Std()
	cfg.MLPipelineVersion = strings.TrimSpace(os.Getenv("ML_PIPELINE_VERSION"))
	limitRaw := strings.TrimSpace(os.Getenv("ML_MAX_TEXT_BYTES"))
	if (cfg.MLPipelineVersion == "") != (limitRaw == "") {
		return Config{}, fmt.Errorf("ML_PIPELINE_VERSION and ML_MAX_TEXT_BYTES must be set together")
	}
	if limitRaw != "" {
		limit, err := strconv.Atoi(limitRaw)
		if err != nil || limit < 1 {
			return Config{}, fmt.Errorf("ML_MAX_TEXT_BYTES must be a positive integer")
		}
		cfg.MLMaxTextBytes = limit
	}
	cfg.MLBaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("ML_BASE_URL")), "/")
	cfg.MLServiceToken = os.Getenv("ML_SERVICE_TOKEN")
	if (cfg.MLBaseURL == "") != (cfg.MLServiceToken == "") {
		return Config{}, fmt.Errorf("ML_BASE_URL and ML_SERVICE_TOKEN must be set together")
	}
	if cfg.MLBaseURL != "" {
		u, err := url.Parse(cfg.MLBaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Path != "" || cfg.MLPipelineVersion == "" {
			return Config{}, fmt.Errorf("ML_BASE_URL must be an http(s) origin and ML pipeline must be configured")
		}
	}
	cfg.RawStorageBackend = getenv("RAW_STORAGE_BACKEND", "postgres")
	if cfg.RawStorageBackend != "postgres" && cfg.RawStorageBackend != "s3" {
		return Config{}, fmt.Errorf("RAW_STORAGE_BACKEND must be postgres or s3")
	}
	if cfg.RawStorageBackend == "s3" {
		cfg.S3Bucket = strings.TrimSpace(os.Getenv("S3_BUCKET"))
		cfg.S3Region = strings.TrimSpace(os.Getenv("S3_REGION"))
		cfg.S3Endpoint = strings.TrimRight(strings.TrimSpace(os.Getenv("S3_ENDPOINT")), "/")
		if cfg.S3Bucket == "" || cfg.S3Region == "" {
			return Config{}, fmt.Errorf("S3_BUCKET and S3_REGION are required for S3 storage")
		}
		if cfg.S3Endpoint != "" {
			u, err := url.Parse(cfg.S3Endpoint)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
				return Config{}, fmt.Errorf("S3_ENDPOINT must be an http(s) origin")
			}
		}
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
