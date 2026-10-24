package userapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
)

const (
	testUserID    = "11111111-1111-4111-8111-111111111111"
	testContextID = "22222222-2222-4222-8222-222222222222"
	secondContext = "33333333-3333-4333-8333-333333333333"
)

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
	return authn.Middleware(fakeSessionStore{principal: authn.Principal{
		UserID:      testUserID,
		DisplayName: stringPtr("Demo User"),
	}}, Handler(store))
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

	cursor, err := encodeCursor(contextCursor{UserID: testUserID, AfterID: testContextID}, [sha256.Size]byte{})
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
		UserID:  "66666666-6666-4666-8666-666666666666",
		AfterID: testContextID,
	}, [sha256.Size]byte{})
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/contexts?cursor="+foreignCursor, ""))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("foreign cursor status=%d body=%s", res.Code, res.Body.String())
	}

	tampered := []byte(cursor)
	if tampered[len(tampered)-1] == 'A' {
		tampered[len(tampered)-1] = 'B'
	} else {
		tampered[len(tampered)-1] = 'A'
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/contexts?cursor="+string(tampered), ""))
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
	authenticatedHandler(&fakeContextStore{}).ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/me", `{}`))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func stringPtr(value string) *string { return &value }
