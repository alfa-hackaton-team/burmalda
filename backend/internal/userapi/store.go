package userapi

import (
	"context"
	"fmt"
	"strconv"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/rawartifacts"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool            *pgxpool.Pool
	corpus          *pgxpool.Pool
	ml              MLClient
	analysisVersion string
	Settings        func() config.Runtime
	ReadOriginal    func(context.Context, rawartifacts.Artifact) ([]byte, error)
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) ListContexts(ctx context.Context, userID, afterID string, limit int) ([]AvailableContext, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT context.id::text,
		       context.subject_type,
		       COALESCE(context.company_id::text, ''),
		       COALESCE(context.group_id::text, ''),
		       CASE context.subject_type
		           WHEN 'company' THEN COALESCE(NULLIF(company.raw_profile ->> 'name', ''), company.synthetic_uid)
		           ELSE COALESCE(company_group.name, company_group.external_id, context.group_id::text)
		       END,
		       context.role_code,
		       profile.id::text,
		       profile.version
		FROM context_access_grants AS grant_record
		JOIN business_contexts AS context
		  ON context.role_code = grant_record.role_code
		 AND (
		      (grant_record.company_id IS NOT NULL AND context.company_id = grant_record.company_id)
		      OR (grant_record.group_id IS NOT NULL AND context.group_id = grant_record.group_id)
		 )
		JOIN roles AS role ON role.code = context.role_code AND role.enabled
		JOIN business_profiles AS profile
		  ON profile.id = context.profile_id
		 AND profile.subject_type = context.subject_type
		 AND profile.company_id IS NOT DISTINCT FROM context.company_id
		 AND profile.group_id IS NOT DISTINCT FROM context.group_id
		LEFT JOIN companies AS company ON company.id = context.company_id
		LEFT JOIN company_groups AS company_group ON company_group.id = context.group_id
		WHERE grant_record.user_id = $1::uuid
 AND profile.status IN ('active','draft')
		  AND grant_record.revoked_at IS NULL
		  AND (grant_record.expires_at IS NULL OR grant_record.expires_at > now())
		  AND context.id > COALESCE(NULLIF($2, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY context.id
		LIMIT $3
	`, userID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list contexts: %w", err)
	}
	defer rows.Close()

	items := make([]AvailableContext, 0, limit)
	for rows.Next() {
		var item AvailableContext
		var subjectType, companyID, groupID, profileID string
		var profileVersion int
		if err := rows.Scan(
			&item.Context.ContextID,
			&subjectType,
			&companyID,
			&groupID,
			&item.DisplayName,
			&item.RoleID,
			&profileID,
			&profileVersion,
		); err != nil {
			return nil, fmt.Errorf("scan context: %w", err)
		}
		if subjectType == "company" {
			item.Context.Kind = "COMPANY"
			item.Context.CompanyID = companyID
		} else {
			item.Context.Kind = "GROUP"
			item.Context.GroupID = groupID
		}
		item.Profile = &ProfileRef{ProfileID: profileID, Version: strconv.Itoa(profileVersion)}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate contexts: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) settings() config.Runtime {
	if s.Settings != nil {
		return s.Settings()
	}
	return config.DefaultRuntime()
}

func (s *PostgresStore) SetCorpusPool(pool *pgxpool.Pool) { s.corpus = pool }
func (s *PostgresStore) corpusPool() *pgxpool.Pool {
	if s.corpus != nil {
		return s.corpus
	}
	return s.pool
}
