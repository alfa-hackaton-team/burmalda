package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeYAMLReloadIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("source_poll:\n  lease: 45s\nfeeds:\n  max_article_entries: 3\n")
	manager, err := NewManager(path, DefaultRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Current(); got.SourcePoll.Lease.Std() != 45*time.Second || got.Feeds.MaxArticleEntries != 3 {
		t.Fatalf("initial config: %+v", got)
	}
	write("feeds:\n  max_article_entries: 0\n")
	if err := manager.Reload(); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if manager.Current().Feeds.MaxArticleEntries != 3 {
		t.Fatal("invalid reload replaced current config")
	}
	write("database:\n  max_conns: 20\nfeeds:\n  max_article_entries: 7\n")
	if err := manager.Reload(); err == nil {
		t.Fatal("startup-only change accepted on reload")
	}
	if manager.Current().Feeds.MaxArticleEntries != 3 {
		t.Fatal("partial reload applied")
	}
	write("feeds:\n  max_article_entries: 7\n")
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := manager.Current(); got.Feeds.MaxArticleEntries != 7 || got.SourcePoll.Lease.Std() != 2*time.Minute {
		t.Fatalf("reload did not reset omitted values to defaults: %+v", got)
	}
}

func TestRuntimeYAMLRejectsUnknownAndMalformedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	for _, data := range []string{
		"feeds:\n  mystery: 10\n",
		"feeds:\n  request_timeout: 0s\n",
		"feeds:\n  request_timeout: 10\n",
		"feeds:\n  max_article_entries: 600\n",
		"article_retry:\n  max_attempts: 0\n",
		"article_retry:\n  lease: 10s\n",
		"feeds:\n  max_entries: 3\n---\nfeeds:\n  max_entries: 4\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewManager(path, DefaultRuntime()); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}

func TestExampleRuntimeYAML(t *testing.T) {
	manager, err := NewManager("../../config.example.yaml", DefaultRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if manager.Current().Feeds.MaxArticleEntries != 20 {
		t.Fatal("unexpected example limits")
	}
}

func TestLoadUsesRuntimeYAMLOverEnvironmentDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(path, []byte("database:\n  max_conns: 13\nsource_poll:\n  idle: 1s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("DB_MAX_CONNS", "7")
	t.Setenv("SOURCE_POLL_IDLE", "9s")
	t.Setenv("BACKEND_CONFIG_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxConns != 13 || cfg.Runtime.SourcePoll.Idle.Std() != time.Second {
		t.Fatalf("YAML did not override environment defaults: %+v", cfg)
	}
}
