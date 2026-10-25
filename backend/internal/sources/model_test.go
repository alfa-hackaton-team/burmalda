package sources

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
)

type resolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (fn resolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return fn(ctx, host)
}

func validConfig() Config {
	return Config{
		Name: "Example", SourceType: "news", SourceRole: "external",
		TrustLevel: "reviewed", AdapterType: "rss",
		Endpoint: "https://news.example.org/feed", Enabled: true,
		PollingIntervalSeconds: 900,
	}
}

func TestValidateSourceConfig(t *testing.T) {
	validator := Validator{Resolver: resolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "news.example.org" {
			t.Fatalf("unexpected host %q", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	})}
	cfg := validConfig()
	if err := validator.Validate(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}
	if string(cfg.AdapterConfig) != "{}" {
		t.Fatalf("adapter_config = %s", cfg.AdapterConfig)
	}

	for _, endpoint := range []string{
		"http://127.0.0.1/feed", "http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/feed", "http://localhost/feed",
		"https://user:password@news.example.org/feed", "file:///etc/passwd",
	} {
		cfg := validConfig()
		cfg.Endpoint = endpoint
		if err := validator.Validate(context.Background(), &cfg); !errors.Is(err, ErrInvalid) {
			t.Errorf("endpoint %q: expected ErrInvalid, got %v", endpoint, err)
		}
	}

	privateDNS := Validator{Resolver: resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.168.1.5")}}, nil
	})}
	cfg = validConfig()
	if err := privateDNS.Validate(context.Background(), &cfg); !errors.Is(err, ErrInvalid) {
		t.Fatalf("private DNS accepted: %v", err)
	}
	if err := privateDNS.validate(context.Background(), &cfg, false); err != nil {
		t.Fatalf("safe edit of an existing source blocked by DNS: %v", err)
	}

	cfg = validConfig()
	cfg.AdapterType = "html"
	if err := validator.Validate(context.Background(), &cfg); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported adapter accepted: %v", err)
	}
	cfg = validConfig()
	cfg.AdapterConfig = json.RawMessage(`{"allow_private_network":true}`)
	if err := validator.Validate(context.Background(), &cfg); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported adapter option accepted: %v", err)
	}
}

func TestCreateAndPatchInput(t *testing.T) {
	full := []byte(`{"name":"Example","source_type":"news","source_role":"external","trust_level":"reviewed","adapter_type":"rss","endpoint":"https://news.example.org/feed","enabled":true,"polling_interval_seconds":900}`)
	cfg, err := ParseCreateJSON(full)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := ApplyPatch(cfg, []byte(`{"enabled":false,"name":"Renamed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.Name != "Renamed" {
		t.Fatalf("patch not applied: %+v", updated)
	}
	for _, input := range [][]byte{
		[]byte(`{"name":"Only name"}`),
		[]byte(`{"name":null,"source_type":"news","source_role":"external","trust_level":"reviewed","adapter_type":"rss","endpoint":"https://news.example.org/feed","enabled":true,"polling_interval_seconds":900}`),
		[]byte(`{"name":"Example","source_type":"news","source_role":"external","trust_level":"reviewed","adapter_type":"rss","endpoint":"https://news.example.org/feed","enabled":true,"polling_interval_seconds":900,"password":"secret"}`),
	} {
		if _, err := ParseCreateJSON(input); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid create accepted: %s: %v", input, err)
		}
	}
	for _, patch := range [][]byte{[]byte(`{}`), []byte(`{"enabled":null}`), []byte(`{"unknown":1}`)} {
		if _, err := ApplyPatch(cfg, patch); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid patch accepted: %s: %v", patch, err)
		}
	}
}
