package mljobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/alfa-hackaton-team/burmalda/backend/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func emptyIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "ml_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return db
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := emptyIntegrationPool(t)
	if _, err := migrate.Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func savedVersion(t *testing.T, db *pgxpool.Pool, body string, snapshot bool) (string, string) {
	t.Helper()
	ctx := context.Background()
	source, article, raw, version := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO sources(id,name,source_type,source_role,trust_level,adapter_type,endpoint,enabled,polling_interval_seconds) VALUES($1,'Publisher','news','external','reviewed','rss','https://example.com/feed',false,60)`, []any{source}},
		{`INSERT INTO raw_artifacts(id,source_id,sha256,content_type,size_bytes,content) VALUES($1,$2,$3,'text/plain',$4,$5)`, []any{raw, source, hash, len(body), []byte(body)}},
		{`INSERT INTO articles(id,source_id,identity_kind,identity_key,external_id) VALUES($1,$2,'external_id','original-id','original-id')`, []any{article, source}},
		{`INSERT INTO article_versions(id,source_id,article_id,version_no,content_hash,parser_version,title,body_text,fetched_at,raw_artifact_id) VALUES($1,$2,$3,1,$4,'test-v1','',$5,now(),$6)`, []any{version, source, article, hash, body, raw}},
		{`UPDATE articles SET latest_version_id=$2 WHERE id=$1`, []any{article, version}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot {
		if _, err := db.Exec(ctx, `INSERT INTO article_version_source_snapshots(article_version_id,source_name,source_type,source_role,source_trust_level,source_config_version) VALUES($1,'Publisher','news','external','reviewed',1)`, version); err != nil {
			t.Fatal(err)
		}
	}
	return source, version
}

func queueVersion(t *testing.T, s *Store, version string) {
	t.Helper()
	r, err := s.Enqueue(context.Background(), EnqueueOptions{PipelineVersion: "test-pipeline", VersionID: version, Limit: 10, MaxTextBytes: 131072})
	if err != nil || r.Queued != 1 {
		t.Fatalf("enqueue %+v %v", r, err)
	}
}
func claimJob(t *testing.T, s *Store) Job {
	t.Helper()
	job, ok, err := s.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	return job
}
func makeDue(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `UPDATE processing_jobs SET next_attempt_at=now()-interval '1 second' WHERE status='RETRY'`); err != nil {
		t.Fatal(err)
	}
}
func operationBody(job Job, state string, extra string) string {
	return fmt.Sprintf(`{"processing_id":"operation-1","article_version_id":%q,"pipeline_version":%q,"trace_id":%q,"created_at":"2026-10-24T12:00:00Z","state":%q%s}`, job.VersionID, job.PipelineVersion, job.ID, state, extra)
}

func TestEnqueueSavedVersionsIsIdempotent(t *testing.T) {
	db := integrationPool(t)
	ctx := context.Background()
	s := NewStore(db)
	source, version := savedVersion(t, db, "Текст статьи", true)
	if _, err := db.Exec(ctx, `UPDATE sources SET name='Changed',config_version=2 WHERE id=$1`, source); err != nil {
		t.Fatal(err)
	}
	o := EnqueueOptions{PipelineVersion: "test-pipeline", SourceID: source, Limit: 10, MaxTextBytes: 131072, DryRun: true}
	r, err := s.Enqueue(ctx, o)
	if err != nil || r.Queued != 0 || r.Selected != 1 || r.Entries[0].Outcome != "WOULD_QUEUE" {
		t.Fatalf("dry run %+v %v", r, err)
	}
	o.DryRun = false
	r, err = s.Enqueue(ctx, o)
	if err != nil || r.Queued != 1 {
		t.Fatalf("queue %+v %v", r, err)
	}
	r, err = s.Enqueue(ctx, o)
	if err != nil || r.Existing != 1 || r.Queued != 0 {
		t.Fatalf("repeat %+v %v", r, err)
	}
	job := claimJob(t, s)
	if job.SourceName != "Publisher" || job.SourceConfigVersion != 1 || job.VersionID != version || job.Title != "" {
		t.Fatalf("snapshot changed %+v", job)
	}
	o.PipelineVersion = "new-pipeline"
	r, err = s.Enqueue(ctx, o)
	if err != nil || r.Queued != 1 {
		t.Fatalf("new pipeline %+v %v", r, err)
	}
	var versions int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM article_versions`).Scan(&versions); err != nil || versions != 1 {
		t.Fatal("enqueue created a version")
	}
	legacySource, legacy := savedVersion(t, db, "Old text", false)
	queueVersion(t, s, legacy)
	var origin string
	if err = db.QueryRow(ctx, `SELECT source_snapshot_origin FROM processing_jobs WHERE article_version_id=$1`, legacy).Scan(&origin); err != nil || origin != "CAPTURED_AT_ENQUEUE" {
		t.Fatalf("legacy origin %s %v", origin, err)
	}
	if _, err := db.Exec(ctx, `UPDATE sources SET name='Changed legacy source',config_version=2 WHERE id=$1`, legacySource); err != nil {
		t.Fatal(err)
	}
	backfill, err := s.Enqueue(ctx, EnqueueOptions{VersionID: legacy, PipelineVersion: "new-legacy-pipeline", Limit: 10, MaxTextBytes: 131072})
	if err != nil || backfill.Queued != 1 {
		t.Fatalf("legacy reprocessing %+v %v", backfill, err)
	}
	var preservedName string
	if err := db.QueryRow(ctx, `SELECT source_name FROM processing_jobs WHERE article_version_id=$1 AND pipeline_version='new-legacy-pipeline'`, legacy).Scan(&preservedName); err != nil || preservedName != "Publisher" {
		t.Fatalf("saved source snapshot replaced %s %v", preservedName, err)
	}
	_, large := savedVersion(t, db, strings.Repeat("я", 65536)+"a", false)
	o.VersionID = large
	o.SourceID = ""
	r, err = s.Enqueue(ctx, o)
	if err != nil || r.TooLarge != 1 || r.Queued != 0 {
		t.Fatalf("large %+v %v", r, err)
	}
}

func TestLostResponseAndMissingGETRecoverSameOperationThroughDB(t *testing.T) {
	db := integrationPool(t)
	s := NewStore(db)
	ctx := context.Background()
	_, version := savedVersion(t, db, "Saved body", true)
	queueVersion(t, s, version)
	posts, gets := 0, 0
	firstKey := ""
	var original []byte
	var current Job
	client := Client{BaseURL: "http://ml.example", HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			posts++
			body, _ := io.ReadAll(req.Body)
			key := req.Header.Get("Idempotency-Key")
			if posts == 1 {
				original = body
				firstKey = key
				return nil, fmt.Errorf("response lost after ML accepted operation")
			}
			if key != firstKey || string(body) != string(original) {
				t.Error("retry changed original request or key")
			}
			return response(202, operationBody(current, "RUNNING", ""), nil), nil
		}
		gets++
		if gets == 1 {
			return response(404, "", nil), nil
		}
		return response(200, operationBody(current, "SUCCEEDED", `,"document_version_id":"ml-document-1","event_ids":[]`), nil), nil
	})}}
	for attempt := 0; attempt < 4; attempt++ {
		current = claimJob(t, s)
		result := client.Process(ctx, current)
		status, err := s.Finish(ctx, current, result)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			if status != "RETRY" {
				t.Fatalf("unexpected intermediate status %s %+v", status, result)
			}
			makeDue(t, db)
		} else if status != "SUCCEEDED" {
			t.Fatalf("result %s %+v", status, result)
		}
	}
	if posts != 3 || gets != 2 {
		t.Fatalf("calls: posts=%d gets=%d", posts, gets)
	}
	var operation, document string
	var events json.RawMessage
	if err := db.QueryRow(ctx, `SELECT ml_processing_id,document_version_id,event_ids FROM processing_jobs`).Scan(&operation, &document, &events); err != nil {
		t.Fatal(err)
	}
	if operation != "operation-1" || document != "ml-document-1" || string(events) != "[]" {
		t.Fatalf("wrong stored success %s %s %s", operation, document, events)
	}
	if _, ok, err := s.Claim(ctx); err != nil || ok {
		t.Fatal("successful operation was claimed again")
	}
}

func TestQualityRejectionAndProtocolFailureRemainTerminal(t *testing.T) {
	for _, mode := range []string{"quality", "invalid-response", "http-422"} {
		t.Run(mode, func(t *testing.T) {
			db := integrationPool(t)
			s := NewStore(db)
			ctx := context.Background()
			_, version := savedVersion(t, db, "Saved body", true)
			queueVersion(t, s, version)
			job := claimJob(t, s)
			requests := 0
			client := Client{BaseURL: "http://ml.example", HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				if mode == "http-422" {
					return response(422, `{"code":"VALIDATION_ERROR","message":"Invalid material","retryable":false}`, nil), nil
				}
				body := operationBody(job, "FAILED", `,"error":{"code":"INSUFFICIENT_EVIDENCE","message":"No usable text","retryable":true}`)
				if mode == "invalid-response" {
					body = `{"processing_id":"bad","state":"SUCCEEDED"}`
				}
				return response(202, body, nil), nil
			})}}
			result := client.Process(ctx, job)
			status, err := s.Finish(ctx, job, result)
			if err != nil || status != "FAILED" {
				t.Fatalf("finish %s %v", status, err)
			}
			var code, message string
			var retryable bool
			if err = db.QueryRow(ctx, `SELECT last_error_code,last_error,last_error_retryable FROM processing_jobs`).Scan(&code, &message, &retryable); err != nil {
				t.Fatal(err)
			}
			if mode == "quality" {
				if code != "INSUFFICIENT_EVIDENCE" || message != "No usable text" || !retryable {
					t.Fatalf("lost ML error %s %s %v", code, message, retryable)
				}
			} else if mode == "http-422" {
				if code != "VALIDATION_ERROR" || retryable {
					t.Fatalf("permanent HTTP error lost %s %v", code, retryable)
				}
			} else if code != "INVALID_ML_RESPONSE" {
				t.Fatalf("protocol code %s", code)
			}
			makeDue(t, db)
			again, err := s.Enqueue(ctx, EnqueueOptions{VersionID: version, PipelineVersion: "test-pipeline", Limit: 10, MaxTextBytes: 131072})
			if err != nil || again.Existing != 1 || again.Queued != 0 {
				t.Fatalf("terminal operation was replaced %+v %v", again, err)
			}
			if _, ok, err := s.Claim(ctx); err != nil || ok {
				t.Fatal("terminal failure was claimed again")
			}
			if requests != 1 {
				t.Fatal("repeated terminal POST")
			}
		})
	}
}

func TestContinuousRunningTimesOutInDB(t *testing.T) {
	db := integrationPool(t)
	s := NewStore(db)
	ctx := context.Background()
	settings := config.DefaultRuntime()
	settings.ML.MaxProcessingDuration = config.Duration(time.Minute)
	s.Settings = func() config.Runtime { return settings }
	_, version := savedVersion(t, db, "Saved body", true)
	queueVersion(t, s, version)
	job := claimJob(t, s)
	result := decodeOperation([]byte(operationBody(job, "RUNNING", "")), job)
	if _, err := s.Finish(ctx, job, result); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE processing_jobs SET processing_started_at=now()-interval '2 minutes',next_attempt_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	job = claimJob(t, s)
	result = decodeOperation([]byte(operationBody(job, "RUNNING", "")), job)
	status, err := s.Finish(ctx, job, result)
	if err != nil || status != "FAILED" {
		t.Fatalf("timeout %s %v", status, err)
	}
	var code string
	if err = db.QueryRow(ctx, `SELECT last_error_code FROM processing_jobs`).Scan(&code); err != nil || code != "PROCESSING_TIMEOUT" {
		t.Fatalf("timeout reason %s %v", code, err)
	}
}

func TestUnverifiedLegacyJobsAreNotSent(t *testing.T) {
	db := integrationPool(t)
	s := NewStore(db)
	ctx := context.Background()
	_, version := savedVersion(t, db, "Old body", true)
	queueVersion(t, s, version)
	if _, err := db.Exec(ctx, `UPDATE processing_jobs SET source_snapshot_origin='UNVERIFIED_LEGACY',ml_processing_id='already-accepted'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Claim(ctx); err != nil || ok {
		t.Fatal("unverified job was claimed")
	}
	audit, err := s.AuditLegacy(ctx)
	if err != nil || audit.Total != 1 || len(audit.Jobs) != 1 || audit.Jobs[0]["processing_id"].(*string) == nil || *audit.Jobs[0]["processing_id"].(*string) != "already-accepted" {
		t.Fatalf("audit %+v %v", audit, err)
	}
}

func TestMigrationQuarantinesOnlyPre009Jobs(t *testing.T) {
	db := emptyIntegrationPool(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `CREATE TABLE schema_migrations(version bigint PRIMARY KEY,name text NOT NULL,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	files, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".sql") {
			continue
		}
		version, err := strconv.Atoi(strings.SplitN(file.Name(), "_", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		if version >= 17 {
			continue
		}
		body, err := migrations.Files.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if _, err = db.Exec(ctx, `INSERT INTO schema_migrations(version,name,checksum)VALUES($1,$2,$3)`, version, file.Name(), hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
	}
	_, version := savedVersion(t, db, "Historical text", false)
	for _, pipeline := range []string{"before-009", "after-009"} {
		_, err = db.Exec(ctx, `INSERT INTO processing_jobs(article_version_id,dedupe_key,pipeline_version,source_name,source_type,source_role,source_trust_level,source_config_version,ml_processing_id,created_at)
 VALUES($1::uuid,$1::text||':'||$2,$2,'Publisher','news','external','reviewed',1,$2,
 (SELECT applied_at FROM schema_migrations WHERE version=9)+CASE WHEN $2='before-009' THEN -interval '1 minute' ELSE interval '1 minute' END)`, version, pipeline)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = migrate.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	var oldOrigin, newOrigin, operation, code string
	if err = db.QueryRow(ctx, `SELECT source_snapshot_origin,ml_processing_id,last_error_code FROM processing_jobs WHERE pipeline_version='before-009'`).Scan(&oldOrigin, &operation, &code); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT source_snapshot_origin FROM processing_jobs WHERE pipeline_version='after-009'`).Scan(&newOrigin); err != nil {
		t.Fatal(err)
	}
	if oldOrigin != "UNVERIFIED_LEGACY" || newOrigin != "CAPTURED_AT_INGEST" || operation != "before-009" || code != "SOURCE_SNAPSHOT_UNVERIFIED" {
		t.Fatalf("unsafe migration %s %s %s %s", oldOrigin, newOrigin, operation, code)
	}
}

func TestConcurrentEnqueueCreatesOneJob(t *testing.T) {
	db := integrationPool(t)
	s := NewStore(db)
	_, version := savedVersion(t, db, "Saved text", true)
	type outcome struct {
		report EnqueueReport
		err    error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			r, err := s.Enqueue(context.Background(), EnqueueOptions{PipelineVersion: "test-pipeline", VersionID: version, Limit: 10, MaxTextBytes: 131072})
			results <- outcome{r, err}
		}()
	}
	queued, existing := 0, 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		queued += result.report.Queued
		existing += result.report.Existing
	}
	if queued != 1 || existing != 1 {
		t.Fatalf("concurrent enqueue: queued=%d existing=%d", queued, existing)
	}
}

func TestExportMatchesActualClientRequestAndExcludesSecrets(t *testing.T) {
	db := integrationPool(t)
	s := NewStore(db)
	ctx := context.Background()
	source, version := savedVersion(t, db, "Сохранённый текст для проверки контракта.", true)
	queueVersion(t, s, version)
	if _, err := db.Exec(ctx, `UPDATE sources SET credential_ref='DO_NOT_EXPORT_SECRET',adapter_config='{"secret":"DO_NOT_EXPORT_SECRET"}' WHERE id=$1`, source); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir() + "/payloads"
	report, err := s.Export(ctx, ExportOptions{PipelineVersion: "test-pipeline", VersionID: version, Limit: 10, Directory: directory, MaxTextBytes: 131072})
	if err != nil || len(report) != 1 {
		t.Fatalf("export %+v %v", report, err)
	}
	payload, err := os.ReadFile(directory + "/" + version + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "DO_NOT_EXPORT_SECRET") {
		t.Fatal("source access configuration leaked")
	}
	job := claimJob(t, s)
	var captured []byte
	client := Client{BaseURL: "http://ml.example", HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured, _ = io.ReadAll(req.Body)
		return response(202, operationBody(job, "RUNNING", ""), nil), nil
	})}}
	result := client.Process(ctx, job)
	if !result.Pending || string(payload) != string(captured) {
		t.Fatalf("export differs from actual POST %+v", result)
	}
	if directory := os.Getenv("ML_HANDOFF_EDGE_DIR"); directory != "" {
		if err := os.Mkdir(directory, 0755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"empty-title-no-date.request.json", "repeat-same-version.request.json"} {
			if err := os.WriteFile(directory+"/"+name, captured, 0644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(directory+"/original.txt", []byte(job.Body), 0644); err != nil {
			t.Fatal(err)
		}
		job.Body = strings.Repeat("я", 65536) + "a"
		called := false
		guarded := Client{BaseURL: "http://ml.example", HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, fmt.Errorf("must not send") })}}
		refusal := guarded.Process(ctx, job)
		if called || refusal.ErrorCode != "PAYLOAD_TOO_LARGE" {
			t.Fatalf("oversized request sent %+v", refusal)
		}
		data, _ := json.MarshalIndent(map[string]any{"synthetic": true, "body_bytes": len(job.Body), "limit_bytes": 131072, "construction": "65536 Cyrillic я characters followed by ASCII a", "http_requests": 0, "error": map[string]any{"code": refusal.ErrorCode, "message": refusal.Error, "retryable": refusal.ErrorRetryable}}, "", "  ")
		if err := os.WriteFile(directory+"/size-rejection.json", data, 0644); err != nil {
			t.Fatal(err)
		}
		manifest, _ := json.MarshalIndent(map[string]any{"synthetic": true, "idempotency_key": job.DedupeKey, "source_id": job.SourceID, "article_id": job.ArticleID, "article_version_id": job.VersionID, "raw_artifact_id": job.RawArtifactID, "raw_sha256": job.ContentHash, "original": "original.txt", "note": "Generated by actual Go HTTP client against an isolated PostgreSQL test fixture; IDs are not references into the production database."}, "", "  ")
		if err := os.WriteFile(directory+"/manifest.json", manifest, 0644); err != nil {
			t.Fatal(err)
		}
	}
	markerReport, err := s.Export(ctx, ExportOptions{VersionID: version, Limit: 10, Directory: t.TempDir() + "/without-pipeline", MaxTextBytes: 131072})
	if err != nil || markerReport[0]["fixture_only"] != true {
		t.Fatalf("unconfirmed version was not marked %+v %v", markerReport, err)
	}
	_, oversized := savedVersion(t, db, strings.Repeat("😀", 32768)+"a", false)
	largeDirectory := t.TempDir() + "/oversized"
	largeReport, err := s.Export(ctx, ExportOptions{VersionID: oversized, Limit: 10, Directory: largeDirectory, MaxTextBytes: 131072})
	if err != nil || largeReport[0]["outcome"] != "PAYLOAD_TOO_LARGE" || largeReport[0]["text_bytes"] != 131073 {
		t.Fatalf("oversized export %+v %v", largeReport, err)
	}
	if _, err := os.Stat(largeDirectory + "/" + oversized + ".json"); !os.IsNotExist(err) {
		t.Fatal("oversized POST payload exported")
	}

}
