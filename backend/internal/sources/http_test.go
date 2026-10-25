package sources

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var testCursorKey = sha256.Sum256([]byte("admin-source-test-key"))

func TestIfMatchRequiresExactStrongETag(t *testing.T) {
	for _, value := range []string{`"01"`, `"+1"`, `" 1"`, `W/"1"`, `"1", "2"`, `*`, `1`, `"0"`} {
		if _, err := parseIfMatch(value); err == nil {
			t.Fatalf("invalid strong ETag accepted: %s", value)
		}
	}
	if version, err := parseIfMatch(`"1"`); err != nil || version != 1 {
		t.Fatalf("version=%d err=%v", version, err)
	}
}

type fakeRegistry struct {
	called int
	source Source
}

func (f *fakeRegistry) Create(_ context.Context, cfg Config) (Source, error) {
	f.called++
	f.source.Config = cfg
	return f.source, nil
}
func (f *fakeRegistry) Get(context.Context, string) (Source, error) {
	f.called++
	return f.source, nil
}
func (f *fakeRegistry) Update(_ context.Context, _ string, _ int64, patch []byte) (Source, error) {
	f.called++
	var err error
	f.source.Config, err = ApplyPatch(f.source.Config, patch)
	return f.source, err
}
func (f *fakeRegistry) List(context.Context, int, string) (Page, error) {
	f.called++
	return Page{Items: []Source{f.source}}, nil
}

func TestAdminHandlerFailsClosed(t *testing.T) {
	if _, err := AdminHandler(&fakeRegistry{}, nil, testCursorKey); err == nil {
		t.Fatal("handler accepted missing authorizer")
	}
	registry := &fakeRegistry{}
	handler, err := AdminHandler(registry, func(*http.Request, Permission) int { return 401 }, testCursorKey)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v0.1/admin/sources", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 401 || registry.called != 0 {
		t.Fatalf("status=%d repository calls=%d", res.Code, registry.called)
	}
}

func TestAdminHandlerCreateAndPatchPermissions(t *testing.T) {
	registry := &fakeRegistry{source: Source{SourceID: "source-id", ConfigVersion: "1", Health: "UNKNOWN"}}
	handler, err := AdminHandler(registry, func(_ *http.Request, permission Permission) int {
		if permission == SetTrust {
			return 403
		}
		return 0
	}, testCursorKey)
	if err != nil {
		t.Fatal(err)
	}
	full := `{"name":"Example","source_type":"news","source_role":"external","trust_level":"reviewed","adapter_type":"rss","endpoint":"https://example.com/feed","enabled":true,"polling_interval_seconds":900}`
	create := httptest.NewRequest("POST", "/v0.1/admin/sources", strings.NewReader(full))
	create.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, create)
	if res.Code != 403 || registry.called != 0 {
		t.Fatalf("create: status=%d calls=%d", res.Code, registry.called)
	}

	patch := httptest.NewRequest("PATCH", "/v0.1/admin/sources/11111111-1111-4111-8111-111111111111", strings.NewReader(`{"trust_level":"other"}`))
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("If-Match", `"1"`)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, patch)
	if res.Code != 403 || registry.called != 0 {
		t.Fatalf("trust patch: status=%d calls=%d", res.Code, registry.called)
	}

	patch = httptest.NewRequest("PATCH", "/v0.1/admin/sources/11111111-1111-4111-8111-111111111111", strings.NewReader(`{"enabled":false}`))
	patch.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, patch)
	if res.Code != 428 || registry.called != 0 {
		t.Fatalf("missing If-Match: status=%d calls=%d", res.Code, registry.called)
	}

	patch = httptest.NewRequest("PATCH", "/v0.1/admin/sources/11111111-1111-4111-8111-111111111111", strings.NewReader(`{"enabled":false}`))
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("If-Match", `"1"`)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, patch)
	if res.Code != 200 || registry.called != 1 || registry.source.Config.Enabled {
		t.Fatalf("ordinary patch: status=%d calls=%d", res.Code, registry.called)
	}
}

func TestAdminHandlerCreate(t *testing.T) {
	registry := &fakeRegistry{source: Source{SourceID: "source-id", ConfigVersion: "1", Health: "UNKNOWN"}}
	handler, err := AdminHandler(registry, func(*http.Request, Permission) int { return 0 }, testCursorKey)
	if err != nil {
		t.Fatal(err)
	}
	input := `{"name":"Example","source_type":"news","source_role":"external","trust_level":"reviewed","adapter_type":"rss","endpoint":"https://example.com/feed","enabled":true,"polling_interval_seconds":900}`
	req := httptest.NewRequest("POST", "/v0.1/admin/sources", strings.NewReader(input))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 201 || res.Header().Get("ETag") != `"1"` || registry.called != 1 {
		t.Fatalf("status=%d etag=%q calls=%d", res.Code, res.Header().Get("ETag"), registry.called)
	}
}
