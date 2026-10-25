package dataset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBankReportsReuseVersionsAndQueueNewPipeline(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "bank_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = migrate.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../feeds/testdata/article.pdf")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	body := "\n\n=== Page 1 ===\nSample PDF article text"
	if _, err = db.Exec(ctx, `INSERT INTO dataset_bundles(id,manifest_sha256)VALUES('test-bundle',$1)`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO active_dataset_bundle(singleton,bundle_id)VALUES(true,'test-bundle')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO dataset_bank_reports(bundle_id,name,sha256,size_bytes,content,body_text,text_status)VALUES('test-bundle','report.pdf',$1,$2,$3,$4,'OCR')`, hash, len(raw), raw, body); err != nil {
		t.Fatal(err)
	}
	for _, pipeline := range []string{"", "test-pipeline", "test-pipeline", "new-pipeline"} {
		count, err := EnsureBankReportDocuments(ctx, db, pipeline, 131072)
		if err != nil || count != 1 {
			t.Fatalf("bridge %s %d %v", pipeline, count, err)
		}
	}
	var versions, jobs, pages int
	if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM article_versions),(SELECT count(*) FROM processing_jobs),(SELECT count(*) FROM dataset_bank_report_pages)`).Scan(&versions, &jobs, &pages); err != nil || versions != 1 || jobs != 2 || pages != 1 {
		t.Fatalf("duplicate reports: versions=%d jobs=%d pages=%d err=%v", versions, jobs, pages, err)
	}
	var origin, method, role, external string
	if err = db.QueryRow(ctx, `SELECT v.text_origin,v.extraction_method,j.source_role,j.external_id FROM article_versions v JOIN processing_jobs j ON j.article_version_id=v.id LIMIT 1`).Scan(&origin, &method, &role, &external); err != nil || origin != "pdf_extracted" || method != "tesseract-ocr" || role != "bank" || external != "report.pdf" {
		t.Fatalf("incorrect report provenance %s %s %s %s %v", origin, method, role, external, err)
	}
	var originalArticle string
	if err = db.QueryRow(ctx, `SELECT id::text FROM articles`).Scan(&originalArticle); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO dataset_bundles(id,manifest_sha256)VALUES('renamed-bundle',$1)`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO dataset_bank_reports(bundle_id,name,sha256,size_bytes,content,body_text,text_status)
		SELECT 'renamed-bundle','renamed.pdf',sha256,size_bytes,content,body_text,text_status FROM dataset_bank_reports WHERE bundle_id='test-bundle'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE active_dataset_bundle SET bundle_id='renamed-bundle'`); err != nil {
		t.Fatal(err)
	}
	if _, err = EnsureBankReportDocuments(ctx, db, "test-pipeline", 131072); err != nil {
		t.Fatal(err)
	}
	var renamedArticle string
	if err = db.QueryRow(ctx, `SELECT id::text FROM articles WHERE external_id='renamed.pdf'`).Scan(&renamedArticle); err != nil || renamedArticle != originalArticle {
		t.Fatalf("rename created another article %s %s %v", renamedArticle, originalArticle, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM article_versions`).Scan(&versions); err != nil || versions != 1 {
		t.Fatal("rename created a new version")
	}
	if _, err = db.Exec(ctx, `UPDATE dataset_bank_reports SET text_status='PROVIDED',body_text='[Скриншот 9.1]'||chr(10)||'Предоставленный текст',text_issues='["PAGE_COUNT_MISMATCH"]' WHERE bundle_id='renamed-bundle'`); err != nil {
		t.Fatal(err)
	}
	if _, err = EnsureBankReportDocuments(ctx, db, "new-pipeline", 131072); err != nil {
		t.Fatal(err)
	}
	var issueCount int
	if err = db.QueryRow(ctx, `SELECT v.extraction_method,jsonb_array_length(v.extraction_issues) FROM articles a JOIN article_versions v ON v.id=a.latest_version_id WHERE a.id=$1::uuid`, originalArticle).Scan(&method, &issueCount); err != nil || method != "provided-text" || issueCount != 1 {
		t.Fatalf("provided text provenance %s %d %v", method, issueCount, err)
	}
	if _, err = EnsureBankReportDocuments(ctx, db, "another-pipeline", 1); err == nil {
		t.Fatal("oversized bank report queued")
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM processing_jobs`).Scan(&jobs); err != nil || jobs != 3 {
		t.Fatal("oversized report changed queue")
	}
}
