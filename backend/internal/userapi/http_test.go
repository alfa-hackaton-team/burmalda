package userapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpsecurity"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
	"gopkg.in/yaml.v3"
)

const (
	testUserID    = "11111111-1111-4111-8111-111111111111"
	testSessionID = "77777777-7777-4777-8777-777777777777"
	testContextID = "22222222-2222-4222-8222-222222222222"
	secondContext = "33333333-3333-4333-8333-333333333333"
)

var testCursorSigningKey = sha256.Sum256([]byte("server-only cursor signing key"))
var testAllowedOrigins = httpsecurity.OriginAllowlist{"https://app.example": {}}

type fakeContextStore struct {
	items   []AvailableContext
	userID  string
	afterID string
	limit   int
	called  int
}

func (s *fakeContextStore) ListContexts(_ context.Context, userID, afterID string, limit int) ([]AvailableContext, error) {
	s.called++
	s.userID, s.afterID, s.limit = userID, afterID, limit
	return s.items, nil
}

type fakeSessionStore struct {
	principal authn.Principal
}

func (s fakeSessionStore) Authenticate(context.Context, [sha256.Size]byte) (authn.Principal, error) {
	return s.principal, nil
}

func authenticatedHandler(store Store) http.Handler {
	handler := authn.Middleware(fakeSessionStore{principal: authn.Principal{
		SessionID:   testSessionID,
		UserID:      testUserID,
		DisplayName: stringPtr("Demo User"),
	}}, Handler(store, testCursorSigningKey))
	return httpsecurity.Protect(handler, httpsecurity.Options{
		RequestTimeout: time.Second,
		MaxBodyBytes:   1024,
		AllowedOrigins: testAllowedOrigins,
	})
}

func authenticatedRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.AddCookie(&http.Cookie{
		Name:  "session_id",
		Value: base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size)),
	})
	return req
}

func TestGetMeUsesAuthenticatedPrincipal(t *testing.T) {
	handler := authenticatedHandler(&fakeContextStore{})
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/me", ""))

	if res.Code != http.StatusOK || res.Body.String() != "{\"user_id\":\"11111111-1111-4111-8111-111111111111\",\"display_name\":\"Demo User\"}\n" {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestListContextsPaginatesAndBindsCursorToUser(t *testing.T) {
	store := &fakeContextStore{items: []AvailableContext{
		{
			Context:     BusinessContext{ContextID: testContextID, Kind: "COMPANY", CompanyID: "44444444-4444-4444-8444-444444444444"},
			DisplayName: "Company",
			RoleID:      "cfo",
		},
		{Context: BusinessContext{ContextID: secondContext, Kind: "GROUP", GroupID: "55555555-5555-4555-8555-555555555555"}},
	}}
	handler := authenticatedHandler(store)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/contexts?limit=1", ""))

	if res.Code != http.StatusOK || store.userID != testUserID || store.limit != 2 {
		t.Fatalf("status=%d user=%q limit=%d body=%s", res.Code, store.userID, store.limit, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"next_cursor":"`) || strings.Contains(res.Body.String(), secondContext) {
		t.Fatalf("unexpected page: %s", res.Body.String())
	}

	cursor, err := encodeCursor(contextCursor{
		UserID: testUserID, SessionID: testSessionID, AfterID: testContextID,
	}, testCursorSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	store.items = nil
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/contexts?cursor="+cursor, ""))
	if res.Code != http.StatusOK || store.afterID != testContextID {
		t.Fatalf("status=%d after=%q body=%s", res.Code, store.afterID, res.Body.String())
	}

	foreignCursor, err := encodeCursor(contextCursor{
		UserID:    "66666666-6666-4666-8666-666666666666",
		SessionID: testSessionID,
		AfterID:   testContextID,
	}, testCursorSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/contexts?cursor="+foreignCursor, ""))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("foreign cursor status=%d body=%s", res.Code, res.Body.String())
	}

	otherSessionCursor, err := encodeCursor(contextCursor{
		UserID: testUserID, SessionID: "88888888-8888-4888-8888-888888888888", AfterID: testContextID,
	}, testCursorSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/contexts?cursor="+otherSessionCursor, ""))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("other-session cursor status=%d body=%s", res.Code, res.Body.String())
	}

	clientKnownCSRFHash := sha256.Sum256([]byte("client-known CSRF token"))
	forgedCursor, err := encodeCursor(contextCursor{
		UserID: testUserID, SessionID: testSessionID, AfterID: testContextID,
	}, clientKnownCSRFHash)
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/contexts?cursor="+forgedCursor, ""))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("client-signed cursor status=%d body=%s", res.Code, res.Body.String())
	}

	tampered, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	tampered[len(tampered)-1] ^= 1
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/contexts?cursor="+base64.RawURLEncoding.EncodeToString(tampered), ""))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("tampered cursor status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestListContextsRejectsInvalidInputBeforeStore(t *testing.T) {
	tests := []string{
		"/v0.1/contexts?limit=0",
		"/v0.1/contexts?limit=101",
		"/v0.1/contexts?limit=1&limit=2",
		"/v0.1/contexts?cursor=bad",
		"/v0.1/contexts?unknown=x",
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			store := &fakeContextStore{}
			res := httptest.NewRecorder()
			authenticatedHandler(store).ServeHTTP(res, authenticatedRequest(http.MethodGet, target, ""))
			if res.Code != http.StatusBadRequest || store.called != 0 {
				t.Fatalf("status=%d calls=%d body=%s", res.Code, store.called, res.Body.String())
			}
		})
	}
}

func TestGetRejectsBody(t *testing.T) {
	res := httptest.NewRecorder()
	req := authenticatedRequest(http.MethodGet, "/v0.1/me", "")
	req.Body = panicReadCloser{}
	req.ContentLength = 1
	authenticatedHandler(&fakeContextStore{}).ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestHandlerReturnsJSONForUnknownRouteAndMethod(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantCode   string
		wantAllow  string
	}{
		{name: "unknown route", method: http.MethodGet, target: "/v0.1/missing", wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "wrong method", method: http.MethodPost, target: "/v0.1/me", wantStatus: http.StatusMethodNotAllowed, wantCode: "VALIDATION_ERROR", wantAllow: "GET, HEAD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := httptest.NewRecorder()
			Handler(&fakeContextStore{}, testCursorSigningKey).ServeHTTP(res, httptest.NewRequest(tc.method, tc.target, nil))

			var response httpx.Error
			if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode error response: %v body=%q", err, res.Body.String())
			}
			if res.Code != tc.wantStatus || response.Code != tc.wantCode || res.Header().Get("Content-Type") != "application/json" || res.Header().Get("Allow") != tc.wantAllow {
				t.Fatalf("status=%d error=%+v headers=%v", res.Code, response, res.Header())
			}
		})
	}
}

func TestSecurityChainOrdersAuthenticationProofAndRouting(t *testing.T) {
	csrfToken := bytes.Repeat([]byte{7}, sha256.Size)
	encodedCSRF := base64.RawURLEncoding.EncodeToString(csrfToken)
	principal := authn.Principal{
		SessionID:     testSessionID,
		UserID:        testUserID,
		CSRFTokenHash: sha256.Sum256(csrfToken),
	}
	handler := httpsecurity.Protect(
		authn.Middleware(fakeSessionStore{principal: principal},
			httpsecurity.RequireMutationProof(Handler(&fakeContextStore{}, testCursorSigningKey), testAllowedOrigins)),
		httpsecurity.Options{RequestTimeout: time.Second, MaxBodyBytes: 1024, AllowedOrigins: testAllowedOrigins},
	)

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v0.1/missing", nil))
	assertHTTPError(t, res, http.StatusUnauthorized, "UNAUTHENTICATED")

	res = httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodPost, "/v0.1/missing", ""))
	assertHTTPError(t, res, http.StatusForbidden, "FORBIDDEN")

	req := authenticatedRequest(http.MethodPost, "/v0.1/missing", "")
	req.Header.Set("Origin", "https://app.example")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: encodedCSRF})
	req.Header.Set("X-CSRF-Token", encodedCSRF)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	assertHTTPError(t, res, http.StatusNotFound, "NOT_FOUND")
	if res.Header().Get("Access-Control-Allow-Origin") != "https://app.example" || res.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("cors headers=%v", res.Header())
	}
}

func TestUserResponsesAreNotCacheable(t *testing.T) {
	res := httptest.NewRecorder()
	authenticatedHandler(&fakeContextStore{}).ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/me", ""))
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("authenticated cache control=%q", got)
	}

	unauthenticated := httpsecurity.Protect(
		authn.Middleware(&fakeSessionStore{}, Handler(&fakeContextStore{}, testCursorSigningKey)),
		httpsecurity.Options{RequestTimeout: time.Second, MaxBodyBytes: 1024, AllowedOrigins: testAllowedOrigins},
	)
	res = httptest.NewRecorder()
	unauthenticated.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v0.1/me", nil))
	if res.Code != http.StatusUnauthorized || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache control=%q", res.Code, res.Header().Get("Cache-Control"))
	}
}

func TestRequestTimeoutCancelsStoreQuery(t *testing.T) {
	handler := authn.Middleware(fakeSessionStore{principal: authn.Principal{
		SessionID: testSessionID,
		UserID:    testUserID,
	}}, Handler(timeoutStore{}, testCursorSigningKey))
	handler = httpsecurity.Protect(handler, httpsecurity.Options{
		RequestTimeout: time.Millisecond,
		MaxBodyBytes:   1024,
		AllowedOrigins: testAllowedOrigins,
	})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/contexts", ""))
	if res.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var response httpx.Error
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Code != httpx.ErrorCodeTimeout || !response.Retryable {
		t.Fatalf("error=%+v", response)
	}
	assertOpenAPIErrorCode(t, response.Code)
}

func TestProtectHTTPLimitsRequestBody(t *testing.T) {
	handler := httpsecurity.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Value string `json:"value"`
		}
		if httpx.DecodeJSONObject(w, r, &payload) {
			t.Fatal("oversized request decoded")
		}
	}), httpsecurity.Options{RequestTimeout: time.Second, MaxBodyBytes: 2, AllowedOrigins: testAllowedOrigins})
	req := httptest.NewRequest(http.MethodPost, "/v0.1/test", strings.NewReader(`{"value":"too large"}`))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	assertHTTPError(t, res, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE")
}

func TestParseCursorSigningKey(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(testCursorSigningKey[:])
	got, err := ParseCursorSigningKey(encoded)
	if err != nil || got != testCursorSigningKey {
		t.Fatalf("key=%x error=%v", got, err)
	}
	if _, err := ParseCursorSigningKey("public-or-short"); err == nil {
		t.Fatal("short signing key accepted")
	}
}

type panicReadCloser struct{}

func (panicReadCloser) Read([]byte) (int, error) { panic("GET body must not be read") }
func (panicReadCloser) Close() error             { return nil }

type timeoutStore struct{}

func (timeoutStore) ListContexts(ctx context.Context, _, _ string, _ int) ([]AvailableContext, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func assertOpenAPIErrorCode(t *testing.T, code string) {
	t.Helper()
	data, err := os.ReadFile("../../../docs/api/schemas.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas struct {
				Error struct {
					Properties struct {
						Code struct {
							Enum []string `yaml:"enum"`
						} `yaml:"code"`
					} `yaml:"properties"`
				} `yaml:"Error"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, allowed := range document.Components.Schemas.Error.Properties.Code.Enum {
		if code == allowed {
			return
		}
	}
	t.Fatalf("error code %q is absent from docs/api/schemas.yaml", code)
}

func assertHTTPError(t *testing.T, res *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var response httpx.Error
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v body=%q", err, res.Body.String())
	}
	if res.Code != status || response.Code != code {
		t.Fatalf("status=%d error=%+v", res.Code, response)
	}
}

func stringPtr(value string) *string { return &value }
