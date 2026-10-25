package httpsecurity

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
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
)

const allowedOrigin = "https://app.example"

var allowedOrigins = OriginAllowlist{allowedOrigin: {}}

type sessionStore struct {
	principal authn.Principal
}

func (s sessionStore) Authenticate(context.Context, [sha256.Size]byte) (authn.Principal, error) {
	return s.principal, nil
}

func TestParseOriginAllowlist(t *testing.T) {
	got, err := ParseOriginAllowlist("https://app.example, http://127.0.0.1:5173")
	if err != nil || len(got) != 2 {
		t.Fatalf("allowlist=%v error=%v", got, err)
	}
	for _, invalid := range []string{
		"", "*", "null", "ftp://app.example", "https://user@app.example",
		"https://app.example/", "https://app.example?q=1", "https://app.example#fragment",
		"https://app.example,https://app.example",
	} {
		t.Run(invalid, func(t *testing.T) {
			if _, err := ParseOriginAllowlist(invalid); err == nil {
				t.Fatalf("invalid allowlist %q accepted", invalid)
			}
		})
	}
}

func TestProtectHandlesCredentialedPreflight(t *testing.T) {
	handler := Protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight reached application handler")
	}), Options{RequestTimeout: time.Second, MaxBodyBytes: 1024, AllowedOrigins: allowedOrigins})
	req := httptest.NewRequest(http.MethodOptions, "/v0.1/insights", nil)
	req.Header.Set("Origin", allowedOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-csrf-token, idempotency-key")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent || res.Header().Get("Access-Control-Allow-Origin") != allowedOrigin ||
		res.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		!headerListContains(res.Header().Values("Access-Control-Expose-Headers"), "ETag") ||
		!headerListContains(res.Header().Values("Access-Control-Expose-Headers"), "Location") ||
		strings.Contains(res.Header().Get("Access-Control-Allow-Origin"), "*") {
		t.Fatalf("status=%d headers=%v body=%s", res.Code, res.Header(), res.Body.String())
	}
	for _, value := range []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
		if !headerListContains(res.Header().Values("Vary"), value) {
			t.Fatalf("Vary=%v does not contain %q", res.Header().Values("Vary"), value)
		}
	}
}

func TestProtectRejectsInvalidPreflight(t *testing.T) {
	tests := []struct {
		name    string
		origin  string
		method  string
		headers string
	}{
		{name: "foreign origin", origin: "https://app.example.evil", method: http.MethodPost},
		{name: "unsupported method", origin: allowedOrigin, method: http.MethodDelete},
		{name: "unsupported header", origin: allowedOrigin, method: http.MethodPost, headers: "Authorization"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := Protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("invalid preflight reached application handler")
			}), Options{RequestTimeout: time.Second, MaxBodyBytes: 1024, AllowedOrigins: allowedOrigins})
			req := httptest.NewRequest(http.MethodOptions, "/v0.1/insights", nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Access-Control-Request-Method", tc.method)
			if tc.headers != "" {
				req.Header.Set("Access-Control-Request-Headers", tc.headers)
			}
			res := httptest.NewRecorder()

			handler.ServeHTTP(res, req)

			assertError(t, res, http.StatusForbidden, "FORBIDDEN")
			if res.Header().Get("Access-Control-Allow-Origin") == "*" {
				t.Fatal("wildcard origin returned")
			}
		})
	}
}

func TestMutationProofIsBoundToSession(t *testing.T) {
	token := make([]byte, sha256.Size)
	for index := range token {
		token[index] = byte(index + 1)
	}
	encodedToken := base64.RawURLEncoding.EncodeToString(token)
	principal := authn.Principal{
		SessionID:     "77777777-7777-4777-8777-777777777777",
		UserID:        "11111111-1111-4111-8111-111111111111",
		CSRFTokenHash: sha256.Sum256(token),
	}

	tests := []struct {
		name         string
		configure    func(*http.Request)
		wantStatus   int
		wantNextCall bool
	}{
		{name: "valid", configure: func(r *http.Request) { addProof(r, allowedOrigin, encodedToken) }, wantStatus: http.StatusNoContent, wantNextCall: true},
		{name: "missing origin", configure: func(r *http.Request) { addCSRF(r, encodedToken) }, wantStatus: http.StatusForbidden},
		{name: "foreign origin", configure: func(r *http.Request) { addProof(r, "https://app.example.evil", encodedToken) }, wantStatus: http.StatusForbidden},
		{name: "null origin", configure: func(r *http.Request) { addProof(r, "null", encodedToken) }, wantStatus: http.StatusForbidden},
		{name: "missing csrf", configure: func(r *http.Request) { r.Header.Set("Origin", allowedOrigin) }, wantStatus: http.StatusForbidden},
		{name: "malformed csrf", configure: func(r *http.Request) { addProof(r, allowedOrigin, "not-base64") }, wantStatus: http.StatusForbidden},
		{name: "cookie header mismatch", configure: func(r *http.Request) {
			r.Header.Set("Origin", allowedOrigin)
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: encodedToken})
			r.Header.Set(csrfHeaderName, base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size)))
		}, wantStatus: http.StatusForbidden},
		{name: "other session token", configure: func(r *http.Request) {
			other := base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))
			addProof(r, allowedOrigin, other)
		}, wantStatus: http.StatusForbidden},
		{name: "duplicate csrf cookie", configure: func(r *http.Request) {
			addProof(r, allowedOrigin, encodedToken)
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: encodedToken})
		}, wantStatus: http.StatusForbidden},
		{name: "duplicate csrf header", configure: func(r *http.Request) {
			addProof(r, allowedOrigin, encodedToken)
			r.Header.Add(csrfHeaderName, encodedToken)
		}, wantStatus: http.StatusForbidden},
		{name: "duplicate origin", configure: func(r *http.Request) {
			addProof(r, allowedOrigin, encodedToken)
			r.Header.Add("Origin", allowedOrigin)
		}, wantStatus: http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			handler := authn.Middleware(sessionStore{principal: principal}, RequireMutationProof(next, allowedOrigins))
			req := authenticatedRequest(http.MethodPost)
			tc.configure(req)
			res := httptest.NewRecorder()

			handler.ServeHTTP(res, req)

			if res.Code != tc.wantStatus || called != tc.wantNextCall {
				t.Fatalf("status=%d called=%v body=%s", res.Code, called, res.Body.String())
			}
		})
	}
}

func TestAuthenticationRunsBeforeMutationProof(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached handler")
	})
	handler := authn.Middleware(sessionStore{}, RequireMutationProof(next, allowedOrigins))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v0.1/insights", nil))

	assertError(t, res, http.StatusUnauthorized, "UNAUTHENTICATED")
}

func TestSafeRequestDoesNotRequireMutationProof(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	principal := authn.Principal{SessionID: "session", UserID: "user"}
	handler := authn.Middleware(sessionStore{principal: principal}, RequireMutationProof(next, allowedOrigins))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet))

	if res.Code != http.StatusNoContent || !called {
		t.Fatalf("status=%d called=%v body=%s", res.Code, called, res.Body.String())
	}
}

func TestProtectRejectsDeclaredOversizedBody(t *testing.T) {
	handler := Protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("oversized request reached handler")
	}), Options{RequestTimeout: time.Second, MaxBodyBytes: 2, AllowedOrigins: allowedOrigins})
	req := httptest.NewRequest(http.MethodPost, "/v0.1/test", strings.NewReader("too large"))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	assertError(t, res, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE")
}

func authenticatedRequest(method string) *http.Request {
	req := httptest.NewRequest(method, "/v0.1/insights", nil)
	req.AddCookie(&http.Cookie{
		Name:  "session_id",
		Value: base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size)),
	})
	return req
}

func addProof(req *http.Request, origin, token string) {
	req.Header.Set("Origin", origin)
	addCSRF(req, token)
}

func addCSRF(req *http.Request, token string) {
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
	req.Header.Set(csrfHeaderName, token)
}

func assertError(t *testing.T, res *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var response httpx.Error
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v body=%q", err, res.Body.String())
	}
	if res.Code != status || response.Code != code {
		t.Fatalf("status=%d error=%+v", res.Code, response)
	}
}

func headerListContains(lines []string, value string) bool {
	for _, line := range lines {
		for _, item := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return true
			}
		}
	}
	return false
}
