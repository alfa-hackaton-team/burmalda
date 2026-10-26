package userapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpsecurity"
	"github.com/google/uuid"
)

// Opt-in cross-process E2E. Python runs real ML2 with mocked model responses.
// No frontend, live corpus, external models, or production demo data are used.
func TestFollowUpSnapshotBackendE2E(t *testing.T) {
	mlURL, token := os.Getenv("FOLLOW_UP_E2E_ML_URL"), os.Getenv("FOLLOW_UP_E2E_TOKEN")
	if mlURL == "" || token == "" {
		t.Skip("Run the backend follow-up E2E runner with its test-only Python bridge")
	}
	pool := integrationPool(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	request, response := fixture(t, "analysis.request"), fixture(t, "analysis.ok")
	c, event := obj(request["context"]), obj(request["event"])
	chunk := obj(array(fixture(t, "chunks.ok")["results"])[0])
	exec(`INSERT INTO users(id,identity_issuer,identity_subject) VALUES($1,'e2e-fixture','owner')`, testUserID)
	exec(`INSERT INTO companies(id,synthetic_uid) VALUES($1,'e2e-fixture')`, c["company_id"])
	profile := decode(raw(c["profile_data"]))
	profile["profile_features"] = c["profile_features"]
	exec(`INSERT INTO business_profiles(id,subject_type,company_id,version,status,profile_data) VALUES($1,'company',$2,1,'active',$3)`, c["profile_id"], c["company_id"], raw(profile))
	exec(`INSERT INTO business_contexts(id,subject_type,company_id,profile_id,role_code) VALUES($1,'company',$2,$3,'cfo')`, c["business_context_id"], c["company_id"], c["profile_id"])
	exec(`INSERT INTO context_access_grants(user_id,company_id,role_code) VALUES($1,$2,'cfo')`, testUserID, c["company_id"])
	sessionBytes, csrfBytes := bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{8}, 32)
	sessionHash, csrfHash := sha256.Sum256(sessionBytes), sha256.Sum256(csrfBytes)
	session, csrf := base64.RawURLEncoding.EncodeToString(sessionBytes), base64.RawURLEncoding.EncodeToString(csrfBytes)
	exec(`INSERT INTO user_sessions(user_id,session_token_hash,csrf_token_hash,expires_at) VALUES($1,$2,$3,now()+interval '10 minutes')`, testUserID, sessionHash[:], csrfHash[:])
	store := NewPostgresStore(pool)
	store.ConfigureML(mlURL, token, "processing-v1")
	// Seed a persisted verified fixture through the existing atomic publication.
	// No ML corpus tables are created: even chunk lookup would fail this E2E.
	gid, lease := str(request["request_operation_id"]), uuid.NewString()
	exec(`INSERT INTO request_operations(id,user_id,business_context_id,operation_type,idempotency_key,request_fingerprint,request_snapshot,state,trace_id,event_id,lease_token,lease_until) VALUES($1,$2,$3,'insight','fixture',decode(repeat('00',32),'hex'),$4,'RUNNING',$5,$6,$7,now()+interval '1 minute')`, gid, testUserID, c["business_context_id"], raw(request), request["trace_id"], event["id"], lease)
	if err := store.saveAnalysisEvidence(ctx, gid, lease, request, response, []any{chunk}); err != nil {
		t.Fatal(err)
	}
	iid := str(obj(response["insight"])["id"])
	var original []byte
	if err := pool.QueryRow(ctx, `SELECT ml_snapshot FROM insights WHERE id=$1`, iid).Scan(&original); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpsecurity.Protect(authn.Middleware(authn.NewPostgresStore(pool), httpsecurity.RequireMutationProof(Handler(store, testCursorSigningKey), testAllowedOrigins)), httpsecurity.Options{RequestTimeout: 10 * time.Second, MaxBodyBytes: 1 << 20, AllowedOrigins: testAllowedOrigins}))
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}
	call := func(t *testing.T, method, path string, body any, key string, status int, authenticated bool) object {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(raw(body))
		}
		r, err := http.NewRequest(method, server.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://app.example")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", key)
		r.AddCookie(&http.Cookie{Name: "csrf_token", Value: csrf})
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "session_id", Value: session})
		}
		res, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != status {
			t.Fatalf("%s %s: %d expected %d: %s (%v)", method, path, res.StatusCode, status, data, err)
		}
		return decode(data)
	}
	call(t, "GET", "/v0.1/insights/"+iid, nil, "", 401, false)
	card := call(t, "GET", "/v0.1/insights/"+iid, nil, "", 200, true)
	for _, tc := range []struct {
		mode, state string
		assistant   bool
	}{
		{"grounded", "SUCCEEDED", true}, {"insufficient", "INSUFFICIENT_EVIDENCE", false},
		{"reject", "FAILED", false}, {"bad-quote", "FAILED", false}, {"outside-chunk", "FAILED", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			conv := call(t, "POST", "/v0.1/insights/"+iid+"/conversations", object{}, uuid.NewString(), 201, true)
			cid := str(conv["conversation_id"])
			key := uuid.NewString()
			path := "/v0.1/conversations/" + cid + "/messages"
			g := call(t, "POST", path, object{"text": tc.mode}, key, 202, true)
			if err := store.runAnalysis(ctx); err != nil {
				t.Fatal(err)
			}
			result := call(t, "GET", "/v0.1/generations/"+str(g["generation_id"]), nil, "", 200, true)
			if result["state"] != tc.state {
				t.Fatalf("generation: %v", result)
			}
			var snapshot []byte
			if err := pool.QueryRow(ctx, `SELECT request_snapshot FROM request_operations WHERE id=$1`, g["generation_id"]).Scan(&snapshot); err != nil {
				t.Fatal(err)
			}
			initial := obj(obj(decode(snapshot)["follow_up"])["initial_insight"])
			for _, field := range []string{"context", "event", "insight", "chunks"} {
				if !bytes.Equal(raw(initial[field]), raw(decode(original)[field])) {
					t.Fatalf("saved %s changed", field)
				}
			}
			// Duplicate HTTP submit must reuse the operation, not invoke ML again.
			again := call(t, "POST", path, object{"text": tc.mode}, key, 202, true)
			if again["generation_id"] != g["generation_id"] {
				t.Fatal("idempotency lost")
			}
			messages := array(call(t, "GET", path, nil, "", 200, true)["items"])
			want := 1
			if tc.assistant {
				want = 2
			}
			if len(messages) != want {
				t.Fatalf("published unexpected assistant: %v", messages)
			}
			if tc.assistant && len(array(obj(obj(messages[1])["response"])["evidence"])) != 1 {
				t.Fatal("answer lost exact evidence")
			}
		})
		if t.Failed() {
			return
		}
	}
	exec(`UPDATE context_access_grants SET revoked_at=now() WHERE user_id=$1`, testUserID)
	call(t, "GET", "/v0.1/insights/"+iid, nil, "", 403, true)
	exec(`UPDATE context_access_grants SET revoked_at=NULL WHERE user_id=$1`, testUserID)
	if !bytes.Equal(raw(card), raw(call(t, "GET", "/v0.1/insights/"+iid, nil, "", 200, true))) {
		t.Fatal("original card changed")
	}
	r, _ := http.NewRequest("GET", mlURL+"/test/stats", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var stats map[string]int
	if err = json.NewDecoder(res.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"requests": 5, "generation": 5, "guard": 5, "verifier": 2, "search": 0, "get_chunks": 0, "search_path": 0} {
		if stats[name] != want {
			t.Fatalf("%s=%d expected %d; stats=%v", name, stats[name], want, stats)
		}
	}
	t.Logf("cross-process counters: %v", stats)
}
