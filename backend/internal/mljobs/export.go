package mljobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/google/uuid"
)

type ExportOptions struct {
	PipelineVersion, SourceID, VersionID, Directory string
	Limit, MaxTextBytes                             int
}

func (s *Store) Export(ctx context.Context, o ExportOptions) ([]map[string]any, error) {
	if o.Directory == "" || o.Limit < 1 || o.Limit > 1000 || o.MaxTextBytes < 1 || o.MaxTextBytes > config.MLTextByteLimit {
		return nil, fmt.Errorf("export requires output directory, limit 1..1000 and positive text limit")
	}
	for _, id := range []string{o.SourceID, o.VersionID} {
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return nil, fmt.Errorf("invalid UUID filter")
			}
		}
	}
	fixtureOnly := o.PipelineVersion == ""
	if fixtureOnly {
		o.PipelineVersion = "UNCONFIRMED-FIXTURE-ONLY"
	}
	rows, err := s.db.Query(ctx, `SELECT v.id::text,v.article_id::text,v.source_id::text,v.title,CASE WHEN octet_length(v.body_text)<=$5 THEN v.body_text ELSE '' END,
 v.content_hash,v.raw_artifact_id::text,v.language,v.author,v.published_at,v.fetched_at,v.metadata,
 COALESCE(j.source_name,ss.source_name,history.source_name,src.name),COALESCE(j.source_type,ss.source_type,history.source_type,src.source_type),
 COALESCE(j.source_role,ss.source_role,history.source_role,src.source_role),COALESCE(j.source_trust_level,ss.source_trust_level,history.source_trust_level,src.trust_level),
 COALESCE(j.source_config_version,ss.source_config_version,history.source_config_version,src.config_version),
 CASE WHEN j.id IS NOT NULL THEN j.external_id WHEN ss.article_version_id IS NOT NULL THEN ss.external_id WHEN history.id IS NOT NULL THEN history.external_id ELSE a.external_id END,
 CASE WHEN j.id IS NOT NULL THEN j.canonical_url WHEN ss.article_version_id IS NOT NULL THEN ss.canonical_url WHEN history.id IS NOT NULL THEN history.canonical_url ELSE a.canonical_url END,
 COALESCE(j.source_snapshot_origin,ss.capture_origin,history.source_snapshot_origin,'CAPTURED_AT_ENQUEUE'),
 v.text_origin,v.extraction_method,v.extraction_issues,j.id::text,r.sha256,r.content_type,r.size_bytes,octet_length(v.body_text), CASE WHEN j.id IS NOT NULL THEN j.delivery_extraction ELSE jsonb_build_object('text_origin',v.text_origin,'extraction_issues',v.extraction_issues) END,
 CASE WHEN j.id IS NOT NULL THEN j.delivery_version_no ELSE v.version_no END
 FROM article_versions v JOIN raw_artifacts r ON r.id=v.raw_artifact_id JOIN articles a ON a.id=v.article_id JOIN sources src ON src.id=v.source_id
 LEFT JOIN article_version_source_snapshots ss ON ss.article_version_id=v.id
 LEFT JOIN processing_jobs j ON j.article_version_id=v.id AND j.pipeline_version=$1
 LEFT JOIN LATERAL (SELECT old.* FROM processing_jobs old WHERE old.article_version_id=v.id
 AND old.source_snapshot_origin<>'UNVERIFIED_LEGACY' ORDER BY old.created_at,old.id LIMIT 1) history ON true
 WHERE ($2::uuid IS NULL OR v.source_id=$2::uuid) AND ($3::uuid IS NULL OR v.id=$3::uuid)
 AND ($3::uuid IS NOT NULL OR a.latest_version_id=v.id) ORDER BY v.id LIMIT $4`,
		o.PipelineVersion, nullIfEmpty(o.SourceID), nullIfEmpty(o.VersionID), o.Limit, o.MaxTextBytes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type sample struct {
		textBytes                  int
		rawSHA, rawType            string
		rawSize                    int64
		job                        Job
		origin, textOrigin, method string
		issues                     json.RawMessage
	}
	var samples []sample
	for rows.Next() {
		var sample sample
		var id *string
		j := &sample.job
		if err = rows.Scan(&j.VersionID, &j.ArticleID, &j.SourceID, &j.Title, &j.Body, &j.ContentHash, &j.RawArtifactID,
			&j.Language, &j.Author, &j.PublishedAt, &j.FetchedAt, &j.Metadata, &j.SourceName, &j.SourceType, &j.SourceRole, &j.TrustLevel, &j.SourceConfigVersion,
			&j.ExternalID, &j.CanonicalURL, &sample.origin, &sample.textOrigin, &sample.method, &sample.issues, &id, &sample.rawSHA, &sample.rawType, &sample.rawSize, &sample.textBytes, &j.DeliveryExtraction, &j.VersionNo); err != nil {
			return nil, err
		}
		j.PipelineVersion = o.PipelineVersion
		j.DedupeKey = j.VersionID + ":" + o.PipelineVersion
		if id != nil {
			j.ID = *id
		} else {
			j.ID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("payload-export:"+j.DedupeKey)).String()
		}
		samples = append(samples, sample)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err = os.Mkdir(o.Directory, 0700); err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(o.Directory)
		}
	}()
	manifest := []map[string]any{}
	for _, sample := range samples {
		j := sample.job
		entry := map[string]any{"article_version_id": j.VersionID, "text_bytes": sample.textBytes, "source_snapshot_origin": sample.origin,
			"text_origin": sample.textOrigin, "extraction_method": sample.method, "extraction_issues": sample.issues,
			"fixture_only": fixtureOnly, "idempotency_key": j.DedupeKey,
			"raw_artifact_id": j.RawArtifactID, "raw_sha256": sample.rawSHA, "raw_content_type": sample.rawType, "raw_size_bytes": sample.rawSize, "source_id": j.SourceID, "article_id": j.ArticleID,
			"method": "POST", "path": "/internal/v0.1/article-processings"}
		switch {
		case sample.origin == "UNVERIFIED_LEGACY":
			entry["outcome"] = "SOURCE_SNAPSHOT_UNVERIFIED"
		case sample.textBytes > o.MaxTextBytes:
			entry["outcome"] = "PAYLOAD_TOO_LARGE"
			entry["limit_bytes"] = o.MaxTextBytes
		default:
			payload, err := RequestPayload(j)
			if err != nil {
				entry["outcome"] = "INVALID_ML_REQUEST"
				entry["error"] = err.Error()
				manifest = append(manifest, entry)
				continue
			}
			name := j.VersionID + ".json"
			if err = os.WriteFile(filepath.Join(o.Directory, name), payload, 0600); err != nil {
				return nil, err
			}
			entry["file"] = name
			entry["outcome"] = "EXPORTED"
		}
		manifest = append(manifest, entry)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(o.Directory, "manifest.json"), data, 0600); err != nil {
		return nil, err
	}
	complete = true
	return manifest, nil
}
