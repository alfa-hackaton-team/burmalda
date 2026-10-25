package dataset

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNormalizeKeepsDistinctPaymentsAndQuarantinesConflicts(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := migrate.Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	id := "normalization-test-" + uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO dataset_bundles (id, manifest_sha256) VALUES ($1,$2)`, id, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		number            int
		company, industry string
	}{
		{1, "U_A", "10"}, {2, "U_A", "10"},
		{3, "U_B", "20"}, {4, "U_B", "30"}, {5, "U_C", "40"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO dataset_company_profiles
			(bundle_id,row_no,synthetic_u,mainokved_code) VALUES ($1,$2,$3,$4)`, id, row.number, row.company, row.industry); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		number, amount int
		payer          string
	}{
		{1, 10, "U_A"}, {2, 10, "U_A"}, {3, 20, "U_A"}, {4, 30, "U_B"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO dataset_payments
			(bundle_id,row_no,beneficiary,payer,pay_amt,pay_cnt) VALUES ($1,$2,'U_C',$3,$4,1)`, id, row.number, row.payer, row.amount); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		number       int
		person, city string
	}{
		{1, "F_A", "Москва"}, {2, "F_A", "Москва"},
		{3, "F_B", "Москва"}, {4, "F_B", "Казань"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO dataset_person_company_links
			(bundle_id,row_no,synthetic_f,city_name,synthetic_u) VALUES ($1,$2,$3,$4,'U_C')`, id, row.number, row.person, row.city); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := normalize(ctx, tx, id); err != nil {
			t.Fatal(err)
		}
	}
	var profileConflicts, profileDuplicates, paymentDuplicates, linkConflicts, linkDuplicates int
	if err := tx.QueryRow(ctx, `SELECT profile_conflict_keys,profile_duplicate_rows,payment_duplicate_rows,
		person_link_conflict_pairs,person_link_duplicate_rows FROM dataset_normalization_runs WHERE bundle_id=$1`, id).Scan(
		&profileConflicts, &profileDuplicates, &paymentDuplicates, &linkConflicts, &linkDuplicates); err != nil {
		t.Fatal(err)
	}
	if profileConflicts != 1 || profileDuplicates != 1 || paymentDuplicates != 1 || linkConflicts != 1 || linkDuplicates != 1 {
		t.Fatalf("unexpected normalization counts: %d %d %d %d %d", profileConflicts, profileDuplicates, paymentDuplicates, linkConflicts, linkDuplicates)
	}
	for _, table := range []struct {
		name string
		want int
	}{
		{"dataset_company_profiles_clean", 2},
		{"dataset_payments_clean", 3},
		{"dataset_person_company_links_clean", 1},
	} {
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table.name+" WHERE bundle_id=$1", id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != table.want {
			t.Errorf("%s: got %d, want %d", table.name, count, table.want)
		}
	}
}
