package feeds

import (
	"bytes"
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

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
)

const MaxFeedBytes int64 = 4 << 20
const MaxArticleBytes int64 = 4 << 20

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
	settings config.Runtime
	maxBytes int64
}

func (f *SafeFetcher) options() config.Runtime {
	if f.settings.Feeds.MaxFeedBytes == 0 {
		return config.DefaultRuntime()
	}
	return f.settings
}

func NewSafeFetcher(resolver Resolver) *SafeFetcher {
	return NewSafeFetcherWithSettings(resolver, config.DefaultRuntime())
}

func NewSafeFetcherWithSettings(resolver Resolver, settings config.Runtime) *SafeFetcher {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	transport := &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          settings.Feeds.MaxIdleConns,
		MaxConnsPerHost:       settings.Feeds.MaxConnsPerHost,
		IdleConnTimeout:       settings.Feeds.IdleConnTimeout.Std(),
		TLSHandshakeTimeout:   settings.Feeds.TLSHandshakeTimeout.Std(),
		ResponseHeaderTimeout: settings.Feeds.ResponseHeaderTimeout.Std(),
		DialContext:           secureDialWithSettings(resolver, settings),
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   settings.Feeds.RequestTimeout.Std(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= settings.Feeds.MaxRedirects {
				return fmt.Errorf("too many source redirects")
			}
			return validateURL(req.URL)
		},
	}
	return &SafeFetcher{client: client, settings: settings, maxBytes: settings.Feeds.MaxFeedBytes}
}

func (f *SafeFetcher) CloseIdleConnections() { f.client.CloseIdleConnections() }

func (f *SafeFetcher) Fetch(ctx context.Context, lease polling.Lease) (Response, error) {
	u, err := url.Parse(lease.Endpoint)
	if err != nil || validateURL(u) != nil {
		return Response{}, fmt.Errorf("invalid source endpoint")
	}
	maxBytes := f.maxBytes
	if maxBytes == 0 {
		maxBytes = f.options().Feeds.MaxFeedBytes
	}
	return f.fetch(ctx, u, "application/rss+xml, application/atom+xml, application/xml, text/xml", maxBytes, lease.HTTPETag, lease.LastModified, "")
}

func (f *SafeFetcher) FetchArticle(ctx context.Context, feedURL, articleURL string) (Response, error) {
	feed, feedErr := url.Parse(feedURL)
	u, err := url.Parse(articleURL)
	if feedErr != nil || err != nil || validateURL(feed) != nil || validateURL(u) != nil || !sameHost(feed, u) ||
		(feed.Scheme == "https" && u.Scheme != "https") {
		return Response{}, fmt.Errorf("article URL is outside the source host")
	}
	response, err := f.fetch(ctx, u, "text/html, application/xhtml+xml, application/pdf", f.options().Feeds.MaxArticleBytes, nil, nil, feed.Host)
	if err != nil {
		return response, err
	}
	if response.Status != http.StatusOK {
		return response, fmt.Errorf("article returned HTTP %d", response.Status)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(response.ContentType, ";", 2)[0]))
	if mediaType == "application/octet-stream" && bytes.HasPrefix(response.Body, []byte("%PDF-")) {
		response.ContentType = "application/pdf"
		mediaType = "application/pdf"
	}
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" && mediaType != "application/pdf" {
		return response, fmt.Errorf("unsupported article media type")
	}
	return response, nil
}

func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(a.Host, b.Host)
}

func (f *SafeFetcher) fetch(ctx context.Context, u *url.URL, accept string, maxBytes int64, etag, modified *string, allowedHost string) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Response{}, fmt.Errorf("build source request: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", f.options().Feeds.UserAgent)
	if etag != nil {
		req.Header.Set("If-None-Match", *etag)
	}
	if modified != nil {
		req.Header.Set("If-Modified-Since", *modified)
	}
	client := *f.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= f.options().Feeds.MaxRedirects || validateURL(req.URL) != nil ||
			(allowedHost != "" && (!strings.EqualFold(req.URL.Host, allowedHost) ||
				(u.Scheme == "https" && req.URL.Scheme != "https"))) {
			return fmt.Errorf("unsafe source redirect")
		}
		return nil
	}
	resp, err := client.Do(req)
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
		result.RetryAfter = parseRetryAfterWithMax(resp.Header.Get("Retry-After"), f.options().Feeds.RetryAfterMax.Std())
	}
	if resp.StatusCode != http.StatusOK {
		return result, nil
	}
	if resp.ContentLength > maxBytes {
		return result, fmt.Errorf("source response exceeds %d bytes", maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return result, fmt.Errorf("source body read failed")
	}
	if int64(len(data)) > maxBytes {
		return result, fmt.Errorf("source response exceeds %d bytes", maxBytes)
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
	return secureDialWithSettings(resolver, config.DefaultRuntime())
}

func secureDialWithSettings(resolver Resolver, settings config.Runtime) func(context.Context, string, string) (net.Conn, error) {
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
			lookupCtx, cancel := context.WithTimeout(ctx, settings.Feeds.DNSLookupTimeout.Std())
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
		dialer := &net.Dialer{Timeout: settings.Feeds.DialTimeout.Std()}
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
	return parseRetryAfterWithMax(raw, 24*time.Hour)
}

func parseRetryAfterWithMax(raw string, maxDelay time.Duration) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(min(seconds, int64(maxDelay/time.Second))) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		return min(max(time.Until(when), 0), maxDelay)
	}
	return 0
}
