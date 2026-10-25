package main

import (
	"context"
	"crypto/rand"
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
	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdminSourcePostgresLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = migrate.Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	userID := uuid.NewString()
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		t.Fatal(err)
	}
	tokenHash := sha256.Sum256(token)
	csrfHash := sha256.Sum256(make([]byte, 32))
	_, err = pool.Exec(ctx, `INSERT INTO users(id,identity_issuer,identity_subject) VALUES($1,'admin-api-test',$2)`, userID, userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM admin_permission_grants WHERE user_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	_, err = pool.Exec(ctx, `INSERT INTO user_sessions(user_id,session_token_hash,csrf_token_hash,expires_at) VALUES($1,$2,$3,now()+interval '1 hour')`, userID, tokenHash[:], csrfHash[:])
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newAPIHandler(authn.NewPostgresStore(pool), nil, sources.NewStore(pool, sources.Validator{}), sources.NewPostgresAuthorizer(pool), sha256.Sum256([]byte("integration key")), httpsecurity.Options{RequestTimeout: time.Second * 5, MaxBodyBytes: 1 << 20, AllowedOrigins: httpsecurity.OriginAllowlist{"https://app.example": {}}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := request(method, path, body, false, true)
		req.AddCookie(&http.Cookie{Name: "session_id", Value: base64.RawURLEncoding.EncodeToString(token)})
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, res.Code, status, res.Body.String())
		}
		return res
	}
	grant := func(permission sources.Permission) {
		t.Helper()
		_, err = pool.Exec(ctx, `INSERT INTO admin_permission_grants(user_id,permission) VALUES($1,$2)`, userID, string(permission))
		if err != nil {
			t.Fatal(err)
		}
	}
	call("GET", "/v0.1/admin/sources", "", 403)
	grant(sources.ReadSources)
	call("GET", "/v0.1/admin/sources", "", 200)
	for _, update := range []string{
		`UPDATE admin_permission_grants SET expires_at=now()-interval '1 second', created_at=now()-interval '2 hours' WHERE user_id=$1`,
		`UPDATE admin_permission_grants SET expires_at=NULL,revoked_at=now() WHERE user_id=$1`,
	} {
		if _, err = pool.Exec(ctx, update, userID); err != nil {
			t.Fatal(err)
		}
		call("GET", "/v0.1/admin/sources", "", 403)
	}
	_, err = pool.Exec(ctx, `UPDATE admin_permission_grants SET revoked_at=NULL WHERE user_id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	grant(sources.ManageSources)
	call("POST", "/v0.1/admin/sources", sourceInput, 403)
	grant(sources.SetTrust)
	call("POST", "/v0.1/admin/sources", strings.Replace(sourceInput, "https://example.com/feed", "http://127.0.0.1/private", 1), 400)
	// A public literal avoids network/DNS dependencies while exercising the real validator.
	res := call("POST", "/v0.1/admin/sources", strings.Replace(sourceInput, "https://example.com/feed", "https://93.184.216.34/feed", 1), 201)
	var source sources.Source
	if err = json.Unmarshal(res.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, source.SourceID) })
	path := "/v0.1/admin/sources/" + source.SourceID
	if res.Header().Get("ETag") != `"1"` || res.Header().Get("Location") != path {
		t.Fatalf("create headers=%v", res.Header())
	}
	call("GET", path, "", 200)
	call("PATCH", path, `{"enabled":false}`, 200)
	call("PATCH", path, `{"enabled":true}`, 409)
	_, err = pool.Exec(ctx, `UPDATE users SET disabled_at=now() WHERE id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	call("GET", path, "", 401)
	_, err = pool.Exec(ctx, `UPDATE users SET disabled_at=NULL WHERE id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	call("GET", path, "", 401)
}
