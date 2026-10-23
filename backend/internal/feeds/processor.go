package feeds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
	"github.com/jackc/pgx/v5"
)

type Processor struct{ Fetcher Fetcher }

func (p Processor) Poll(ctx context.Context, lease polling.Lease) (polling.Outcome, error) {
	if p.Fetcher == nil {
		return polling.Outcome{}, fmt.Errorf("feed fetcher is not configured")
	}
	response, err := p.Fetcher.Fetch(ctx, lease)
	result := polling.Outcome{RetryAfter: response.RetryAfter}
	if response.Status != 0 {
		result.HTTPStatus = &response.Status
	}
	if err != nil {
		return result, err
	}
	switch response.Status {
	case http.StatusNotModified:
		if lease.HTTPETag == nil && lease.LastModified == nil {
			return result, fmt.Errorf("unexpected 304 without stored HTTP validators")
		}
		if len(response.ETag) > 1024 || len(response.LastModified) > 1024 {
			return result, fmt.Errorf("source HTTP validator is too large")
		}
		result.Success = true
		if response.ETag != "" || response.LastModified != "" {
			result.Persist = func(ctx context.Context, tx pgx.Tx) (int, int, error) {
				_, err := tx.Exec(ctx, `UPDATE sources SET
					http_etag=COALESCE($2::text, http_etag),
					http_last_modified=COALESCE($3::text, http_last_modified)
					WHERE id=$1::uuid`, lease.SourceID,
					nullIfEmpty(response.ETag), nullIfEmpty(response.LastModified))
				return 0, 0, err
			}
		}
		return result, nil
	case http.StatusOK:
	default:
		return result, fmt.Errorf("source returned HTTP %d", response.Status)
	}
	entries, err := Parse(response.Body, lease.Endpoint, lease.AdapterType)
	if err != nil {
		return result, err
	}
	if len(response.ETag) > 1024 || len(response.LastModified) > 1024 {
		return result, fmt.Errorf("source HTTP validator is too large")
	}
	result.Success = true
	result.DiscoveredCount = len(entries)
	fetchedAt := time.Now().UTC()
	parserVersion := lease.AdapterType + "-" + ParserVersion
	result.Persist = func(ctx context.Context, tx pgx.Tx) (int, int, error) {
		artifactID, err := saveFeedSnapshot(ctx, tx, lease.SourceID, lease.AdapterType, response.Body)
		if err != nil {
			return 0, 0, err
		}
		_, err = tx.Exec(ctx, `UPDATE sources SET http_etag=$2, http_last_modified=$3
			WHERE id=$1::uuid`, lease.SourceID,
			nullIfEmpty(response.ETag), nullIfEmpty(response.LastModified))
		if err != nil {
			return 0, 0, fmt.Errorf("save feed HTTP validators: %w", err)
		}
		var newArticles, newVersions int
		for _, entry := range entries {
			createdArticle, createdVersion, err := saveEntry(ctx, tx, lease.SourceID, artifactID, fetchedAt, parserVersion, entry)
			if err != nil {
				return 0, 0, err
			}
			if createdArticle {
				newArticles++
			}
			if createdVersion {
				newVersions++
			}
		}
		return newArticles, newVersions, nil
	}
	return result, nil
}

func saveFeedSnapshot(ctx context.Context, tx pgx.Tx, sourceID, adapterType string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	contentType := "application/rss+xml"
	if adapterType == "atom" {
		contentType = "application/atom+xml"
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO raw_artifacts
		(source_id, sha256, content_type, size_bytes, content)
		VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (source_id, sha256) DO NOTHING RETURNING id::text`,
		sourceID, hash, contentType, len(data), data).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id::text FROM raw_artifacts
			WHERE source_id=$1::uuid AND sha256=$2`, sourceID, hash).Scan(&id)
	}
	if err != nil {
		return "", fmt.Errorf("save raw feed snapshot: %w", err)
	}
	return id, nil
}

func saveEntry(ctx context.Context, tx pgx.Tx, sourceID, artifactID string, fetchedAt time.Time, parserVersion string, entry Entry) (bool, bool, error) {
	kind, key := entry.Identity()
	var articleID string
	err := tx.QueryRow(ctx, `INSERT INTO articles
		(source_id, identity_kind, identity_key, external_id, canonical_url)
		VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (source_id, identity_kind, identity_key) DO NOTHING
		RETURNING id::text`, sourceID, kind, key,
		nullIfEmpty(entry.ExternalID), nullIfEmpty(entry.CanonicalURL)).Scan(&articleID)
	createdArticle := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, false, fmt.Errorf("create article: %w", err)
	}
	var latestID *string
	err = tx.QueryRow(ctx, `SELECT id::text, latest_version_id::text FROM articles
		WHERE source_id=$1::uuid AND identity_kind=$2 AND identity_key=$3
		FOR UPDATE`, sourceID, kind, key).Scan(&articleID, &latestID)
	if err != nil {
		return false, false, fmt.Errorf("lock article: %w", err)
	}
	versionNo := 1
	contentHash := entry.ContentHash()
	if latestID != nil {
		var latestNo int
		var latestHash, latestParser string
		err = tx.QueryRow(ctx, `SELECT version_no, content_hash, parser_version
			FROM article_versions WHERE id=$1::uuid`, *latestID).Scan(
			&latestNo, &latestHash, &latestParser)
		if err != nil {
			return false, false, fmt.Errorf("read latest article version: %w", err)
		}
		if latestHash == contentHash && latestParser == parserVersion {
			_, err = tx.Exec(ctx, `UPDATE articles SET external_id=$2, canonical_url=$3
				WHERE id=$1::uuid`, articleID, nullIfEmpty(entry.ExternalID),
				nullIfEmpty(entry.CanonicalURL))
			if err != nil {
				return false, false, fmt.Errorf("update article metadata: %w", err)
			}
			return createdArticle, false, nil
		}
		versionNo = latestNo + 1
	}
	var versionID string
	err = tx.QueryRow(ctx, `INSERT INTO article_versions
		(source_id, article_id, version_no, content_hash, parser_version,
		 title, body_text, published_at, fetched_at, raw_artifact_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10::uuid)
		RETURNING id::text`, sourceID, articleID, versionNo, contentHash,
		parserVersion, entry.Title, entry.Body, entry.PublishedAt,
		fetchedAt, artifactID).Scan(&versionID)
	if err != nil {
		return false, false, fmt.Errorf("create article version: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE articles SET latest_version_id=$2::uuid,
		external_id=$3, canonical_url=$4 WHERE id=$1::uuid`, articleID, versionID,
		nullIfEmpty(entry.ExternalID), nullIfEmpty(entry.CanonicalURL))
	if err != nil {
		return false, false, fmt.Errorf("set latest article version: %w", err)
	}
	return createdArticle, true, nil
}

func nullIfEmpty(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
