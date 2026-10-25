package observability

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func observabilityDB(t *testing.T) *pgxpool.Pool {
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
	schema := "metrics_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 1
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if _, err := migrate.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPipelineUsesPersistedJobsAndSeparatesSuccessFromFailure(t *testing.T) {
	db := observabilityDB(t)
	ctx := context.Background()
	source, article, raw, version := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sources(id,name,source_type,source_role,trust_level,adapter_type,endpoint,enabled,polling_interval_seconds) VALUES($1,'test','news','external','standard','rss','https://example.com',false,60)`, []any{source}},
		{`INSERT INTO raw_artifacts(id,source_id,sha256,content_type,size_bytes,content) VALUES($1,$2,repeat('a',64),'text/plain',1,'x')`, []any{raw, source}},
		{`INSERT INTO articles(id,source_id,identity_kind,identity_key) VALUES($1,$2,'external_id','original')`, []any{article, source}},
		{`INSERT INTO article_versions(id,source_id,article_id,version_no,content_hash,parser_version,title,body_text,fetched_at,raw_artifact_id) VALUES($1,$2,$3,1,repeat('a',64),'test','title','body',now()-interval '10 minutes',$4)`, []any{version, source, article, raw}},
	} {
		if _, err := db.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, status := range []string{"SUCCEEDED", "FAILED", "QUEUED"} {
		if _, err := db.Exec(ctx, `INSERT INTO processing_jobs(article_version_id,dedupe_key,pipeline_version,status,source_name,source_type,source_role,source_trust_level,source_config_version,created_at,completed_at,last_error_code,document_version_id,event_ids)
 VALUES($1::uuid,$2,$2,$3,'test','news','external','standard',1,now()-interval '2 minutes',CASE WHEN $3='QUEUED' THEN NULL ELSE now()-interval '10 seconds' END,CASE WHEN $3='FAILED' THEN 'not-a-bounded-code' ELSE NULL END,$1::uuid::text,'[]'::jsonb)`, version, uuid.NewString(), status); err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
	}
	collector := NewPipelineCollector(db)
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	for range 2 {
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]float64{}
		for _, family := range families {
			for _, metric := range family.Metric {
				labels := map[string]string{}
				for _, l := range metric.Label {
					labels[l.GetName()] = l.GetValue()
				}
				value := metric.Gauge.GetValue()
				switch family.GetName() {
				case "ingestion_queue_jobs_last_5m":
					if labels["queue"] == "ml" {
						counts[labels["event"]] = value
					}
				case "ingestion_ml_failures_last_hour":
					if labels["code"] != "OTHER" {
						t.Fatalf("unbounded code: %v", labels)
					}
					counts["failure_code"] = value
				case "ingestion_document_processing_seconds":
					if labels["stage"] == "ingest_to_search" && (labels["status"] != "SUCCEEDED" || value < 585 || value > 605) {
						t.Fatalf("invalid end-to-end duration: %v %v", labels, value)
					}
					if labels["stage"] == "queue_to_terminal" && (value < 105 || value > 125) {
						t.Fatalf("queue duration ignored timestamps: %v", value)
					}
				case "ingestion_pipeline_collection_up":
					if value != 1 {
						t.Fatal("collection failed")
					}
				}
			}
		}
		if counts["arrived"] != 3 || counts["succeeded"] != 1 || counts["failed"] != 1 || counts["failure_code"] != 1 {
			t.Fatalf("polling counted as new work or failure hidden: %v", counts)
		}
	}
}

func TestPoolShowsExhaustionAndCanceledAcquire(t *testing.T) {
	db := observabilityDB(t)
	ctx := context.Background()
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	waitCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	if extra, err := db.Acquire(waitCtx); err == nil {
		extra.Release()
		t.Fatal("pool should be exhausted")
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(NewPoolCollector(db))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() == "ingestion_postgres_pool_canceled_acquires_total" {
			found = true
			if family.Metric[0].Counter.GetValue() < 1 {
				t.Fatal("cancellation not counted")
			}
		}
		if family.GetName() == "ingestion_postgres_pool_connections" {
			for _, m := range family.Metric {
				for _, l := range m.Label {
					if l.GetValue() == "acquired" && m.Gauge.GetValue() != 1 {
						t.Fatal("busy connection missing")
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("pool metrics missing")
	}
}
