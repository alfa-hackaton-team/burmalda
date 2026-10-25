package mljobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/google/uuid"
)

type EnqueueOptions struct {
	PipelineVersion string
	SourceID        string
	VersionID       string
	After           string
	Limit           int
	MaxTextBytes    int
	AllVersions     bool
	DryRun          bool
}

type EnqueueEntry struct {
	VersionID      string `json:"article_version_id"`
	TextBytes      int    `json:"text_bytes"`
	Outcome        string `json:"outcome"`
	SnapshotOrigin string `json:"source_snapshot_origin"`
}

type EnqueueReport struct {
	Selected  int            `json:"selected"`
	Queued    int            `json:"queued"`
	Existing  int            `json:"existing"`
	TooLarge  int            `json:"too_large"`
	DryRun    bool           `json:"dry_run"`
	NextAfter string         `json:"next_after,omitempty"`
	Entries   []EnqueueEntry `json:"entries"`
}

func (s *Store) Enqueue(ctx context.Context, o EnqueueOptions) (EnqueueReport, error) {
	report := EnqueueReport{DryRun: o.DryRun, Entries: []EnqueueEntry{}}
	if strings.TrimSpace(o.PipelineVersion) == "" || o.MaxTextBytes < 1 || o.MaxTextBytes > config.MLTextByteLimit || o.Limit < 1 || o.Limit > 10000 {
		return report, fmt.Errorf("pipeline version, positive text limit and batch limit 1..10000 are required")
	}
	for _, id := range []string{o.SourceID, o.VersionID, o.After} {
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return report, fmt.Errorf("invalid UUID filter")
			}
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return report, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT v.id::text,octet_length(v.body_text),
 COALESCE(ss.source_name,history.source_name,src.name),COALESCE(ss.source_type,history.source_type,src.source_type),
 COALESCE(ss.source_role,history.source_role,src.source_role),COALESCE(ss.source_trust_level,history.source_trust_level,src.trust_level),
 COALESCE(ss.source_config_version,history.source_config_version,src.config_version),
 COALESCE(j.source_snapshot_origin,ss.capture_origin,history.source_snapshot_origin,'CAPTURED_AT_ENQUEUE'),
 CASE WHEN ss.article_version_id IS NOT NULL THEN ss.external_id WHEN history.id IS NOT NULL THEN history.external_id ELSE a.external_id END,
 CASE WHEN ss.article_version_id IS NOT NULL THEN ss.canonical_url WHEN history.id IS NOT NULL THEN history.canonical_url ELSE a.canonical_url END,
 j.id IS NOT NULL
 FROM article_versions v JOIN articles a ON a.id=v.article_id JOIN sources src ON src.id=v.source_id
 LEFT JOIN article_version_source_snapshots ss ON ss.article_version_id=v.id
 LEFT JOIN processing_jobs j ON j.article_version_id=v.id AND j.pipeline_version=$1
 LEFT JOIN LATERAL (SELECT old.* FROM processing_jobs old WHERE old.article_version_id=v.id
 AND old.source_snapshot_origin<>'UNVERIFIED_LEGACY' ORDER BY old.created_at,old.id LIMIT 1) history ON true
 WHERE ($2::uuid IS NULL OR v.source_id=$2::uuid)
 AND ($3::uuid IS NULL OR v.id=$3::uuid)
 AND ($4::uuid IS NULL OR v.id>$4::uuid)
 AND ($5::boolean OR $3::uuid IS NOT NULL OR a.latest_version_id=v.id)
 ORDER BY v.id LIMIT $6`, o.PipelineVersion, nullIfEmpty(o.SourceID), nullIfEmpty(o.VersionID), nullIfEmpty(o.After), o.AllVersions, o.Limit)
	if err != nil {
		return report, err
	}
	type candidate struct {
		entry                   EnqueueEntry
		name, kind, role, trust string
		config                  int64
		external, url           *string
		exists                  bool
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.entry.VersionID, &c.entry.TextBytes, &c.name, &c.kind, &c.role, &c.trust, &c.config, &c.entry.SnapshotOrigin, &c.external, &c.url, &c.exists); err != nil {
			rows.Close()
			return report, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	for _, c := range candidates {
		report.Selected++
		report.NextAfter = c.entry.VersionID
		switch {
		case c.exists && c.entry.SnapshotOrigin == "UNVERIFIED_LEGACY":
			c.entry.Outcome = "SOURCE_SNAPSHOT_UNVERIFIED"
			report.Existing++
		case c.exists:
			c.entry.Outcome = "EXISTING"
			report.Existing++
		case c.entry.TextBytes > o.MaxTextBytes:
			c.entry.Outcome = "PAYLOAD_TOO_LARGE"
			report.TooLarge++
		case o.DryRun:
			c.entry.Outcome = "WOULD_QUEUE"
		default:
			tag, err := tx.Exec(ctx, `INSERT INTO processing_jobs
 (article_version_id,dedupe_key,pipeline_version,source_name,source_type,source_role,
 source_trust_level,source_config_version,external_id,canonical_url,source_snapshot_origin)
 VALUES ($1::uuid,$1::text||':'||$2,$2,$3,$4,$5,$6,$7,$8,$9,$10)
 ON CONFLICT (article_version_id,pipeline_version) DO NOTHING`, c.entry.VersionID, o.PipelineVersion, c.name, c.kind, c.role, c.trust, c.config, c.external, c.url, c.entry.SnapshotOrigin)
			if err != nil {
				return report, err
			}
			if tag.RowsAffected() == 1 {
				report.Queued++
				c.entry.Outcome = "QUEUED"
			} else {
				report.Existing++
				c.entry.Outcome = "EXISTING"
			}
		}
		report.Entries = append(report.Entries, c.entry)
	}
	if err = tx.Commit(ctx); err != nil {
		return report, err
	}
	return report, nil
}

type LegacyAudit struct {
	Total     int64            `json:"total"`
	Jobs      []map[string]any `json:"jobs"`
	Truncated bool             `json:"truncated"`
}

func (s *Store) AuditLegacy(ctx context.Context) (LegacyAudit, error) {
	audit := LegacyAudit{Jobs: []map[string]any{}}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM processing_jobs WHERE source_snapshot_origin='UNVERIFIED_LEGACY'`).Scan(&audit.Total); err != nil {
		return audit, err
	}
	audit.Truncated = audit.Total > 1000
	rows, err := s.db.Query(ctx, `SELECT id::text,article_version_id::text,pipeline_version,status,ml_processing_id
 FROM processing_jobs WHERE source_snapshot_origin='UNVERIFIED_LEGACY' ORDER BY created_at,id LIMIT 1000`)
	if err != nil {
		return audit, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, version, pipeline, status string
		var operation *string
		if err := rows.Scan(&id, &version, &pipeline, &status, &operation); err != nil {
			return audit, err
		}
		result = append(result, map[string]any{"job_id": id, "article_version_id": version, "pipeline_version": pipeline, "status": status, "processing_id": operation})
	}
	audit.Jobs = result
	return audit, rows.Err()
}
