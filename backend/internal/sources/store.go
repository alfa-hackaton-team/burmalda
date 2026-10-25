package sources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sourceColumns = `id::text, name, source_type, source_role, trust_level,
	adapter_type, endpoint, enabled, polling_interval_seconds, credential_ref,
	adapter_config, config_version, health, last_sync_at, last_success_at,
	last_error, created_at`

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Store struct {
	db        *pgxpool.Pool
	validator Validator
}

func NewStore(db *pgxpool.Pool, validator Validator) *Store {
	return &Store{db: db, validator: validator}
}

func (s *Store) Create(ctx context.Context, cfg Config) (Source, error) {
	if err := s.validator.Validate(ctx, &cfg); err != nil {
		return Source{}, err
	}
	row := s.db.QueryRow(ctx, `INSERT INTO sources
		(name, source_type, source_role, trust_level, adapter_type, endpoint,
		 enabled, polling_interval_seconds, credential_ref, adapter_config)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)
		RETURNING `+sourceColumns,
		cfg.Name, cfg.SourceType, cfg.SourceRole, cfg.TrustLevel, cfg.AdapterType,
		cfg.Endpoint, cfg.Enabled, cfg.PollingIntervalSeconds, cfg.CredentialRef,
		string(cfg.AdapterConfig))
	source, _, err := scanSource(row)
	if err != nil {
		return Source{}, fmt.Errorf("create source: %w", err)
	}
	return source, nil
}

func (s *Store) Get(ctx context.Context, id string) (Source, error) {
	if !uuidPattern.MatchString(id) {
		return Source{}, fmt.Errorf("%w: invalid source_id", ErrInvalid)
	}
	row := s.db.QueryRow(ctx, `SELECT `+sourceColumns+` FROM sources WHERE id = $1::uuid`, id)
	source, _, err := scanSource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	if err != nil {
		return Source{}, fmt.Errorf("get source: %w", err)
	}
	return source, nil
}

func (s *Store) Update(ctx context.Context, id string, expectedVersion int64, patch []byte) (Source, error) {
	if !uuidPattern.MatchString(id) || expectedVersion < 1 {
		return Source{}, fmt.Errorf("%w: invalid source_id or config_version", ErrInvalid)
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return Source{}, err
	}
	version, err := current.Version()
	if err != nil {
		return Source{}, fmt.Errorf("parse stored config_version: %w", err)
	}
	if version != expectedVersion {
		return Source{}, ErrConflict
	}
	cfg, err := ApplyPatch(current.Config, patch)
	if err != nil {
		return Source{}, err
	}
	if err := s.validator.validate(ctx, &cfg, current.Config.Endpoint != cfg.Endpoint); err != nil {
		return Source{}, err
	}
	resetHTTP := current.Config.Endpoint != cfg.Endpoint || current.Config.AdapterType != cfg.AdapterType || string(current.Config.AdapterConfig) != string(cfg.AdapterConfig)
	row := s.db.QueryRow(ctx, `UPDATE sources SET
		name=$3, source_type=$4, source_role=$5, trust_level=$6,
		adapter_type=$7, endpoint=$8, enabled=$9, polling_interval_seconds=$10,
		credential_ref=$11, adapter_config=$12::jsonb,
		config_version=config_version+1, updated_at=now(), next_poll_at=now(),
		lease_until=NULL, lease_token=NULL,
		http_etag=CASE WHEN $13::boolean THEN NULL ELSE http_etag END,
		http_last_modified=CASE WHEN $13::boolean THEN NULL ELSE http_last_modified END,
		source_cursor=CASE WHEN $13::boolean THEN NULL ELSE source_cursor END,
		health=CASE WHEN $13::boolean THEN 'UNKNOWN' ELSE health END
		WHERE id=$1::uuid AND config_version=$2
		RETURNING `+sourceColumns,
		id, expectedVersion, cfg.Name, cfg.SourceType, cfg.SourceRole, cfg.TrustLevel,
		cfg.AdapterType, cfg.Endpoint, cfg.Enabled, cfg.PollingIntervalSeconds,
		cfg.CredentialRef, string(cfg.AdapterConfig), resetHTTP)
	updated, _, err := scanSource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrConflict
	}
	if err != nil {
		return Source{}, fmt.Errorf("update source: %w", err)
	}
	return updated, nil
}

type cursorValue struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"id"`
}

func (s *Store) List(ctx context.Context, limit int, cursor string) (Page, error) {
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 {
		return Page{}, fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalid)
	}
	var after cursorValue
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(data, &after) != nil || after.CreatedAt.IsZero() || !uuidPattern.MatchString(after.ID) {
			return Page{}, fmt.Errorf("%w: invalid cursor", ErrInvalid)
		}
	}
	query := `SELECT ` + sourceColumns + ` FROM sources`
	var args []any
	if cursor != "" {
		query += ` WHERE (created_at, id) < ($1::timestamptz, $2::uuid)`
		args = append(args, after.CreatedAt, after.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()
	page := Page{Items: make([]Source, 0, limit)}
	var last cursorValue
	for rows.Next() {
		source, createdAt, err := scanSource(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan sources: %w", err)
		}
		if len(page.Items) == limit {
			data, err := json.Marshal(last)
			if err != nil {
				return Page{}, fmt.Errorf("encode cursor: %w", err)
			}
			encoded := base64.RawURLEncoding.EncodeToString(data)
			page.NextCursor = &encoded
			break
		}
		page.Items = append(page.Items, source)
		last = cursorValue{CreatedAt: createdAt, ID: source.SourceID}
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("iterate sources: %w", err)
	}
	return page, nil
}

type scanner interface{ Scan(...any) error }

func scanSource(row scanner) (Source, time.Time, error) {
	var source Source
	var version int64
	var configJSON []byte
	var createdAt time.Time
	err := row.Scan(&source.SourceID, &source.Config.Name, &source.Config.SourceType,
		&source.Config.SourceRole, &source.Config.TrustLevel, &source.Config.AdapterType,
		&source.Config.Endpoint, &source.Config.Enabled, &source.Config.PollingIntervalSeconds,
		&source.Config.CredentialRef, &configJSON, &version, &source.Health,
		&source.LastSyncAt, &source.LastSuccessAt, &source.LastError, &createdAt)
	if err != nil {
		return Source{}, time.Time{}, err
	}
	source.Config.AdapterConfig = json.RawMessage(configJSON)
	source.ConfigVersion = strconv.FormatInt(version, 10)
	return source, createdAt, nil
}
