package config

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("duration must be a string such as 5s")
	}
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*d = Duration(value)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Runtime struct {
	Database struct {
		MaxConns       int      `yaml:"max_conns"`
		ConnectTimeout Duration `yaml:"connect_timeout"`
	} `yaml:"database"`
	Server struct {
		ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
		ReadinessTimeout  Duration `yaml:"readiness_timeout"`
		ShutdownTimeout   Duration `yaml:"shutdown_timeout"`
	} `yaml:"server"`
	SourcePoll struct {
		Lease          Duration `yaml:"lease"`
		Idle           Duration `yaml:"idle"`
		ReapBatch      int      `yaml:"reap_batch"`
		FinishTimeout  Duration `yaml:"finish_timeout"`
		PersistTimeout Duration `yaml:"persist_timeout"`
	} `yaml:"source_poll"`
	ArticleRetry struct {
		Idle          Duration `yaml:"idle"`
		Lease         Duration `yaml:"lease"`
		FinishTimeout Duration `yaml:"finish_timeout"`
		MaxAttempts   int      `yaml:"max_attempts"`
		RetryStep     Duration `yaml:"retry_step"`
		RetryMax      Duration `yaml:"retry_max"`
		RetryAfterMax Duration `yaml:"retry_after_max"`
	} `yaml:"article_retry"`
	Feeds struct {
		MaxFeedBytes          int64    `yaml:"max_feed_bytes"`
		MaxArticleBytes       int64    `yaml:"max_article_bytes"`
		MaxEntries            int      `yaml:"max_entries"`
		MaxArticleEntries     int      `yaml:"max_article_entries"`
		MaxPDFPages           int      `yaml:"max_pdf_pages"`
		MaxPDFTextBytes       int64    `yaml:"max_pdf_text_bytes"`
		RequestTimeout        Duration `yaml:"request_timeout"`
		DNSLookupTimeout      Duration `yaml:"dns_lookup_timeout"`
		DialTimeout           Duration `yaml:"dial_timeout"`
		TLSHandshakeTimeout   Duration `yaml:"tls_handshake_timeout"`
		ResponseHeaderTimeout Duration `yaml:"response_header_timeout"`
		IdleConnTimeout       Duration `yaml:"idle_conn_timeout"`
		MaxIdleConns          int      `yaml:"max_idle_conns"`
		MaxConnsPerHost       int      `yaml:"max_conns_per_host"`
		MaxRedirects          int      `yaml:"max_redirects"`
		RetryAfterMax         Duration `yaml:"retry_after_max"`
		UserAgent             string   `yaml:"user_agent"`
	} `yaml:"feeds"`
	ML struct {
		Idle                  Duration `yaml:"idle"`
		Lease                 Duration `yaml:"lease"`
		FinishTimeout         Duration `yaml:"finish_timeout"`
		RequestTimeout        Duration `yaml:"request_timeout"`
		MaxResponseBytes      int64    `yaml:"max_response_bytes"`
		MaxAttempts           int      `yaml:"max_attempts"`
		RetryStep             Duration `yaml:"retry_step"`
		RetryMax              Duration `yaml:"retry_max"`
		RetryAfterMax         Duration `yaml:"retry_after_max"`
		StatusPollInterval    Duration `yaml:"status_poll_interval"`
		MaxProcessingDuration Duration `yaml:"max_processing_duration"`
	} `yaml:"ml"`
	Retention struct {
		SourcePollDays int      `yaml:"source_poll_days"`
		BatchSize      int      `yaml:"batch_size"`
		Interval       Duration `yaml:"interval"`
	} `yaml:"retention"`
	RawStorage struct {
		RequestTimeout Duration `yaml:"request_timeout"`
	} `yaml:"raw_storage"`
}

func DefaultRuntime() Runtime {
	var r Runtime
	r.Database.MaxConns, r.Database.ConnectTimeout = 10, Duration(5*time.Second)
	r.Server.ReadHeaderTimeout, r.Server.ReadinessTimeout, r.Server.ShutdownTimeout = Duration(5*time.Second), Duration(2*time.Second), Duration(10*time.Second)
	r.SourcePoll.Lease, r.SourcePoll.Idle = Duration(2*time.Minute), Duration(5*time.Second)
	r.SourcePoll.ReapBatch, r.SourcePoll.FinishTimeout = 100, Duration(5*time.Second)
	r.SourcePoll.PersistTimeout = Duration(90 * time.Second)
	r.ArticleRetry.Idle, r.ArticleRetry.Lease, r.ArticleRetry.FinishTimeout = Duration(5*time.Second), Duration(2*time.Minute), Duration(5*time.Second)
	r.ArticleRetry.MaxAttempts = 5
	r.ArticleRetry.RetryStep, r.ArticleRetry.RetryMax, r.ArticleRetry.RetryAfterMax = Duration(30*time.Second), Duration(15*time.Minute), Duration(24*time.Hour)
	r.Feeds.MaxFeedBytes, r.Feeds.MaxArticleBytes = 4<<20, 4<<20
	r.Feeds.MaxEntries, r.Feeds.MaxArticleEntries = 500, 20
	r.Feeds.MaxPDFPages, r.Feeds.MaxPDFTextBytes = 100, 4<<20
	r.Feeds.RequestTimeout = Duration(20 * time.Second)
	r.Feeds.DNSLookupTimeout, r.Feeds.DialTimeout = Duration(3*time.Second), Duration(5*time.Second)
	r.Feeds.TLSHandshakeTimeout, r.Feeds.ResponseHeaderTimeout = Duration(5*time.Second), Duration(10*time.Second)
	r.Feeds.IdleConnTimeout = Duration(30 * time.Second)
	r.Feeds.MaxIdleConns, r.Feeds.MaxConnsPerHost, r.Feeds.MaxRedirects = 10, 2, 5
	r.Feeds.RetryAfterMax = Duration(24 * time.Hour)
	r.Feeds.UserAgent = "Burmalda/0.1 (+source-polling)"
	r.ML.Lease, r.ML.FinishTimeout, r.ML.RequestTimeout = Duration(2*time.Minute), Duration(5*time.Second), Duration(20*time.Second)
	r.ML.Idle = Duration(5 * time.Second)
	r.ML.MaxResponseBytes, r.ML.MaxAttempts = 1<<20, 12
	r.ML.RetryStep, r.ML.RetryMax, r.ML.RetryAfterMax = Duration(5*time.Second), Duration(time.Minute), Duration(time.Hour)
	r.ML.StatusPollInterval = Duration(5 * time.Second)
	r.ML.MaxProcessingDuration = Duration(30 * time.Minute)
	r.Retention.SourcePollDays, r.Retention.BatchSize, r.Retention.Interval = 30, 1000, Duration(24*time.Hour)
	r.RawStorage.RequestTimeout = Duration(30 * time.Second)
	return r
}

func (r Runtime) validate() error {
	for name, d := range map[string]Duration{
		"database.connect_timeout":   r.Database.ConnectTimeout,
		"server.read_header_timeout": r.Server.ReadHeaderTimeout,
		"server.readiness_timeout":   r.Server.ReadinessTimeout,
		"server.shutdown_timeout":    r.Server.ShutdownTimeout,
		"source_poll.lease":          r.SourcePoll.Lease, "source_poll.idle": r.SourcePoll.Idle,
		"source_poll.finish_timeout":  r.SourcePoll.FinishTimeout,
		"source_poll.persist_timeout": r.SourcePoll.PersistTimeout,
		"article_retry.idle":          r.ArticleRetry.Idle, "article_retry.lease": r.ArticleRetry.Lease,
		"article_retry.finish_timeout": r.ArticleRetry.FinishTimeout,
		"article_retry.retry_step":     r.ArticleRetry.RetryStep, "article_retry.retry_max": r.ArticleRetry.RetryMax,
		"article_retry.retry_after_max": r.ArticleRetry.RetryAfterMax,
		"feeds.request_timeout":         r.Feeds.RequestTimeout, "feeds.dns_lookup_timeout": r.Feeds.DNSLookupTimeout,
		"feeds.dial_timeout": r.Feeds.DialTimeout, "feeds.tls_handshake_timeout": r.Feeds.TLSHandshakeTimeout,
		"feeds.response_header_timeout": r.Feeds.ResponseHeaderTimeout, "feeds.idle_conn_timeout": r.Feeds.IdleConnTimeout,
		"feeds.retry_after_max": r.Feeds.RetryAfterMax,
		"ml.idle":               r.ML.Idle, "ml.lease": r.ML.Lease, "ml.finish_timeout": r.ML.FinishTimeout,
		"ml.request_timeout": r.ML.RequestTimeout, "ml.retry_step": r.ML.RetryStep,
		"ml.retry_max": r.ML.RetryMax, "ml.retry_after_max": r.ML.RetryAfterMax,
		"ml.status_poll_interval":     r.ML.StatusPollInterval,
		"ml.max_processing_duration":  r.ML.MaxProcessingDuration,
		"retention.interval":          r.Retention.Interval,
		"raw_storage.request_timeout": r.RawStorage.RequestTimeout,
	} {
		if d <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if r.Database.MaxConns < 1 || r.Database.MaxConns > 2147483647 {
		return fmt.Errorf("database.max_conns must be between 1 and 2147483647")
	}
	if r.SourcePoll.Lease < Duration(3*time.Second) || r.SourcePoll.FinishTimeout >= r.SourcePoll.Lease ||
		r.SourcePoll.ReapBatch < 1 || r.SourcePoll.ReapBatch > 1000 {
		return fmt.Errorf("invalid source_poll lease or reap_batch")
	}
	if r.ArticleRetry.MaxAttempts < 1 || r.ArticleRetry.MaxAttempts > 10000 ||
		r.ArticleRetry.RetryMax < r.ArticleRetry.RetryStep ||
		r.ArticleRetry.RetryAfterMax < r.ArticleRetry.RetryMax ||
		r.ArticleRetry.Lease <= r.Feeds.RequestTimeout+r.ArticleRetry.FinishTimeout {
		return fmt.Errorf("invalid article_retry attempts, delay or lease")
	}
	if r.Feeds.MaxFeedBytes < 1 || r.Feeds.MaxFeedBytes > 1<<30 ||
		r.Feeds.MaxArticleBytes < 1 || r.Feeds.MaxArticleBytes > 1<<30 ||
		r.Feeds.MaxEntries < 1 || r.Feeds.MaxEntries > 100000 ||
		r.Feeds.MaxArticleEntries < 1 || r.Feeds.MaxArticleEntries > r.Feeds.MaxEntries ||
		r.Feeds.MaxPDFPages < 1 || r.Feeds.MaxPDFPages > 10000 ||
		r.Feeds.MaxPDFTextBytes < 1 || r.Feeds.MaxPDFTextBytes > 1<<30 ||
		r.Feeds.MaxIdleConns < 1 || r.Feeds.MaxIdleConns > 10000 ||
		r.Feeds.MaxConnsPerHost < 1 || r.Feeds.MaxConnsPerHost > 1000 ||
		r.Feeds.MaxRedirects < 1 || r.Feeds.MaxRedirects > 100 || len(r.Feeds.UserAgent) < 1 || len(r.Feeds.UserAgent) > 256 {
		return fmt.Errorf("invalid feeds limits")
	}
	if r.ML.MaxResponseBytes < 1 || r.ML.MaxResponseBytes > 1<<30 ||
		r.ML.MaxAttempts < 1 || r.ML.MaxAttempts > 10000 ||
		r.ML.RetryMax < r.ML.RetryStep || r.ML.RetryAfterMax < r.ML.RetryMax ||
		r.ML.Lease <= r.ML.RequestTimeout+r.ML.FinishTimeout {
		return fmt.Errorf("invalid ml limits or retry intervals")
	}
	if r.Retention.SourcePollDays < 1 || r.Retention.SourcePollDays > 3650 ||
		r.Retention.BatchSize < 1 || r.Retention.BatchSize > 10000 {
		return fmt.Errorf("invalid retention source_poll_days or batch_size")
	}
	return nil
}

func readRuntime(path string, base Runtime) (Runtime, error) {
	if path == "" {
		return base, base.validate()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Runtime{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	value := base
	if err := decoder.Decode(&value); err != nil {
		return Runtime{}, fmt.Errorf("decode runtime YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Runtime{}, fmt.Errorf("runtime YAML must contain exactly one document")
	}
	if err := value.validate(); err != nil {
		return Runtime{}, err
	}
	return value, nil
}

type Manager struct {
	path  string
	base  Runtime
	value atomic.Pointer[Runtime]
}

func NewManager(path string, base Runtime) (*Manager, error) {
	m := &Manager{path: path, base: base}
	if err := m.Reload(); err != nil {
		return nil, err
	}
	return m, nil
}

func NewManagerFromConfig(cfg Config) *Manager {
	m := &Manager{path: cfg.ConfigFile, base: cfg.RuntimeBase}
	value := cfg.Runtime
	m.value.Store(&value)
	return m
}

func (m *Manager) Current() Runtime { return *m.value.Load() }

func (m *Manager) Reload() error {
	value, err := readRuntime(m.path, m.base)
	if err != nil {
		return err
	}
	if current := m.value.Load(); current != nil &&
		(value.Database != current.Database || value.Server.ReadHeaderTimeout != current.Server.ReadHeaderTimeout) {
		return fmt.Errorf("database settings and server.read_header_timeout require restart")
	}
	m.value.Store(&value)
	return nil
}

func WatchSIGHUP(ctx context.Context, m *Manager) {
	if m.path == "" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP)
	go func() {
		defer signal.Stop(signals)
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				if err := m.Reload(); err != nil {
					slog.Error("runtime config reload rejected", "error", err)
				} else {
					slog.Info("runtime config reloaded", "path", m.path)
				}
			}
		}
	}()
}
