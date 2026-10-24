package dataset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const bankSourceName = "Альфа-Банк: аналитические материалы"
const bankParserVersion = "dataset-ocr-v1"

var pageHeader = regexp.MustCompile(`(?m)^(?:=== Page ([1-9][0-9]*) ===|=== Страница [1-9][0-9]*\.([1-9][0-9]*) ===|\[Скриншот [1-9][0-9]*\.([1-9][0-9]*)\])\r?$`)

type pageSpan struct {
	Number, Start, End int
}

func reportPages(body string) ([]pageSpan, error) {
	matches := pageHeader.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("PDF text has no supported page markers")
	}
	pages := make([]pageSpan, 0, len(matches))
	for i, match := range matches {
		pageNumber := ""
		for group := 2; group < len(match); group += 2 {
			if match[group] >= 0 {
				pageNumber = body[match[group]:match[group+1]]
				break
			}
		}
		number, err := strconv.Atoi(pageNumber)
		if err != nil || number != i+1 {
			return nil, fmt.Errorf("PDF text page numbers are not consecutive")
		}
		end := len(body)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		pages = append(pages, pageSpan{Number: number, Start: match[0], End: end})
	}
	return pages, nil
}

func EnsureBankReportDocuments(ctx context.Context, db *pgxpool.Pool, pipelineVersion string, maxTextBytes int) (int, error) {
	if (pipelineVersion != "" && maxTextBytes == 0) || maxTextBytes < 0 {
		return 0, fmt.Errorf("pipeline version and text limit must be configured together")
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(748294679294)`); err != nil {
		return 0, err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(748294679294)`)
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var bundleID string
	if err := tx.QueryRow(ctx, `SELECT bundle_id FROM active_dataset_bundle WHERE singleton=true`).Scan(&bundleID); err != nil {
		return 0, fmt.Errorf("active dataset: %w", err)
	}
	sourceID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("alfa-hackathon-reference-pdfs"))
	_, err = tx.Exec(ctx, `INSERT INTO sources
		(id,name,source_type,source_role,trust_level,adapter_type,endpoint,enabled,polling_interval_seconds)
		VALUES ($1,$2,'bank_analytics','bank','primary','dataset','dataset://alfa-bank/hackathon',false,86400)
		ON CONFLICT (id) DO NOTHING`, sourceID, bankSourceName)
	if err != nil {
		return 0, fmt.Errorf("register bank source: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT name,sha256,size_bytes,content,object_uri,body_text,text_status,text_issues
		FROM dataset_bank_reports WHERE bundle_id=$1 ORDER BY name`, bundleID)
	if err != nil {
		return 0, err
	}
	type report struct {
		name, hash, body, status string
		issues                   json.RawMessage
		size                     int64
		content                  []byte
		objectURI                *string
	}
	var reports []report
	for rows.Next() {
		var r report
		if err := rows.Scan(&r.name, &r.hash, &r.size, &r.content, &r.objectURI, &r.body, &r.status, &r.issues); err != nil {
			rows.Close()
			return 0, err
		}
		reports = append(reports, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, r := range reports {
		if maxTextBytes > 0 && len(r.body) > maxTextBytes {
			return 0, fmt.Errorf("bank report %s exceeds ML text limit: %d > %d bytes", r.name, len(r.body), maxTextBytes)
		}
		parserVersion, method := bankParserVersion, "tesseract-ocr"
		switch r.status {
		case "PROVIDED":
			parserVersion, method = "dataset-provided-text-v1", "provided-text"
		case "EXTRACTED":
			parserVersion, method = "dataset-pdf-text-v1", "pdf-text"
		case "OCR":
		default:
			return 0, fmt.Errorf("report %s has no ready text", r.name)
		}
		pages, err := reportPages(r.body)
		if err != nil {
			return 0, fmt.Errorf("bank report %s: %w", r.name, err)
		}
		var artifactID string
		err = tx.QueryRow(ctx, `INSERT INTO raw_artifacts
			(source_id,sha256,content_type,size_bytes,content,object_uri)
			VALUES ($1,$2,'application/pdf',$3,$4,$5)
			ON CONFLICT (source_id,sha256) DO UPDATE SET sha256=EXCLUDED.sha256
			RETURNING id::text`, sourceID, r.hash, r.size, r.content, r.objectURI).Scan(&artifactID)
		if err != nil {
			return 0, fmt.Errorf("bank report artifact %s: %w", r.name, err)
		}
		var articleID string
		var latestID *string
		err = tx.QueryRow(ctx, `SELECT id::text,latest_version_id::text FROM articles
			WHERE source_id=$1 AND identity_kind='external_id' AND identity_key=$2`, sourceID, r.name).Scan(&articleID, &latestID)
		if errors.Is(err, pgx.ErrNoRows) {
			matches, queryErr := tx.Query(ctx, `SELECT DISTINCT a.id::text,a.latest_version_id::text FROM articles a
				JOIN article_versions v ON v.article_id=a.id WHERE a.source_id=$1 AND v.raw_artifact_id=$2::uuid LIMIT 2`, sourceID, artifactID)
			if queryErr != nil {
				return 0, queryErr
			}
			count := 0
			for matches.Next() {
				count++
				if err = matches.Scan(&articleID, &latestID); err != nil {
					matches.Close()
					return 0, err
				}
			}
			err = matches.Err()
			matches.Close()
			if err != nil {
				return 0, err
			}
			if count > 1 {
				return 0, fmt.Errorf("ambiguous bank report rename: %s", r.name)
			}
			if count == 1 {
				_, err = tx.Exec(ctx, `UPDATE articles SET identity_key=$2,external_id=$2 WHERE id=$1::uuid AND identity_kind='external_id'`, articleID, r.name)
			} else {
				err = tx.QueryRow(ctx, `INSERT INTO articles(source_id,identity_kind,identity_key,external_id)
					VALUES($1,'external_id',$2,$2) RETURNING id::text`, sourceID, r.name).Scan(&articleID)
			}
		}
		if err != nil {
			return 0, fmt.Errorf("bank report article %s: %w", r.name, err)
		}
		bodyHash := sha256.Sum256([]byte(r.body))
		contentHash := hex.EncodeToString(bodyHash[:])
		versionID := ""
		versionNo := 1
		if latestID != nil {
			var oldHash, oldParser, oldArtifactID string
			err = tx.QueryRow(ctx, `SELECT version_no,content_hash,parser_version,raw_artifact_id::text
				FROM article_versions WHERE id=$1::uuid`, *latestID).
				Scan(&versionNo, &oldHash, &oldParser, &oldArtifactID)
			if err != nil {
				return 0, err
			}
			if oldHash == contentHash && oldParser == parserVersion && oldArtifactID == artifactID {
				versionID = *latestID
			} else {
				versionNo++
			}
		}
		newVersion := versionID == ""
		if newVersion {
			err = tx.QueryRow(ctx, `INSERT INTO article_versions
				(source_id,article_id,version_no,content_hash,parser_version,title,body_text,fetched_at,raw_artifact_id,text_origin,extraction_method,extraction_issues)
				VALUES ($1,$2::uuid,$3,$4,$5,$6,$7,now(),$8::uuid,'pdf_extracted',$9,$10::jsonb) RETURNING id::text`,
				sourceID, articleID, versionNo, contentHash, parserVersion,
				strings.TrimSuffix(r.name, ".pdf"), r.body, artifactID, method, r.issues).Scan(&versionID)
			if err != nil {
				return 0, fmt.Errorf("bank report version %s: %w", r.name, err)
			}
			if _, err = tx.Exec(ctx, `UPDATE articles SET latest_version_id=$2::uuid WHERE id=$1::uuid`, articleID, versionID); err != nil {
				return 0, err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO dataset_bank_report_documents (bundle_id,name,article_version_id)
			VALUES ($1,$2,$3::uuid)
			ON CONFLICT (bundle_id,name) DO UPDATE SET article_version_id=EXCLUDED.article_version_id`, bundleID, r.name, versionID); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM dataset_bank_report_pages WHERE bundle_id=$1 AND name=$2`, bundleID, r.name); err != nil {
			return 0, err
		}
		for _, page := range pages {
			if _, err = tx.Exec(ctx, `INSERT INTO dataset_bank_report_pages
				(bundle_id,name,page_no,start_byte,end_byte) VALUES ($1,$2,$3,$4,$5)`,
				bundleID, r.name, page.Number, page.Start, page.End); err != nil {
				return 0, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE article_versions SET text_origin='pdf_extracted',extraction_method=$2,extraction_issues=$3::jsonb
			WHERE id=$1::uuid AND text_origin='unknown'`, versionID, method, r.issues); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO article_version_source_snapshots
			(article_version_id,source_name,source_type,source_role,source_trust_level,source_config_version,capture_origin,external_id)
			SELECT $1::uuid,name,source_type,source_role,trust_level,config_version,CASE WHEN $3::boolean THEN 'CAPTURED_AT_INGEST' ELSE 'CAPTURED_AT_ENQUEUE' END,$4 FROM sources WHERE id=$2
			ON CONFLICT DO NOTHING`, versionID, sourceID, newVersion, r.name); err != nil {
			return 0, err
		}
		if pipelineVersion != "" {
			_, err = tx.Exec(ctx, `INSERT INTO processing_jobs
				(article_version_id,dedupe_key,pipeline_version,source_name,source_type,source_role,
				 source_trust_level,source_config_version,external_id,source_snapshot_origin)
				SELECT $1::uuid,$1::text || ':' || $2,$2,source_name,source_type,source_role,source_trust_level,source_config_version,external_id,capture_origin
 FROM article_version_source_snapshots WHERE article_version_id=$1::uuid
				ON CONFLICT (article_version_id,pipeline_version) DO NOTHING`,
				versionID, pipelineVersion)
			if err != nil {
				return 0, fmt.Errorf("queue bank report %s: %w", r.name, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(reports), nil
}
