package userapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/apicontract"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPrecomputedInsightPostgres(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	request := fixture(t, "analysis.request")
	c, event := obj(request["context"]), obj(request["event"])
	chunk := obj(array(fixture(t, "chunks.ok")["results"])[0])
	other := uuid.NewString()
	exec(`INSERT INTO users(id,identity_issuer,identity_subject) VALUES($1,'precompute-test','owner'),($2,'precompute-test','other')`, testUserID, other)
	exec(`INSERT INTO companies(id,synthetic_uid) VALUES($1,'test-only-company')`, c["company_id"])
	profile := obj(c["profile_data"])
	profile["profile_features"] = c["profile_features"]
	exec(`INSERT INTO business_profiles(id,subject_type,company_id,version,status,profile_data) VALUES($1,'company',$2,1,'active',$3)`, c["profile_id"], c["company_id"], raw(profile))
	exec(`INSERT INTO business_contexts(id,subject_type,company_id,profile_id,role_code) VALUES($1,'company',$2,$3,'cfo')`, c["business_context_id"], c["company_id"], c["profile_id"])
	exec(`INSERT INTO context_access_grants(user_id,company_id,role_code) VALUES($1,$2,'cfo')`, testUserID, c["company_id"])
	exec(`CREATE SCHEMA ml;CREATE TABLE ml.events(id uuid,event_type text,title text,summary text,occurred_at timestamptz,geography jsonb,status text,confidence numeric);CREATE TABLE ml.event_documents(event_id uuid,document_version_id uuid);CREATE TABLE ml.document_version_catalog(document_version_id uuid,ready boolean);CREATE TABLE ml.document_versions(id uuid,snapshot jsonb);CREATE TABLE ml.document_chunks(id uuid,document_version_id uuid,chunk_text text)`)
	exec(`INSERT INTO ml.events VALUES($1,$2,$3,$4,NULL,'[]','confirmed',NULL)`, event["id"], event["event_type"], event["title"], event["summary"])
	exec(`INSERT INTO ml.event_documents VALUES($1,$2)`, event["id"], chunk["document_version_id"])
	exec(`INSERT INTO ml.document_version_catalog VALUES($1,true)`, chunk["document_version_id"])
	doc := object{"document_id": chunk["document_id"], "title": chunk["title"], "canonical_url": chunk["canonical_url"], "published_at": chunk["published_at"], "retrieved_at": chunk["retrieved_at"], "source": object{"source_id": chunk["source_id"], "name": chunk["source_name"], "source_type": chunk["source_type"], "source_role": chunk["source_role"], "trust_level": chunk["trust_level"]}}
	exec(`INSERT INTO ml.document_versions VALUES($1,$2)`, chunk["document_version_id"], raw(doc))
	exec(`INSERT INTO ml.document_chunks VALUES($1,$2,$3)`, chunk["chunk_id"], chunk["document_version_id"], chunk["chunk_text"])
	var readMLCalls atomic.Int32
	tripwire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { readMLCalls.Add(1); w.WriteHeader(500) }))
	defer tripwire.Close()
	newStore := func(p *pgxpool.Pool) *PostgresStore {
		s := NewPostgresStore(p)
		s.ConfigureML(tripwire.URL, "test-only", "processing-v1")
		s.ConfigureDemo("demo-v1")
		return s
	}
	store := newStore(pool)
	calls := 0
	status := "succeeded"
	badQuote := false
	execute := func(ctx context.Context, in map[string]any) (map[string]any, error) {
		calls++
		r := obj(in["request"])
		out := fixture(t, "analysis.ok")
		out["trace_id"] = r["trace_id"]
		out["request_operation_id"] = r["request_operation_id"]
		out["status"] = status
		if status != "succeeded" {
			out["insight"] = nil
			if status == "insufficient_evidence" {
				out["gaps"] = []any{object{"code": "NO_EVENT_EVIDENCE", "description": "No support", "required_feature_key": nil}}
			} else {
				out["error"] = object{"code": "VERIFICATION_FAILED", "message": "Rejected", "retryable": false}
			}
		} else {
			obj(out["insight"])["id"] = uuid.NewString()
			if badQuote {
				obj(array(obj(array(obj(out["insight"])["claims"])[0])["evidence"])[0])["quote_text"] = "invented quote"
			}
		}
		return object{"response": out, "chunks": []any{chunk}, "manifest": in["manifest"]}, nil
	}
	prepare := func(set string) (object, error) {
		return store.Precompute(ctx, testUserID, str(c["business_context_id"]), str(event["id"]), set, object{"version": "test-only"}, execute)
	}
	ready, e := prepare("demo-v1")
	if e != nil {
		t.Fatal(e)
	}
	if ready["state"] != "SUCCEEDED" {
		t.Fatal(ready)
	}
	iid := str(ready["insight_id"])
	again, e := prepare("demo-v1")
	if e != nil || again["generation_id"] != ready["generation_id"] || calls != 1 {
		t.Fatalf("repeat: %v %v calls=%d", again, e, calls)
	}
	ins, snapshot, e := store.insight(ctx, testUserID, iid)
	if e != nil {
		t.Fatal(e)
	}
	if e = apicontract.Validate("public", "Insight", ins); e != nil {
		t.Fatal(e)
	}
	if len(array(obj(ins["response"])["evidence"])) != 2 || len(array(snapshot["chunks"])) != 1 {
		t.Fatal("lost evidence")
	}
	// Recreate the pool/store to simulate a process restart; no in-memory cache.
	restarted, e := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	store = newStore(restarted)
	if _, _, e = store.insight(ctx, testUserID, iid); e != nil {
		t.Fatal(e)
	}
	if _, _, e = store.insight(ctx, other, iid); e == nil {
		t.Fatal("other user read Insight")
	}
	for _, change := range []struct{ name, sql, undo string }{
		{"revoked", `UPDATE context_access_grants SET revoked_at=now()`, `UPDATE context_access_grants SET revoked_at=NULL`},
		{"expired", `UPDATE context_access_grants SET created_at=now()-interval '1 day',expires_at=now()-interval '1 second'`, `UPDATE context_access_grants SET expires_at=NULL`},
		{"role", `UPDATE business_contexts SET role_code='ceo'`, `UPDATE business_contexts SET role_code='cfo'`},
		{"profile version", `UPDATE business_profiles SET version=2`, `UPDATE business_profiles SET version=1`},
		{"profile content", `UPDATE business_profiles SET profile_data=jsonb_set(profile_data,'{changed}','true')`, `UPDATE business_profiles SET profile_data=profile_data-'changed'`},
	} {
		t.Run(change.name, func(t *testing.T) {
			exec(change.sql)
			if change.name == "profile content" {
				before := calls
				if _, e = prepare("demo-v1"); e == nil || calls != before {
					t.Fatal("changed frozen slot reused or reran ML")
				}
			}
			if _, _, e = store.insight(ctx, testUserID, iid); e == nil {
				t.Fatal("incompatible access accepted")
			}
			exec(change.undo)
		})
	}
	store.ConfigureDemo("demo-v2")
	if _, _, e = store.insight(ctx, testUserID, iid); e == nil {
		t.Fatal("stale set accepted")
	}
	store.ConfigureDemo("demo-v1")
	newProfile, newContext := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO business_profiles(id,subject_type,company_id,version,status,profile_data) SELECT $1,'company',company_id,2,'active',profile_data FROM business_profiles WHERE id=$2::uuid`, newProfile, c["profile_id"])
	exec(`INSERT INTO business_contexts(id,subject_type,company_id,profile_id,role_code) VALUES($1,'company',$2,$3,'cfo')`, newContext, c["company_id"], newProfile)
	otherPage, e := store.readyInsights(ctx, testUserID, newContext)
	if e != nil || len(array(otherPage["items"])) != 0 {
		t.Fatal("another context received result", e)
	}
	exec(`DELETE FROM business_contexts WHERE id=$1::uuid`, newContext)
	exec(`UPDATE business_contexts SET profile_id=$1 WHERE id=$2::uuid`, newProfile, c["business_context_id"])
	if _, _, e = store.insight(ctx, testUserID, iid); e == nil {
		t.Fatal("different profile ID accepted")
	}
	exec(`UPDATE business_contexts SET profile_id=$1 WHERE id=$2::uuid`, c["profile_id"], c["business_context_id"])
	page, e := store.readyInsights(ctx, testUserID, str(c["business_context_id"]))
	if e != nil || len(array(page["items"])) != 1 {
		t.Fatalf("discovery: %v %v", page, e)
	}
	if _, e = store.readyInsights(ctx, other, str(c["business_context_id"])); e == nil {
		t.Fatal("foreign discovery")
	}
	// Real database-backed sessions, HTTP read path and a short loopback timing run.
	token := make([]byte, 32)
	hash := sha256.Sum256(token)
	csrf := sha256.Sum256([]byte("test"))
	exec(`INSERT INTO user_sessions(user_id,session_token_hash,csrf_token_hash,expires_at) VALUES($1,$2,$3,now()+interval '1 hour')`, testUserID, hash[:], csrf[:])
	server := httptest.NewServer(authn.Middleware(authn.NewPostgresStore(restarted), Handler(store, testCursorSigningKey)))
	defer server.Close()
	samples := []float64{}
	for i := 0; i < 30; i++ {
		start := time.Now()
		r, _ := http.NewRequest("GET", server.URL+"/v0.1/insights/"+iid, nil)
		r.AddCookie(&http.Cookie{Name: "session_id", Value: base64.RawURLEncoding.EncodeToString(token)})
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
		samples = append(samples, float64(time.Since(start).Microseconds())/1000)
		if i == 29 {
			t.Log("phases:", resp.Header.Values("Server-Timing"))
		}
	}
	sort.Float64s(samples)
	t.Logf("30 loopback HTTP GETs, real PostgreSQL sessions/grants, p50=%.3fms p95=%.3fms", samples[14], samples[28])
	for _, path := range []string{"/v0.1/insights/" + uuid.NewString(), "/v0.1/contexts/" + str(c["business_context_id"]) + "/insights"} {
		r := authenticatedRequest("GET", path, "")
		w := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(w, r)
		if w.Code != 200 && w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 1 || readMLCalls.Load() != 0 {
		t.Fatal("GET invoked ML")
	}
	exec(`UPDATE user_sessions SET revoked_at=now()`)
	r := authenticatedRequest("GET", "/v0.1/insights/"+iid, "")
	w := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("revoked session read Insight")
	}
	status = "failed"
	failed, e := prepare("failed-v1")
	if e != nil || failed["state"] != "FAILED" {
		t.Fatalf("failure %v %v", failed, e)
	}
	status = "insufficient_evidence"
	insufficient, e := prepare("insufficient-v1")
	if e != nil || insufficient["state"] != "INSUFFICIENT_EVIDENCE" {
		t.Fatalf("insufficient %v %v", insufficient, e)
	}
	status = "succeeded"
	badQuote = true
	if _, e = prepare("bad-quote-v1"); e == nil {
		t.Fatal("bad verification published")
	}
	badQuote = false
	// Force the final success update to fail: Insight and evidence writes roll back.
	exec(`CREATE FUNCTION reject_demo_success() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='SUCCEEDED' AND NEW.demo_set_version='rollback-v1' THEN RAISE EXCEPTION 'test rollback'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_demo_success BEFORE UPDATE ON request_operations FOR EACH ROW EXECUTE FUNCTION reject_demo_success()`)
	if _, e = prepare("rollback-v1"); e == nil {
		t.Fatal("expected transaction failure")
	}
	var count int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM insights`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("partial publication: count=%d %v", count, e)
	}
	unknownCalls := 0
	unknown := func(context.Context, map[string]any) (map[string]any, error) {
		unknownCalls++
		return nil, fmt.Errorf("lost process")
	}
	for i := 0; i < 2; i++ {
		_, _ = store.Precompute(ctx, testUserID, str(c["business_context_id"]), str(event["id"]), "unknown-v1", object{"v": "test"}, unknown)
	}
	if unknownCalls != 1 {
		t.Fatal("unknown outcome reran ML")
	}
	if e = store.runAnalysis(ctx); !strings.Contains(fmt.Sprint(e), "no pending analysis") {
		t.Fatalf("worker claimed demo operation: %v", e)
	}
}
