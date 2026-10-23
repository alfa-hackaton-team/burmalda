package feeds

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
)

type fakeResolver struct{ addresses []net.IPAddr }

func (f fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return f.addresses, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFetchConditionalHeadersAndRetryAfter(t *testing.T) {
	etag := `"old"`
	modified := "Wed, 21 Oct 2026 10:00:00 GMT"
	fetcher := &SafeFetcher{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("If-None-Match") != etag || req.Header.Get("If-Modified-Since") != modified {
			t.Fatalf("conditional headers missing: %+v", req.Header)
		}
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"120"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}, maxBytes: MaxFeedBytes}
	response, err := fetcher.Fetch(context.Background(), polling.Lease{
		Endpoint: "https://example.com/feed.xml", HTTPETag: &etag, LastModified: &modified,
	})
	if err != nil || response.Status != http.StatusTooManyRequests || response.RetryAfter.Seconds() != 120 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestSafeDialRejectsPrivateResolution(t *testing.T) {
	dial := secureDial(fakeResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}})
	_, err := dial(context.Background(), "tcp", "news.example:443")
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("private DNS result: %v", err)
	}
	for _, raw := range []string{"http://127.0.0.1/feed", "http://localhost/feed", "file:///etc/passwd", "https://user:pass@example.com/feed"} {
		u, err := url.Parse(raw)
		if err == nil {
			err = validateURL(u)
		}
		if err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
}
