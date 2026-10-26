package userapi

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/apicontract"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpsecurity"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/rawartifacts"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func fixture(t *testing.T, name string) object {
	t.Helper()
	b, e := os.ReadFile("testdata/" + name + ".json")
	if e != nil {
		t.Fatal(e)
	}
	return decode(b)
}
func TestProductSchemasAndMLTransport(t *testing.T) {
	request := fixture(t, "analysis.request")
	response := fixture(t, "analysis.ok")
	for name, v := range map[string]object{"AnalysisRequest": request, "AnalysisResponse": response} {
		if e := apicontract.Validate("ml", name, v); e != nil {
			t.Fatal(e)
		}
	}
	envelope := object{"analysis_id": response["analysis_run_id"], "state": "SUCCEEDED", "trace_id": response["trace_id"], "created_at": time.Now().UTC(), "response": response}
	if e := apicontract.Validate("public", "AnalysisOperation", envelope); e != nil {
		t.Fatal(e)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Idempotency-Key") != request["request_operation_id"] || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("incorrect authentication or idempotency")
		}
		httpx.WriteJSON(w, 202, envelope)
	}))
	defer server.Close()
	client := MLClient{BaseURL: server.URL, Token: "secret"}
	out, mid, e := client.analysis(context.Background(), request, "")
	if e != nil || out["status"] != "succeeded" || mid != str(response["analysis_run_id"]) || calls != 1 {
		t.Fatalf("%v %v %s", out, e, mid)
	}
}
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	name := "product_" + uuid.NewString()[:8]
	if _, e = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.Database = name
	cfg.MaxConns = 1
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Close()
	})
	if _, e = migrate.Run(ctx, pool); e != nil {
		t.Fatal(e)
	}
	return pool
}
func TestProductLifecyclePostgres(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	request := fixture(t, "analysis.request")
	response := fixture(t, "analysis.ok")
	chunk := obj(array(fixture(t, "chunks.ok")["results"])[0])
	c, event := obj(request["context"]), obj(request["event"])
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO users(id,identity_issuer,identity_subject) VALUES($1,'product-test','user'),($2,'product-test','other')`, testUserID, uuid.NewString())
	exec(`INSERT INTO companies(id,synthetic_uid) VALUES($1,'product-company')`, c["company_id"])
	data := obj(c["profile_data"])
	data["profile_features"] = c["profile_features"]
	exec(`INSERT INTO business_profiles(id,subject_type,company_id,version,status,profile_data) VALUES($1,'company',$2,1,'active',$3)`, c["profile_id"], c["company_id"], raw(data))
	exec(`INSERT INTO business_contexts(id,subject_type,company_id,profile_id,role_code) VALUES($1,'company',$2,$3,'cfo')`, c["business_context_id"], c["company_id"], c["profile_id"])
	exec(`INSERT INTO context_access_grants(user_id,company_id,role_code) VALUES($1,$2,'cfo')`, testUserID, c["company_id"])
	exec(`CREATE SCHEMA ml; CREATE TABLE ml.events(id uuid PRIMARY KEY,event_type text,title text,summary text,occurred_at timestamptz,geography jsonb,status text,confidence numeric);CREATE TABLE ml.event_documents(event_id uuid,document_version_id uuid);CREATE TABLE ml.document_version_catalog(document_version_id uuid,ready boolean);CREATE TABLE ml.document_versions(id uuid,snapshot jsonb);CREATE TABLE ml.document_chunks(id uuid,document_version_id uuid,chunk_text text)`)
	exec(`INSERT INTO ml.events VALUES($1,$2,$3,$4,NULL,'[]','confirmed',NULL)`, event["id"], event["event_type"], event["title"], event["summary"])
	exec(`INSERT INTO ml.event_documents VALUES($1,$2)`, event["id"], chunk["document_version_id"])
	exec(`INSERT INTO ml.document_version_catalog VALUES($1,true)`, chunk["document_version_id"])
	snapshot := object{"document_id": chunk["document_id"], "title": chunk["title"], "canonical_url": chunk["canonical_url"], "published_at": chunk["published_at"], "retrieved_at": chunk["retrieved_at"], "source": object{"source_id": chunk["source_id"], "name": chunk["source_name"], "source_type": chunk["source_type"], "source_role": chunk["source_role"], "trust_level": chunk["trust_level"]}}
	exec(`INSERT INTO ml.document_versions VALUES($1,$2)`, chunk["document_version_id"], raw(snapshot))
	exec(`INSERT INTO ml.document_chunks VALUES($1,$2,$3)`, chunk["chunk_id"], chunk["document_version_id"], chunk["chunk_text"])
	calls := 0
	ml := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in object
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path == "/internal/v0.1/feed-rankings" {
			item := object{"preview_id": uuid.NewString(), "event": object{"event_id": event["id"], "title": event["title"], "summary": event["summary"]}, "published_at": nil, "topic": "credit", "why_relevant": "Profile match", "feature_refs": []any{}, "sources": []any{}, "impact": object{"label": "medium", "explanation": "Test ranking", "policy_version": "test"}}
			httpx.WriteJSON(w, 200, object{"items": []any{item, item}, "next_cursor": nil})
			return
		}
		calls++
		out := fixture(t, "analysis.ok")
		out["trace_id"] = in["trace_id"]
		out["request_operation_id"] = in["request_operation_id"]
		obj(out["insight"])["event_id"] = obj(in["event"])["id"]
		if in["request_type"] == "follow_up" {
			if e := apicontract.Validate("ml", "AnalysisRequest", in); e != nil {
				t.Error(e)
			}
			out = fixture(t, "follow_up.ok")
			out["trace_id"] = in["trace_id"]
			out["request_operation_id"] = in["request_operation_id"]
			msg := obj(out["message"])
			follow := obj(in["follow_up"])
			msg["conversation_id"] = follow["conversation_id"]
			msg["parent_message_id"] = follow["request_message_id"]
		}
		if e := apicontract.Validate("ml", "AnalysisResponse", out); e != nil {
			t.Error(e)
		}
		httpx.WriteJSON(w, 200, out)
	}))
	defer ml.Close()
	store := NewPostgresStore(pool)
	store.ConfigureML(ml.URL, "secret", "processing-v1")
	handlerFor := func(user string) http.Handler {
		return httpsecurity.Protect(authn.Middleware(fakeSessionStore{authn.Principal{UserID: user, SessionID: testSessionID, CSRFTokenHash: sha256.Sum256(make([]byte, 32))}}, httpsecurity.RequireMutationProof(Handler(store, testCursorSigningKey), testAllowedOrigins)), httpsecurity.Options{RequestTimeout: 5 * time.Second, MaxBodyBytes: 1 << 20, AllowedOrigins: testAllowedOrigins})
	}
	handler := handlerFor(testUserID)
	call := func(method, path string, input any, k string, status int) object {
		t.Helper()
		body := ""
		if input != nil {
			body = string(raw(input))
		}
		r := authenticatedRequest(method, path, body)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://app.example")
		token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		r.Header.Set("X-CSRF-Token", token)
		r.AddCookie(&http.Cookie{Name: "csrf_token", Value: token})
		if k != "" {
			r.Header.Set("Idempotency-Key", k)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, status, w.Body.String())
		}
		return decode(w.Body.Bytes())
	}
	call("GET", "/v0.1/me", nil, "", 200)
	call("GET", "/v0.1/contexts", nil, "", 200)
	call("GET", "/v0.1/alerts?context_id="+str(c["business_context_id"]), nil, "", 200)
	feedPath := "/v0.1/feed?context_id=" + str(c["business_context_id"]) + "&limit=1"
	first := call("GET", feedPath, nil, "", 200)
	next := str(first["next_cursor"])
	if next == "" {
		t.Fatal("missing feed cursor")
	}
	call("GET", feedPath+"&cursor="+next, nil, "", 200)
	call("GET", feedPath+"&topic=other&cursor="+next, nil, "", 400)
	input := object{"context_id": c["business_context_id"], "event_id": event["id"]}
	var parallel sync.WaitGroup
	ids := make(chan string, 4)
	for i := 0; i < 4; i++ {
		parallel.Add(1)
		go func() {
			defer parallel.Done()
			result := call("POST", "/v0.1/insights", input, "initial", 202)
			ids <- str(result["generation_id"])
		}()
	}
	parallel.Wait()
	close(ids)
	gid := ""
	for got := range ids {
		if gid == "" {
			gid = got
		}
		if got != gid {
			t.Fatal("concurrent delivery created another operation")
		}
	}
	if gid == "" {
		t.Fatal("generation was not created")
	}
	g := call("GET", "/v0.1/generations/"+gid, nil, "", 200)
	again := call("POST", "/v0.1/insights", input, "initial", 202)
	if again["generation_id"] != gid {
		t.Fatal("duplicate generation")
	}
	// A browser reload generates a new key, but must restore the same operation.
	reloaded := call("POST", "/v0.1/insights", input, "reload-new-key", 202)
	if reloaded["generation_id"] != gid {
		t.Fatal("reload created another paid operation")
	}
	// Recreate API/store state: the recovered key is bound durably in PostgreSQL.
	store = NewPostgresStore(pool)
	store.ConfigureML(ml.URL, "secret", "processing-v1")
	handler = handlerFor(testUserID)
	restarted := call("POST", "/v0.1/insights", input, "reload-new-key", 202)
	if restarted["generation_id"] != gid {
		t.Fatal("recovery key did not survive store recreation")
	}
	bad := object{"context_id": c["business_context_id"], "event_id": uuid.NewString()}
	call("POST", "/v0.1/insights", bad, "initial", 409)
	call("POST", "/v0.1/insights", bad, "reload-new-key", 409)
	if e := store.runAnalysis(ctx); e != nil {
		t.Fatal(e)
	}
	g = call("GET", "/v0.1/generations/"+gid, nil, "", 200)
	if g["state"] != "SUCCEEDED" {
		t.Fatalf("generation %+v", g)
	}
	iid := str(g["insight_id"])
	ins := call("GET", "/v0.1/insights/"+iid, nil, "", 200)
	if e := apicontract.Validate("public", "Insight", ins); e != nil {
		t.Fatal(e)
	}
	reloaded = call("POST", "/v0.1/insights", input, "reload-after-success", 202)
	if reloaded["generation_id"] != gid || reloaded["insight_id"] != iid {
		t.Fatal("completed analysis was not restored")
	}
	// Public canonical URLs are resolved from the exact saved version.
	originalPath := "/v0.1/insights/" + iid + "/documents/" + str(chunk["document_version_id"]) + "/original"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, authenticatedRequest("GET", originalPath, ""))
	if w.Code != 307 || w.Header().Get("Location") != chunk["canonical_url"] {
		t.Fatalf("original redirect %d %s", w.Code, w.Header().Get("Location"))
	}
	call("GET", "/v0.1/insights/"+iid+"/documents/"+uuid.NewString()+"/original", nil, "", 404)
	// Closed files are downloaded through Go; no private S3 URI reaches clients.
	pdf := []byte("%PDF-test original")
	digest := sha256.Sum256(pdf)
	sid, aid, artifactID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO sources(id,name,source_type,source_role,trust_level,adapter_type,endpoint,enabled,polling_interval_seconds) VALUES($1,'Bank','bank_analytics','bank','primary','dataset','dataset://test',false,900)`, sid)
	exec(`INSERT INTO raw_artifacts(id,source_id,sha256,content_type,size_bytes,object_uri) VALUES($1,$2,$3,'application/pdf',$4,'s3://private/raw/test')`, artifactID, sid, hex.EncodeToString(digest[:]), len(pdf))
	exec(`INSERT INTO articles(id,source_id,identity_kind,identity_key) VALUES($1,$2,'fingerprint','test')`, aid, sid)
	exec(`INSERT INTO article_versions(id,source_id,article_id,version_no,content_hash,parser_version,title,body_text,fetched_at,raw_artifact_id) VALUES($1,$2,$3,1,$4,'test','Bank report','Report text',now(),$5)`, chunk["document_version_id"], sid, aid, hex.EncodeToString(digest[:]), artifactID)
	exec(`UPDATE insights SET ml_snapshot=jsonb_set(ml_snapshot,'{chunks,0,canonical_url}','null') WHERE id=$1`, iid)
	reads := 0
	store.ReadOriginal = func(_ context.Context, artifact rawartifacts.Artifact) ([]byte, error) {
		reads++
		if artifact.ObjectURI != "s3://private/raw/test" {
			t.Fatal("wrong artifact")
		}
		return pdf, nil
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, authenticatedRequest("GET", originalPath, ""))
	if w.Code != 200 || w.Body.String() != string(pdf) || reads != 1 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("original download %d %s", w.Code, w.Body.String())
	}
	store.ReadOriginal = func(context.Context, rawartifacts.Artifact) ([]byte, error) { return []byte("corrupt"), nil }
	call("GET", originalPath, nil, "", 503)
	call("GET", "/v0.1/traces/"+str(g["trace_id"]), nil, "", 200)
	conv := call("POST", "/v0.1/insights/"+iid+"/conversations", object{}, "conversation", 201)
	cid := str(conv["conversation_id"])
	call("GET", "/v0.1/insights/"+iid+"/conversations", nil, "", 200)
	fg := call("POST", "/v0.1/conversations/"+cid+"/messages", object{"text": "What should we check?"}, "question", 202)
	call("POST", "/v0.1/conversations/"+cid+"/messages", object{"text": "What should we check?"}, "question", 202)
	call("POST", "/v0.1/conversations/"+cid+"/messages", object{"text": "Other question"}, "other", 409)
	if e := store.runAnalysis(ctx); e != nil {
		t.Fatal(e)
	}
	fg = call("GET", "/v0.1/generations/"+str(fg["generation_id"]), nil, "", 200)
	if fg["state"] != "SUCCEEDED" {
		t.Fatalf("follow-up %+v", fg)
	}
	messages := call("GET", "/v0.1/conversations/"+cid+"/messages", nil, "", 200)
	small := call("GET", "/v0.1/conversations/"+cid+"/messages?limit=1", nil, "", 200)
	call("GET", "/v0.1/conversations/"+cid+"/messages?limit=1&cursor="+str(small["next_cursor"]), nil, "", 200)
	if len(array(messages["items"])) != 2 {
		t.Fatal(messages)
	}
	call("POST", "/v0.1/insights/"+iid+"/feedback", object{"insight_revision": "1", "rating": "POSITIVE"}, "feedback", 201)
	analytics := object{"events": []any{object{"analytics_event_id": "evt-1", "schema_version": "1", "event_type": "INSIGHT_OPENED", "occurred_at": time.Now().UTC(), "context_id": c["business_context_id"], "insight_id": iid}}}
	call("POST", "/v0.1/analytics/events", analytics, "", 202)
	call("POST", "/v0.1/analytics/events", analytics, "", 202)
	if calls != 2 {
		t.Fatalf("paid operation count %d", calls)
	}
	// A protocol-violating ML server reuses a result ID for another operation.
	collision := call("POST", "/v0.1/insights", object{"context_id": c["business_context_id"], "event_id": event["id"], "force_new": true}, "explicit-new-analysis", 202)
	if collision["generation_id"] == gid {
		t.Fatal("explicit analysis was incorrectly deduplicated")
	}
	call("POST", "/v0.1/insights", input, "explicit-new-analysis", 409)
	if e := store.runAnalysis(ctx); e != nil {
		t.Fatal(e)
	}
	collision = call("GET", "/v0.1/generations/"+str(collision["generation_id"]), nil, "", 200)
	if collision["state"] != "FAILED" || obj(collision["error"])["code"] != "INVALID_ML_RESPONSE" {
		t.Fatalf("identity collision did not terminate: %+v", collision)
	}
	previousSuccess := call("POST", "/v0.1/insights", input, "open-after-failed-regeneration", 202)
	if previousSuccess["generation_id"] != gid {
		t.Fatal("failed regeneration hid an existing verified Insight")
	}
	exec(`UPDATE business_profiles SET version=2 WHERE id=$1`, c["profile_id"])
	fresh := call("POST", "/v0.1/insights", input, "new-profile", 202)
	if fresh["generation_id"] == gid || fresh["generation_id"] == collision["generation_id"] {
		t.Fatal("old profile result was restored as current")
	}
	handler = handlerFor(uuid.NewString())
	call("GET", originalPath, nil, "", 404)
	call("GET", "/v0.1/insights/"+iid, nil, "", 404)
	call("GET", "/v0.1/generations/"+gid, nil, "", 404)
	call("GET", "/v0.1/conversations/"+cid+"/messages", nil, "", 404)
	handler = handlerFor(testUserID)
	exec(`UPDATE context_access_grants SET revoked_at=now() WHERE user_id=$1`, testUserID)
	call("GET", "/v0.1/insights/"+iid, nil, "", 403)
	call("GET", originalPath, nil, "", 403)
	_ = response
}

func TestPasswordLoginPostgres(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	user := uuid.NewString()
	salt := make([]byte, 32)
	hash, e := pbkdf2.Key(sha256.New, "correct-password-for-test", salt, 600000, 32)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO users(id,identity_issuer,identity_subject) VALUES($1,'local-password','login-test')`, user); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO user_passwords(user_id,login,salt,password_hash,iterations) VALUES($1,'login-test',$2,$3,600000)`, user, salt, hash); e != nil {
		t.Fatal(e)
	}
	store := NewPostgresStore(pool)
	login := store.LoginHandler(testAllowedOrigins, true, false)
	request := func(origin, password string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/v0.1/auth/login", strings.NewReader(string(raw(object{"login": "login-test", "password": password}))))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		login.ServeHTTP(w, r)
		return w
	}
	if w := request("https://evil.example", "correct-password-for-test"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("https://app.example", "wrong"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request("https://app.example", "correct-password-for-test")
	if w.Code != 200 {
		t.Fatalf("login %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatal(cookies)
	}
	csrf := ""
	for _, cookie := range cookies {
		if cookie.Name == "csrf_token" {
			csrf = cookie.Value
			if cookie.HttpOnly {
				t.Fatal("csrf cookie must be readable")
			}
		}
		if cookie.Name == "session_id" && !cookie.HttpOnly {
			t.Fatal("session cookie must be HttpOnly")
		}
	}
	handler := authn.Middleware(authn.NewPostgresStore(pool), httpsecurity.RequireMutationProof(Handler(store, testCursorSigningKey), testAllowedOrigins))
	call := func(method, path, proof string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if method == "GET" {
			r = httptest.NewRequest(method, path, nil)
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		r.Header.Set("Origin", "https://app.example")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", proof)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w = call("GET", "/v0.1/me", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = call("POST", "/v0.1/auth/logout", "invalid"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = call("POST", "/v0.1/auth/logout", csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = call("GET", "/v0.1/me", ""); w.Code != 401 {
		t.Fatal("revoked session still works")
	}
}

func TestAdministrativeSyncPostgres(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	user := uuid.NewString()
	if _, e := pool.Exec(ctx, `INSERT INTO users(id,identity_issuer,identity_subject) VALUES($1,'test','admin');`, user); e != nil {
		t.Fatal(e)
	}
	for _, permission := range []string{"sources:read", "sources:manage", "sources:trust"} {
		if _, e := pool.Exec(ctx, `INSERT INTO admin_permission_grants(user_id,permission) VALUES($1,$2)`, user, permission); e != nil {
			t.Fatal(e)
		}
	}
	registry := sources.NewStore(pool, sources.Validator{})
	admin, e := sources.AdminHandler(registry, sources.NewPostgresAuthorizer(pool), testCursorSigningKey)
	if e != nil {
		t.Fatal(e)
	}
	handler := authn.Middleware(fakeSessionStore{authn.Principal{UserID: user, SessionID: testSessionID}}, admin)
	call := func(method, path string, input any, version string, status int) object {
		t.Helper()
		r := authenticatedRequest(method, path, string(raw(input)))
		if method == "GET" {
			r = authenticatedRequest(method, path, "")
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", version)
		r.Header.Set("Idempotency-Key", "manual-sync")
		if strings.Contains(path, "/sources") && !strings.Contains(path, "syncs") {
			r.Header.Del("Idempotency-Key")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s %d want %d %s", method, path, w.Code, status, w.Body.String())
		}
		return decode(w.Body.Bytes())
	}
	cfg := object{"name": "Bank", "source_type": "bank_analytics", "source_role": "bank", "trust_level": "primary", "adapter_type": "rss", "endpoint": "https://93.184.216.34/feed", "enabled": true, "polling_interval_seconds": 900}
	created := call("POST", "/v0.1/admin/sources", cfg, "", 201)
	sid := str(created["source_id"])
	if _, e = pool.Exec(ctx, `UPDATE admin_permission_grants SET revoked_at=now() WHERE user_id=$1 AND permission='sources:trust'`, user); e != nil {
		t.Fatal(e)
	}
	call("PATCH", "/v0.1/admin/sources/"+sid, object{"endpoint": "https://93.184.216.34/attacker"}, `"1"`, 403)
	sync := call("POST", "/v0.1/admin/sources/"+sid+"/syncs", object{}, "", 202)
	syncID := str(sync["sync_id"])
	again := call("POST", "/v0.1/admin/sources/"+sid+"/syncs", object{}, "", 202)
	if again["sync_id"] != syncID {
		t.Fatal("duplicate sync request")
	}
	polls := polling.NewStore(pool)
	lease, found, e := polls.Claim(ctx, time.Minute)
	if e != nil || !found {
		t.Fatalf("claim %v %v", found, e)
	}
	running := call("GET", "/v0.1/admin/source-syncs/"+syncID, nil, "", 200)
	if running["state"] != "RUNNING" {
		t.Fatal(running)
	}
	if e = polls.Finish(ctx, lease, polling.Outcome{Success: true}); e != nil {
		t.Fatal(e)
	}
	done := call("GET", "/v0.1/admin/source-syncs/"+syncID, nil, "", 200)
	if done["state"] != "SUCCEEDED" {
		t.Fatal(done)
	}
	if _, e = pool.Exec(ctx, `DELETE FROM source_polls WHERE id=$1`, lease.PollID); e != nil {
		t.Fatal(e)
	}
	done = call("GET", "/v0.1/admin/source-syncs/"+syncID, nil, "", 200)
	if done["state"] != "SUCCEEDED" {
		t.Fatal("retention changed completed sync", done)
	}
	from := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	to := time.Now().UTC().Format(time.RFC3339)
	report := call("GET", "/v0.1/admin/quality?from="+url.QueryEscape(from)+"&to="+url.QueryEscape(to), nil, "", 200)
	if e := apicontract.Validate("public", "QualityReport", report); e != nil {
		t.Fatal(e)
	}
}

func TestAnalysisRestoresOperationAndHonorsRetryAfter(t *testing.T) {
	request := fixture(t, "analysis.request")
	response := fixture(t, "analysis.ok")
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte("not found"))
			return
		}
		postCount++
		if r.Header.Get("Idempotency-Key") != request["request_operation_id"] {
			t.Error("idempotency key changed")
		}
		var received object
		_ = json.NewDecoder(r.Body).Decode(&received)
		if string(raw(received)) != string(raw(request)) {
			t.Error("request snapshot changed")
		}
		if postCount == 1 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(503)
			_, _ = w.Write([]byte("temporary gateway error"))
			return
		}
		httpx.WriteJSON(w, 200, response)
	}))
	defer server.Close()
	client := MLClient{BaseURL: server.URL, Token: "secret"}
	_, _, err := client.analysis(context.Background(), request, "")
	var f *apiFailure
	if !errors.As(err, &f) || !f.retryable || f.retryAfter < 119*time.Second {
		t.Fatalf("retry-after not retained: %+v", err)
	}
	result, _, err := client.analysis(context.Background(), request, str(response["analysis_run_id"]))
	if err != nil || result["status"] != "succeeded" || postCount != 2 {
		t.Fatalf("restore: %+v %v calls=%d", result, err, postCount)
	}
}
