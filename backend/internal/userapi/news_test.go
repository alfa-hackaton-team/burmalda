package userapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRecentNewsUsesStoredLatestExternalVersions(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	insert := func(role, title, url string) string {
		t.Helper()
		sourceID, articleID, artifactID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO sources(id,name,source_type,source_role,trust_level,adapter_type,endpoint,polling_interval_seconds)
			  VALUES($1,'Test source','financial_press',$2,'reviewed','rss',$3,3600)`, []any{sourceID, role, url}},
			{`INSERT INTO raw_artifacts(id,source_id,sha256,content_type,size_bytes,content)
			  VALUES($1,$2,repeat('a',64),'text/html',4,'body'::bytea)`, []any{artifactID, sourceID}},
			{`INSERT INTO articles(id,source_id,identity_kind,identity_key,canonical_url)
			  VALUES($1,$2,'canonical_url',$3,$3)`, []any{articleID, sourceID, url}},
			{`INSERT INTO article_versions(id,source_id,article_id,version_no,content_hash,parser_version,title,body_text,fetched_at,raw_artifact_id)
			  VALUES($1,$2,$3,1,repeat('b',64),'test-v1',$4,$5,now(),$6)`, []any{versionID, sourceID, articleID, title, "First line.\nSecond line.", artifactID}},
			{`UPDATE articles SET latest_version_id=$2 WHERE id=$1`, []any{articleID, versionID}},
		} {
			if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		return articleID
	}
	externalID := insert("external", "Реальная статья", "https://publisher.example/news")
	insert("bank", "Банковский отчёт", "https://bank.example/report")
	privateID := insert("external", "Частная статья", "https://private.example/report")
	if _, err := pool.Exec(ctx, `UPDATE sources SET credential_ref='private-feed' WHERE id=(SELECT source_id FROM articles WHERE id=$1)`, privateID); err != nil {
		t.Fatal(err)
	}
	handler := authenticatedHandler(NewPostgresStore(pool))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/news", ""))
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", res.Code, res.Header().Get("Cache-Control"), res.Body.String())
	}
	var response struct {
		Items []newsItem `json:"items"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].ArticleID != externalID ||
		response.Items[0].Summary != "First line. Second line." || response.Items[0].TextOrigin != "unknown" ||
		response.Items[0].URL == nil || *response.Items[0].URL != "https://publisher.example/news" {
		t.Fatalf("news=%+v", response.Items)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, authenticatedRequest(http.MethodGet, "/v0.1/news?unexpected=1", ""))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unexpected query status=%d", res.Code)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v0.1/news", nil))
	if res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), "UNAUTHENTICATED") {
		t.Fatalf("anonymous status=%d body=%s", res.Code, res.Body.String())
	}
}
