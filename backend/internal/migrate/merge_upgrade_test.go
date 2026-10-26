package migrate

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLatePrecomputeExceptionIsBoundToExactMigrations(t *testing.T) {
	files, err := readMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		want := file.version == 24 || file.version == 25
		if latePrecomputeMigration(file) != want {
			t.Fatalf("unexpected exception: %s", file.name)
		}
		file.checksum = "modified"
		if latePrecomputeMigration(file) {
			t.Fatal("modified SQL bypassed ordering")
		}
	}
}

func TestUpgradeFromMainAndPrecomputeBranches(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	files, err := readMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"main", "precompute"} {
		t.Run(branch, func(t *testing.T) {
			name := "merge_upgrade_" + uuid.NewString()[:8]
			if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)") }()
			cfg, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.Database = name
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			_, err = pool.Exec(ctx, `CREATE TABLE schema_migrations(version bigint PRIMARY KEY,name text NOT NULL,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				if branch == "main" && (file.version == 24 || file.version == 25) || branch == "precompute" && file.version > 25 {
					continue
				}
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, file.sql); err != nil {
					_ = tx.Rollback(ctx)
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version,name,checksum) VALUES($1,$2,$3)", file.version, file.name, file.checksum); err != nil {
					_ = tx.Rollback(ctx)
					t.Fatal(err)
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
			}
			count, err := Run(ctx, pool)
			if err != nil || count != 2 {
				t.Fatalf("upgrade: applied=%d err=%v", count, err)
			}
			count, err = Run(ctx, pool)
			if err != nil || count != 0 {
				t.Fatalf("replay: applied=%d err=%v", count, err)
			}
			var columns int
			err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='request_operations' AND column_name IN ('demo_set_version','analysis_diagnostics')`).Scan(&columns)
			if err != nil || columns != 2 {
				t.Fatalf("columns=%d err=%v", columns, err)
			}
			// Even the audited migrations remain protected against journal tampering.
			if _, err = pool.Exec(ctx, "UPDATE schema_migrations SET checksum='wrong' WHERE version=24"); err != nil {
				t.Fatal(err)
			}
			if _, err = Run(ctx, pool); err == nil {
				t.Fatal("checksum mismatch accepted")
			}
		})
	}
}
