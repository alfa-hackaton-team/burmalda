package feeds

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
)

const MaxFeedBytes int64 = 4 << 20

type Response struct {
	Status       int
	Body         []byte
	ETag         string
	LastModified string
	ContentType  string
	RetryAfter   time.Duration
}

type Fetcher interface {
	Fetch(context.Context, polling.Lease) (Response, error)
}

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type SafeFetcher struct {
	client   *http.Client
	maxBytes int64
}

func NewSafeFetcher(resolver Resolver) *SafeFetcher {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	transport := &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          10,
		MaxConnsPerHost:       2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DialContext:           secureDial(resolver),
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many source redirects")
			}
			return validateURL(req.URL)
		},
	}
	return &SafeFetcher{client: client, maxBytes: MaxFeedBytes}
}

func (f *SafeFetcher) Fetch(ctx context.Context, lease polling.Lease) (Response, error) {
	u, err := url.Parse(lease.Endpoint)
	if err != nil || validateURL(u) != nil {
		return Response{}, fmt.Errorf("invalid source endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Response{}, fmt.Errorf("build source request: %w", err)
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml")
	req.Header.Set("User-Agent", "Burmalda/0.1 (+source-polling)")
	if lease.HTTPETag != nil {
		req.Header.Set("If-None-Match", *lease.HTTPETag)
	}
	if lease.LastModified != nil {
		req.Header.Set("If-Modified-Since", *lease.LastModified)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("source request failed")
	}
	defer resp.Body.Close()
	result := Response{
		Status:       resp.StatusCode,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		ContentType:  resp.Header.Get("Content-Type"),
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		result.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode != http.StatusOK {
		return result, nil
	}
	if resp.ContentLength > f.maxBytes {
		return result, fmt.Errorf("source feed exceeds %d bytes", f.maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return result, fmt.Errorf("source body read failed")
	}
	if int64(len(data)) > f.maxBytes {
		return result, fmt.Errorf("source feed exceeds %d bytes", f.maxBytes)
	}
	result.Body = data
	return result, nil
}

func validateURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return fmt.Errorf("source URL must be http(s) without credentials or fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.Contains(host, "%") {
		return fmt.Errorf("source host is not allowed")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !sources.PublicIP(ip) {
		return fmt.Errorf("source IP is not public")
	}
	return nil
}

func secureDial(resolver Resolver) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid source address: %w", err)
		}
		if err := validateURL(&url.URL{Scheme: "https", Host: address}); err != nil {
			return nil, err
		}
		if port == "" {
			return nil, fmt.Errorf("source port is empty")
		}
		var addresses []net.IPAddr
		if ip, err := netip.ParseAddr(host); err == nil {
			addresses = []net.IPAddr{{IP: net.IP(ip.AsSlice())}}
		} else {
			lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			addresses, err = resolver.LookupIPAddr(lookupCtx, host)
			if err != nil || len(addresses) == 0 {
				return nil, fmt.Errorf("source host does not resolve")
			}
		}
		for _, address := range addresses {
			ip, ok := netip.AddrFromSlice(address.IP)
			if !ok || !sources.PublicIP(ip.Unmap()) {
				return nil, fmt.Errorf("source resolves to a non-public IP")
			}
		}
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		var lastErr error
		for _, address := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("connect to source: %w", lastErr)
	}
}

func parseRetryAfter(raw string) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(min(seconds, int64((24*time.Hour)/time.Second))) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		return min(max(time.Until(when), 0), 24*time.Hour)
	}
	return 0
}
