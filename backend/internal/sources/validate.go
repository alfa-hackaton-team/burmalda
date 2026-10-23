package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type Validator struct {
	Resolver Resolver
}

func (v Validator) Validate(ctx context.Context, cfg *Config) error {
	return v.validate(ctx, cfg, true)
}

func (v Validator) validate(ctx context.Context, cfg *Config, resolveHost bool) error {
	for _, field := range []struct{ name, value string }{
		{"name", cfg.Name}, {"source_type", cfg.SourceType},
		{"trust_level", cfg.TrustLevel}, {"endpoint", cfg.Endpoint},
	} {
		if strings.TrimSpace(field.value) == "" || len(field.value) > 512 {
			return fmt.Errorf("%w: %s must be nonempty and at most 512 bytes", ErrInvalid, field.name)
		}
	}
	if cfg.SourceRole != "external" && cfg.SourceRole != "bank" && cfg.SourceRole != "expert" {
		return fmt.Errorf("%w: unsupported source_role", ErrInvalid)
	}
	if cfg.AdapterType != "rss" && cfg.AdapterType != "atom" {
		return fmt.Errorf("%w: unsupported adapter_type", ErrInvalid)
	}
	if cfg.PollingIntervalSeconds < 60 || cfg.PollingIntervalSeconds > 86400 {
		return fmt.Errorf("%w: polling_interval_seconds must be between 60 and 86400", ErrInvalid)
	}
	if cfg.CredentialRef != nil {
		return fmt.Errorf("%w: credential_ref is not supported for rss/atom yet", ErrInvalid)
	}
	if len(cfg.AdapterConfig) == 0 {
		cfg.AdapterConfig = json.RawMessage(`{}`)
	}
	var adapterConfig map[string]json.RawMessage
	if err := json.Unmarshal(cfg.AdapterConfig, &adapterConfig); err != nil || adapterConfig == nil || len(adapterConfig) != 0 {
		return fmt.Errorf("%w: rss/atom adapter_config must be an empty object", ErrInvalid)
	}
	if err := v.validateEndpoint(ctx, cfg.Endpoint, resolveHost); err != nil {
		return err
	}
	return nil
}

func (v Validator) validateEndpoint(ctx context.Context, raw string, resolveHost bool) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("%w: endpoint must be an http(s) URL without credentials or fragment", ErrInvalid)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return fmt.Errorf("%w: endpoint host is not allowed", ErrInvalid)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !PublicIP(ip) {
			return fmt.Errorf("%w: endpoint IP is not public", ErrInvalid)
		}
		return nil
	}
	if !resolveHost {
		return nil
	}
	resolver := v.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addresses, err := resolver.LookupIPAddr(lookupCtx, host)
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("%w: endpoint host does not resolve", ErrInvalid)
	}
	for _, address := range addresses {
		ip, ok := netip.AddrFromSlice(address.IP)
		if !ok || !PublicIP(ip.Unmap()) {
			return fmt.Errorf("%w: endpoint resolves to a non-public IP", ErrInvalid)
		}
	}
	return nil
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// PublicIP applies the same address policy at configuration time and at dial time.
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
