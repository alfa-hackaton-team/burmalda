package authn

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
)

type fakeStore struct {
	principal Principal
	err       error
	gotHash   [sha256.Size]byte
	called    int
}

func (s *fakeStore) Authenticate(_ context.Context, tokenHash [sha256.Size]byte) (Principal, error) {
	s.called++
	s.gotHash = tokenHash
	return s.principal, s.err
}

func TestMiddlewareAuthenticatesOpaqueSession(t *testing.T) {
	rawToken := make([]byte, sha256.Size)
	for i := range rawToken {
		rawToken[i] = byte(i + 1)
	}
	expectedHash := sha256.Sum256(rawToken)
	store := &fakeStore{principal: Principal{UserID: "user-id", SessionID: "session-id"}}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.UserID != "user-id" {
			t.Fatalf("principal missing from request: %+v, %v", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := Middleware(store, next)
	req := httptest.NewRequest(http.MethodGet, "/v0.1/me", nil)
	req.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: base64.RawURLEncoding.EncodeToString(rawToken),
	})
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent || store.called != 1 || store.gotHash != expectedHash {
		t.Fatalf("status=%d calls=%d hash=%x", res.Code, store.called, store.gotHash)
	}
}

func TestMiddlewareRejectsMissingMalformedAndExpiredSession(t *testing.T) {
	tests := []struct {
		name   string
		cookie *http.Cookie
		store  *fakeStore
	}{
		{name: "missing", store: &fakeStore{}},
		{name: "malformed", cookie: &http.Cookie{Name: sessionCookieName, Value: "not-base64!"}, store: &fakeStore{}},
		{
			name:   "not found",
			cookie: &http.Cookie{Name: sessionCookieName, Value: base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))},
			store:  &fakeStore{err: ErrUnauthenticated},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := Middleware(tt.store, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("protected handler was called")
			}))
			req := httptest.NewRequest(http.MethodGet, "/v0.1/me", nil)
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			res := httptest.NewRecorder()

			handler.ServeHTTP(res, req)

			if res.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
			if got := res.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("content type=%q", got)
			}
		})
	}
}

func TestMiddlewareRejectsDuplicateSessionCookie(t *testing.T) {
	store := &fakeStore{principal: Principal{UserID: "user-id"}}
	handler := Middleware(store, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler was called")
	}))
	req := httptest.NewRequest(http.MethodGet, "/v0.1/me", nil)
	value := base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized || store.called != 0 {
		t.Fatalf("status=%d calls=%d body=%q", res.Code, store.called, res.Body.String())
	}
}

func TestMiddlewareHidesStoreFailure(t *testing.T) {
	store := &fakeStore{err: errors.New("database password leaked")}
	handler := Middleware(store, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/v0.1/me", nil)
	req.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size)),
	})
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError || res.Body.String() == "database password leaked" {
		t.Fatalf("status=%d body=%q", res.Code, res.Body.String())
	}
}

func TestMiddlewareTimeoutUsesOpenAPIErrorCode(t *testing.T) {
	handler := Middleware(timeoutStore{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler was called")
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v0.1/me", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size)),
	})
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	var response httpx.Error
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if res.Code != http.StatusGatewayTimeout || response.Code != httpx.ErrorCodeTimeout || !response.Retryable {
		t.Fatalf("status=%d error=%+v", res.Code, response)
	}
}

type timeoutStore struct{}

func (timeoutStore) Authenticate(ctx context.Context, _ [sha256.Size]byte) (Principal, error) {
	<-ctx.Done()
	return Principal{}, ctx.Err()
}
