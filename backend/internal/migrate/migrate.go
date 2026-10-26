package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/alfa-hackaton-team/burmalda/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type migration struct {
	version  int64
	name     string
	checksum string
	sql      string
}

func Run(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	files, err := readMigrations()
	if err != nil {
		return 0, err
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(741738452987)"); err != nil {
		return 0, fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version bigint PRIMARY KEY,
		name text NOT NULL,
		checksum text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	rows, err := tx.Query(ctx, "SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return 0, fmt.Errorf("read schema_migrations: %w", err)
	}
	applied := make(map[int64]string)
	for rows.Next() {
		var version int64
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	rows.Close()

	known := make(map[int64]bool, len(files))
	for _, file := range files {
		known[file.version] = true
		if checksum, ok := applied[file.version]; ok && checksum != file.checksum {
			return 0, fmt.Errorf("migration %s changed after application", file.name)
		}
	}
	var latestApplied int64
	for version := range applied {
		if !known[version] {
			return 0, fmt.Errorf("applied migration %d is missing from this build", version)
		}
		if version > latestApplied {
			latestApplied = version
		}
	}

	count := 0
	for _, file := range files {
		if _, ok := applied[file.version]; ok {
			continue
		}
		if file.version < latestApplied && !latePrecomputeMigration(file) {
			return 0, fmt.Errorf("migration %s would run out of order", file.name)
		}
		if _, err := tx.Exec(ctx, file.sql); err != nil {
			return 0, fmt.Errorf("apply migration %s: %w", file.name, err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
			file.version, file.name, file.checksum,
		); err != nil {
			return 0, fmt.Errorf("record migration %s: %w", file.name, err)
		}
		count++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit migrations: %w", err)
	}
	return count, nil
}

func readMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	files := make([]migration, 0, len(entries))
	seen := make(map[int64]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		name := entry.Name()
		parts := strings.SplitN(name, "_", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == ".sql" {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || version <= 0 || seen[version] {
			return nil, fmt.Errorf("invalid or duplicate migration version in %q", name)
		}
		seen[version] = true
		body, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		if len(body) == 0 {
			return nil, fmt.Errorf("migration %s is empty", name)
		}
		hash := sha256.Sum256(body)
		files = append(files, migration{
			version:  version,
			name:     name,
			checksum: hex.EncodeToString(hash[:]),
			sql:      string(body),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files, nil
}

// PR #35 adds these independent, additive migrations after main shipped 26/27.
// Keep their original identities for existing demo databases. Only these exact
// audited SQL contents may fill that gap; all other out-of-order migrations fail.
func latePrecomputeMigration(file migration) bool {
	switch file.version {
	case 24:
		return file.name == "000024_precomputed_insights.sql" && file.checksum == "15c43a74753985a5d02a76e7edf63e432a8a56395b4df0916be08174ca563143"
	case 25:
		return file.name == "000025_insight_grounding_diagnostics.sql" && file.checksum == "2cd1da6ceeebf7e3ff7f48203af4f6c66eca693af28927800fefd969b9195de6"
	default:
		return false
	}
}
