package sources

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/jackc/pgx/v5"
)

type permissionDB struct {
	allowed bool
	err     error
	args    []any
	calls   int
}
type permissionRow struct{ db *permissionDB }

func (d *permissionDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	d.calls++
	d.args = args
	return permissionRow{d}
}
func (r permissionRow) Scan(dest ...any) error {
	if r.db.err != nil {
		return r.db.err
	}
	*dest[0].(*bool) = r.db.allowed
	return nil
}

type adminSession struct{ principal authn.Principal }

func (s adminSession) Authenticate(context.Context, [sha256.Size]byte) (authn.Principal, error) {
	return s.principal, nil
}

func adminRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.AddCookie(&http.Cookie{Name: "session_id", Value: base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))})
	return r
}

func TestPostgresAuthorizerFailsClosedAndRechecksEachPermission(t *testing.T) {
	db := &permissionDB{allowed: true}
	authorize := NewPostgresAuthorizer(db)
	principal := authn.Principal{UserID: "11111111-1111-4111-8111-111111111111", SessionID: "session"}
	for _, permission := range []Permission{ReadSources, ManageSources, SetTrust} {
		handler := authn.Middleware(adminSession{principal}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if status := authorize(r, permission); status != 0 {
				t.Fatalf("permission=%s status=%d", permission, status)
			}
			if db.args[0] != principal.UserID || db.args[1] != string(permission) {
				t.Fatalf("query args=%v", db.args)
			}
			w.WriteHeader(204)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), adminRequest("GET", "/"))
	}
	if db.calls != 3 {
		t.Fatalf("permission queries=%d", db.calls)
	}
	db.allowed = false
	handler := authn.Middleware(adminSession{principal}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := authorize(r, ReadSources); got != 403 {
			t.Fatalf("revoked permission status=%d", got)
		}
		db.err = errors.New("private database error")
		if got := authorize(r, ReadSources); got != 500 {
			t.Fatalf("database failure status=%d", got)
		}
		if got := authorize(r, "unknown"); got != 403 {
			t.Fatalf("unknown permission status=%d", got)
		}
	}))
	handler.ServeHTTP(httptest.NewRecorder(), adminRequest("GET", "/"))
	if got := authorize(httptest.NewRequest("GET", "/", nil), ReadSources); got != 401 {
		t.Fatalf("missing principal=%d", got)
	}
}
