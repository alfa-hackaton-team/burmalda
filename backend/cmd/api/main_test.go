package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpsecurity"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
)

const sourceID = "11111111-1111-4111-8111-111111111111"
const sourcePath = "/v0.1/admin/sources/" + sourceID
const sourceInput = `{"name":"Example","source_type":"news","source_role":"external","trust_level":"reviewed","adapter_type":"rss","endpoint":"https://example.com/feed","enabled":true,"polling_interval_seconds":900}`

type sessionStub struct{ principal authn.Principal }

func (s sessionStub) Authenticate(context.Context, [32]byte) (authn.Principal, error) {
	return s.principal, nil
}

type registryStub struct {
	calls   int
	next    *string
	cursor  string
	failure error
}

func (s *registryStub) Create(_ context.Context, cfg sources.Config) (sources.Source, error) {
	s.calls++
	return sources.Source{SourceID: sourceID, ConfigVersion: "1", Config: cfg}, s.failure
}
func (s *registryStub) Get(context.Context, string) (sources.Source, error) {
	s.calls++
	diagnostic := "private-token-in-upstream-error"
	return sources.Source{SourceID: sourceID, ConfigVersion: "1", LastError: &diagnostic}, s.failure
}
func (s *registryStub) Update(context.Context, string, int64, []byte) (sources.Source, error) {
	s.calls++
	return sources.Source{SourceID: sourceID, ConfigVersion: "2"}, s.failure
}
func (s *registryStub) List(_ context.Context, _ int, cursor string) (sources.Page, error) {
	s.calls++
	s.cursor = cursor
	return sources.Page{Items: []sources.Source{}, NextCursor: s.next}, s.failure
}

func request(method, path, body string, session, proof bool) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if session {
		r.AddCookie(&http.Cookie{Name: "session_id", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	}
	if proof {
		token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		r.AddCookie(&http.Cookie{Name: "csrf_token", Value: token})
		r.Header.Set("X-CSRF-Token", token)
		r.Header.Set("Origin", "https://app.example")
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("If-Match", `"1"`)
	return r
}

func apiForTest(t *testing.T, registry *registryStub, permissions map[sources.Permission]bool, user, session string) http.Handler {
	t.Helper()
	principal := authn.Principal{UserID: user, SessionID: session, CSRFTokenHash: sha256.Sum256(make([]byte, 32))}
	handler, err := newAPIHandler(sessionStub{principal}, nil, registry, func(_ *http.Request, p sources.Permission) int {
		if permissions[p] {
			return 0
		}
		return 403
	}, sha256.Sum256([]byte("test server key")), httpsecurity.Options{RequestTimeout: time.Second, MaxBodyBytes: 1 << 20, AllowedOrigins: httpsecurity.OriginAllowlist{"https://app.example": {}}})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestSourceRoutesUseFullSecurityChain(t *testing.T) {
	all := map[sources.Permission]bool{sources.ReadSources: true, sources.ManageSources: true, sources.SetTrust: true}
	tests := []struct {
		name, method, path, body string
		session, proof           bool
		permissions              map[sources.Permission]bool
		status, calls            int
	}{
		{"no session", "GET", "/v0.1/admin/sources", "", false, false, all, 401, 0},
		{"no admin grant", "GET", "/v0.1/admin/sources", "", true, false, nil, 403, 0},
		{"read list", "GET", "/v0.1/admin/sources", "", true, false, all, 200, 1},
		{"read source", "GET", sourcePath, "", true, false, all, 200, 1},
		{"no csrf", "POST", "/v0.1/admin/sources", sourceInput, true, false, all, 403, 0},
		{"create", "POST", "/v0.1/admin/sources", sourceInput, true, true, all, 201, 1},
		{"reader cannot create", "POST", "/v0.1/admin/sources", sourceInput, true, true, map[sources.Permission]bool{sources.ReadSources: true}, 403, 0},
		{"manage cannot set trust on create", "POST", "/v0.1/admin/sources", sourceInput, true, true, map[sources.Permission]bool{sources.ManageSources: true}, 403, 0},
		{"ordinary patch", "PATCH", sourcePath, `{"enabled":false}`, true, true, map[sources.Permission]bool{sources.ManageSources: true}, 200, 1},
		{"trust patch denied", "PATCH", sourcePath, `{"trust_level":"trusted"}`, true, true, map[sources.Permission]bool{sources.ManageSources: true}, 403, 0},
		{"role patch denied", "PATCH", sourcePath, `{"source_role":"bank"}`, true, true, map[sources.Permission]bool{sources.ManageSources: true}, 403, 0},
		{"trust alone cannot patch", "PATCH", sourcePath, `{"trust_level":"trusted"}`, true, true, map[sources.Permission]bool{sources.SetTrust: true}, 403, 0},
		{"trust patch allowed", "PATCH", sourcePath, `{"trust_level":"trusted"}`, true, true, all, 200, 1},
		{"wrong method", "DELETE", sourcePath, "", true, true, all, 405, 0},
		{"unknown route", "GET", sourcePath + "/missing", "", true, false, all, 404, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			registry := &registryStub{}
			handler := apiForTest(t, registry, tc.permissions, "user", "session")
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, request(tc.method, tc.path, tc.body, tc.session, tc.proof))
			if res.Code != tc.status || registry.calls != tc.calls || res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status=%d calls=%d body=%s", res.Code, registry.calls, res.Body.String())
			}
			if strings.Contains(res.Body.String(), "private-token") {
				t.Fatal("private upstream diagnostic leaked")
			}
		})
	}
}

func TestSourceValidationRejectsBeforeRegistry(t *testing.T) {
	all := map[sources.Permission]bool{sources.ReadSources: true, sources.ManageSources: true, sources.SetTrust: true}
	tests := []struct {
		name, method, path, body string
		modify                   func(*http.Request)
		status                   int
	}{
		{"duplicate limit", "GET", "/v0.1/admin/sources?limit=1&limit=2", "", nil, 400},
		{"unknown query", "GET", "/v0.1/admin/sources?user_id=other", "", nil, 400},
		{"invalid ID", "GET", "/v0.1/admin/sources/not-uuid", "", nil, 400},
		{"get body", "GET", sourcePath, `{}`, nil, 400},
		{"missing If-Match", "PATCH", sourcePath, `{"enabled":false}`, func(r *http.Request) { r.Header.Del("If-Match") }, 428},
		{"duplicate If-Match", "PATCH", sourcePath, `{"enabled":false}`, func(r *http.Request) { r.Header.Add("If-Match", `"2"`) }, 400},
		{"noncanonical If-Match", "PATCH", sourcePath, `{"enabled":false}`, func(r *http.Request) { r.Header.Set("If-Match", `"01"`) }, 400},
		{"case alias", "PATCH", sourcePath, `{"TRUST_LEVEL":"trusted"}`, nil, 400},
		{"duplicate JSON", "PATCH", sourcePath, `{"enabled":true,"enabled":false}`, nil, 400},
		{"trailing JSON", "PATCH", sourcePath, `{"enabled":false} {}`, nil, 400},
		{"null required", "POST", "/v0.1/admin/sources", strings.Replace(sourceInput, `"enabled":true`, `"enabled":null`, 1), nil, 400},
		{"oversized stream", "PATCH", sourcePath, `{"name":"` + strings.Repeat("a", 65536) + `"}`, func(r *http.Request) { r.ContentLength = -1 }, 413},
		{"wrong content type", "PATCH", sourcePath, `{"enabled":false}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			registry := &registryStub{}
			handler := apiForTest(t, registry, all, "user", "session")
			req := request(tc.method, tc.path, tc.body, true, true)
			if tc.modify != nil {
				tc.modify(req)
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != tc.status || registry.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", res.Code, registry.calls, res.Body.String())
			}
		})
	}
}

func TestSourceCursorCannotBeReusedAcrossUsersSessionsOrLimits(t *testing.T) {
	raw := "repository-cursor"
	registry := &registryStub{next: &raw}
	permissions := map[sources.Permission]bool{sources.ReadSources: true}
	handler := apiForTest(t, registry, permissions, "user", "session")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, request("GET", "/v0.1/admin/sources?limit=1", "", true, false))
	var page sources.Page
	if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil || page.NextCursor == nil {
		t.Fatalf("page=%s err=%v", res.Body.String(), err)
	}
	registry.next = nil
	for _, tc := range []struct {
		user, session, limit, cursor string
		status                       int
	}{
		{"user", "session", "1", *page.NextCursor, 200},
		{"other", "session", "1", *page.NextCursor, 400},
		{"user", "other", "1", *page.NextCursor, 400},
		{"user", "session", "2", *page.NextCursor, 400},
		{"user", "session", "1", raw, 400},
	} {
		registry.calls = 0
		h := apiForTest(t, registry, permissions, tc.user, tc.session)
		res = httptest.NewRecorder()
		h.ServeHTTP(res, request("GET", "/v0.1/admin/sources?limit="+tc.limit+"&cursor="+tc.cursor, "", true, false))
		if res.Code != tc.status {
			t.Fatalf("status=%d want=%d body=%s", res.Code, tc.status, res.Body.String())
		}
		if tc.status == 400 && registry.calls != 0 {
			t.Fatal("invalid cursor reached registry")
		}
	}
	// Administrative rights are rechecked even for an otherwise valid cursor.
	permissions[sources.ReadSources] = false
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, request("GET", "/v0.1/admin/sources?limit=1&cursor="+*page.NextCursor, "", true, false))
	if res.Code != 403 {
		t.Fatalf("revoked access status=%d", res.Code)
	}
}

func TestSourceStoreErrorsAreSafeAndContractual(t *testing.T) {
	for _, tc := range []struct {
		failure error
		status  int
		code    string
	}{
		{sources.ErrConflict, 409, "CONFLICT"},
		{context.DeadlineExceeded, 504, "TIMEOUT"},
	} {
		registry := &registryStub{failure: tc.failure}
		h := apiForTest(t, registry, map[sources.Permission]bool{sources.ManageSources: true}, "user", "session")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, request("PATCH", sourcePath, `{"enabled":false}`, true, true))
		var response httpx.Error
		if json.Unmarshal(res.Body.Bytes(), &response) != nil || res.Code != tc.status || response.Code != tc.code {
			t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
		}
	}
}
