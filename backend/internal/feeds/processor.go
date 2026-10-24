package feeds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/rawartifacts"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
	"github.com/jackc/pgx/v5"
)

type Processor struct {
	Fetcher         Fetcher
	PipelineVersion string
	MaxTextBytes    int
	Limits          *config.Runtime
	RawStore        rawartifacts.Store
}

type ArticleFetcher interface {
	FetchArticle(context.Context, string, string) (Response, error)
}

type material struct {
	Entry
	Raw              []byte
	ContentType      string
	ParserVersion    string
	EntryNo          int
	Artifact         *rawartifacts.Artifact
	TextOrigin       string
	ExtractionMethod string
	ExtractionIssues []string
}

const MaxArticleEntries = 20

func (p Processor) Poll(ctx context.Context, lease polling.Lease) (polling.Outcome, error) {
	limits := config.DefaultRuntime()
	if p.Limits != nil {
		limits = *p.Limits
	}
	if p.Fetcher == nil {
		return polling.Outcome{}, fmt.Errorf("feed fetcher is not configured")
	}
	if (p.PipelineVersion != "" && p.MaxTextBytes == 0) || p.MaxTextBytes < 0 {
		return polling.Outcome{}, fmt.Errorf("ML pipeline version and text limit must be configured together")
	}
	adapterConfig, err := sources.ParseFeedAdapterConfig(lease.AdapterConfig)
	if err != nil {
		return polling.Outcome{}, err
	}
	fetchLease := lease
	if adapterConfig.ArticleHTML {
		fetchLease.HTTPETag, fetchLease.LastModified = nil, nil
	}
	response, err := p.Fetcher.Fetch(ctx, fetchLease)
	result := polling.Outcome{RetryAfter: response.RetryAfter}
	if response.Status != 0 {
		result.HTTPStatus = &response.Status
	}
	if err != nil {
		return result, err
	}
	switch response.Status {
	case http.StatusNotModified:
		if adapterConfig.ArticleHTML {
			return result, fmt.Errorf("unexpected 304 for HTML article source")
		}
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
	feedURL := response.FinalURL
	if feedURL == "" {
		feedURL = lease.Endpoint
	}
	entries, parseFailures, err := ParseEntriesWithLimit(response.Body, feedURL, lease.AdapterType, adapterConfig.ArticleHTML, limits.Feeds.MaxEntries)
	if err != nil {
		return result, err
	}
	materials := make([]material, 0, len(entries))
	entryFailures := make([]polling.EntryFailure, 0)
	for _, failure := range parseFailures {
		entryFailures = append(entryFailures, polling.EntryFailure{
			EntryNo: failure.EntryNo, ErrorCode: "FEED_ENTRY_INVALID", ErrorMessage: failure.Err.Error(),
		})
	}
	addFailure := func(index int, entry Entry, code string, err error, response Response, retryable, queue bool) {
		var status *int
		if response.Status != 0 {
			status = &response.Status
		}
		entryFailures = append(entryFailures, polling.EntryFailure{
			EntryNo: index + 1, ArticleURL: entry.CanonicalURL,
			ExternalID: entry.ExternalID, Title: entry.Title, PublishedAt: entry.PublishedAt,
			ErrorCode: code, ErrorMessage: err.Error(), HTTPStatus: status,
			RetryAfter: response.RetryAfter, Retryable: retryable, Queue: queue,
		})
	}
	if adapterConfig.ArticleHTML {
		articleFetcher, ok := p.Fetcher.(ArticleFetcher)
		if !ok {
			return result, fmt.Errorf("article HTML fetcher is not configured")
		}
		if len(entries) > limits.Feeds.MaxArticleEntries {
			return result, fmt.Errorf("HTML feed exceeds %d article links", limits.Feeds.MaxArticleEntries)
		}
		for _, parsed := range entries {
			entry, index := parsed.Entry, parsed.EntryNo-1
			if entry.CanonicalURL == "" {
				addFailure(index, entry, "ARTICLE_URL_MISSING", fmt.Errorf("feed entry has no article URL"), Response{}, false, false)
				continue
			}
			articleResponse, err := articleFetcher.FetchArticle(ctx, feedURL, entry.CanonicalURL)
			if err != nil {
				addFailure(index, entry, "ARTICLE_FETCH_FAILED", err, articleResponse,
					RetryableArticleFetch(articleResponse.Status, err), true)
				continue
			}
			mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(articleResponse.ContentType, ";", 2)[0]))
			var body, parserVersion string
			switch mediaType {
			case "text/html", "application/xhtml+xml":
				body, err = ExtractArticleHTMLWithOptions(articleResponse.Body, articleResponse.ContentType,
					adapterConfig.ArticleContainerID, adapterConfig.ArticleExcludeClasses)
				parserVersion = ArticleHTMLParserVersionForOptions(adapterConfig.ArticleContainerID,
					adapterConfig.ArticleExcludeClasses)
			case "application/pdf":
				body, err = ExtractArticlePDFWithLimits(articleResponse.Body, limits.Feeds.MaxPDFPages, limits.Feeds.MaxPDFTextBytes)
				parserVersion = ArticlePDFParserVersion
			default:
				err = fmt.Errorf("unsupported article media type")
			}
			if err != nil {
				addFailure(index, entry, "ARTICLE_EXTRACTION_FAILED", err, articleResponse, false, true)
				continue
			}
			if p.MaxTextBytes > 0 && len(body) > p.MaxTextBytes {
				addFailure(index, entry, "ARTICLE_TEXT_TOO_LARGE", fmt.Errorf("article text exceeds configured ML limit of %d bytes", p.MaxTextBytes), articleResponse, false, true)
				continue
			}
			entry.Body = body
			materials = append(materials, material{Entry: entry, EntryNo: parsed.EntryNo, Raw: articleResponse.Body,
				ContentType: articleResponse.ContentType, ParserVersion: parserVersion, TextOrigin: articleTextOrigin(mediaType), ExtractionMethod: parserVersion})
		}
	} else {
		parserVersion := lease.AdapterType + "-" + ParserVersion
		for _, parsed := range entries {
			entry, index := parsed.Entry, parsed.EntryNo-1
			if p.MaxTextBytes > 0 && len(entry.Body) > p.MaxTextBytes {
				addFailure(index, entry, "ARTICLE_TEXT_TOO_LARGE", fmt.Errorf("article text exceeds configured ML limit of %d bytes", p.MaxTextBytes), Response{}, false, false)
				continue
			}
			materials = append(materials, material{Entry: entry, ParserVersion: parserVersion, TextOrigin: "rss_summary", ExtractionMethod: parserVersion})
		}
	}
	if len(response.ETag) > 1024 || len(response.LastModified) > 1024 {
		return result, fmt.Errorf("source HTTP validator is too large")
	}
	store := p.RawStore
	if store == nil {
		store = rawartifacts.PostgreSQL{}
	}
	feedContentType := "application/rss+xml"
	if lease.AdapterType == "atom" {
		feedContentType = "application/atom+xml"
	}
	feedArtifact, err := store.Prepare(ctx, lease.SourceID, feedContentType, response.Body)
	if err != nil {
		return result, fmt.Errorf("store feed snapshot: %w", err)
	}
	prepared := materials[:0]
	for _, item := range materials {
		if item.Raw != nil {
			artifact, err := store.Prepare(ctx, lease.SourceID, item.ContentType, item.Raw)
			if err != nil {
				addFailure(item.EntryNo-1, item.Entry, "RAW_STORAGE_FAILED", err, Response{}, true, true)
				continue
			}
			item.Artifact = &artifact
		}
		prepared = append(prepared, item)
	}
	materials = prepared
	result.Success = len(entryFailures) == 0 || len(materials) > 0
	result.Partial = len(entryFailures) > 0 && len(materials) > 0
	result.EntryFailures = entryFailures
	if len(entryFailures) > 0 {
		result.Error = fmt.Sprintf("%d of %d feed entries failed", len(entryFailures), len(entries)+len(parseFailures))
	}
	result.DiscoveredCount = len(entries) + len(parseFailures)
	fetchedAt := time.Now().UTC()
	result.Persist = func(ctx context.Context, tx pgx.Tx) (int, int, error) {
		feedArtifactID, err := savePreparedArtifact(ctx, tx, lease.SourceID, feedArtifact)
		if err != nil {
			return 0, 0, err
		}
		if len(entryFailures) == 0 {
			_, err = tx.Exec(ctx, `UPDATE sources SET http_etag=$2, http_last_modified=$3
				WHERE id=$1::uuid`, lease.SourceID,
				nullIfEmpty(response.ETag), nullIfEmpty(response.LastModified))
			if err != nil {
				return 0, 0, fmt.Errorf("save feed HTTP validators: %w", err)
			}
		}
		var newArticles, newVersions int
		for _, item := range materials {
			createdArticle, createdVersion, err := saveEntry(ctx, tx, lease, feedArtifactID, fetchedAt, item.ParserVersion, item, p.PipelineVersion)
			if err != nil {
				return 0, 0, err
			}
			if createdArticle {
				newArticles++
			}
			if createdVersion {
				newVersions++
			}
			if adapterConfig.ArticleHTML {
				_, err = tx.Exec(ctx, `UPDATE article_fetch_jobs SET status='SUCCEEDED',
					lease_token=NULL, lease_until=NULL, completed_at=clock_timestamp(),
					last_error=NULL, last_error_code=NULL, last_http_status=NULL,
					updated_at=clock_timestamp()
					WHERE source_id=$1::uuid AND article_url=$2 AND status<>'SUCCEEDED'`,
					lease.SourceID, item.CanonicalURL)
				if err != nil {
					return 0, 0, fmt.Errorf("resolve article retry: %w", err)
				}
			}
		}
		return newArticles, newVersions, nil
	}
	return result, nil
}

func saveRawArtifact(ctx context.Context, tx pgx.Tx, sourceID, contentType string, data []byte) (string, error) {
	artifact, err := (rawartifacts.PostgreSQL{}).Prepare(ctx, sourceID, contentType, data)
	if err != nil {
		return "", err
	}
	return savePreparedArtifact(ctx, tx, sourceID, artifact)
}

func savePreparedArtifact(ctx context.Context, tx pgx.Tx, sourceID string, artifact rawartifacts.Artifact) (string, error) {
	var content any
	if artifact.ObjectURI == "" {
		content = artifact.Content
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO raw_artifacts
		(source_id, sha256, content_type, size_bytes, content, object_uri)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
		ON CONFLICT (source_id, sha256) DO NOTHING RETURNING id::text`,
		sourceID, artifact.SHA256, artifact.ContentType, artifact.Size, content, nullIfEmpty(artifact.ObjectURI)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id::text FROM raw_artifacts
			WHERE source_id=$1::uuid AND sha256=$2 AND size_bytes=$3`, sourceID, artifact.SHA256, artifact.Size).Scan(&id)
	}
	if err != nil {
		return "", fmt.Errorf("save raw feed snapshot: %w", err)
	}
	return id, nil
}

func saveEntry(ctx context.Context, tx pgx.Tx, lease polling.Lease, feedArtifactID string, fetchedAt time.Time, parserVersion string, item material, pipelineVersion string) (bool, bool, error) {
	entry := item.Entry
	sourceID := lease.SourceID
	kind, key := entry.Identity()
	var articleID string
	createdArticle := false
	var latestID *string
	rows, err := tx.Query(ctx, `SELECT id::text, latest_version_id::text FROM articles
		WHERE source_id=$1::uuid AND
		(($2::text IS NOT NULL AND external_id=$2) OR
		 ($3::text IS NOT NULL AND canonical_url=$3))
		FOR UPDATE`, sourceID, nullIfEmpty(entry.ExternalID), nullIfEmpty(entry.CanonicalURL))
	if err != nil {
		return false, false, fmt.Errorf("find article: %w", err)
	}
	matched := 0
	for rows.Next() {
		matched++
		if err := rows.Scan(&articleID, &latestID); err != nil {
			rows.Close()
			return false, false, fmt.Errorf("read article identity: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, false, fmt.Errorf("find article identity: %w", err)
	}
	rows.Close()
	if matched > 1 {
		return false, false, fmt.Errorf("article external ID and URL refer to different articles")
	}
	if matched == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO articles
			(source_id, identity_kind, identity_key, external_id, canonical_url)
			VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text`,
			sourceID, kind, key, nullIfEmpty(entry.ExternalID), nullIfEmpty(entry.CanonicalURL)).Scan(&articleID)
		if err != nil {
			return false, false, fmt.Errorf("create article: %w", err)
		}
		createdArticle = true
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
			_, err = tx.Exec(ctx, `UPDATE articles SET external_id=COALESCE($2, external_id),
					canonical_url=COALESCE($3, canonical_url)
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
	artifactID := feedArtifactID
	if item.Raw != nil {
		if item.Artifact == nil {
			return false, false, fmt.Errorf("article raw artifact was not prepared")
		}
		artifactID, err = savePreparedArtifact(ctx, tx, sourceID, *item.Artifact)
		if err != nil {
			return false, false, err
		}
	}
	origin, method := item.TextOrigin, item.ExtractionMethod
	if origin == "" {
		origin = "unknown"
	}
	if method == "" {
		method = "unknown"
	}
	issues := item.ExtractionIssues
	if issues == nil {
		issues = []string{}
	}
	issueJSON, err := json.Marshal(issues)
	if err != nil {
		return false, false, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO article_versions
		(source_id, article_id, version_no, content_hash, parser_version,
		 title, body_text, published_at, fetched_at, raw_artifact_id,text_origin,extraction_method,extraction_issues)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10::uuid,$11,$12,$13::jsonb)
		RETURNING id::text`, sourceID, articleID, versionNo, contentHash,
		parserVersion, entry.Title, entry.Body, entry.PublishedAt,
		fetchedAt, artifactID, origin, method, issueJSON).Scan(&versionID)
	if err != nil {
		return false, false, fmt.Errorf("create article version: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO article_version_source_snapshots
		(article_version_id,source_name,source_type,source_role,source_trust_level,source_config_version,external_id,canonical_url)
		VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8)`, versionID, lease.Name, lease.SourceType, lease.SourceRole, lease.TrustLevel, lease.ConfigVersion, nullIfEmpty(entry.ExternalID), nullIfEmpty(entry.CanonicalURL))
	if err != nil {
		return false, false, fmt.Errorf("save source snapshot: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE articles SET latest_version_id=$2::uuid,
		external_id=COALESCE($3, external_id), canonical_url=COALESCE($4, canonical_url)
		WHERE id=$1::uuid`, articleID, versionID,
		nullIfEmpty(entry.ExternalID), nullIfEmpty(entry.CanonicalURL))
	if err != nil {
		return false, false, fmt.Errorf("set latest article version: %w", err)
	}
	if pipelineVersion != "" {
		_, err = tx.Exec(ctx, `INSERT INTO processing_jobs
			(article_version_id, dedupe_key, pipeline_version, source_name,
			 source_type, source_role, source_trust_level, source_config_version,
			 external_id, canonical_url)
			VALUES ($1::uuid, $1::text || ':' || $2, $2, $3, $4, $5, $6, $7, $8, $9)`,
			versionID, pipelineVersion, lease.Name, lease.SourceType, lease.SourceRole,
			lease.TrustLevel, lease.ConfigVersion,
			nullIfEmpty(entry.ExternalID), nullIfEmpty(entry.CanonicalURL))
		if err != nil {
			return false, false, fmt.Errorf("queue article processing: %w", err)
		}
	}
	return createdArticle, true, nil
}

func nullIfEmpty(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func articleTextOrigin(mediaType string) string {
	if mediaType == "application/pdf" {
		return "pdf_extracted"
	}
	return "full_article"
}
