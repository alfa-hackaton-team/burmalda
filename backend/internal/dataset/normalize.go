package dataset

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const normalizationPolicyVersion = 1

func normalized(ctx context.Context, db *pgx.Conn, id string) (bool, error) {
	var version int
	err := db.QueryRow(ctx, `SELECT policy_version FROM dataset_normalization_runs WHERE bundle_id=$1`, id).Scan(&version)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return version == normalizationPolicyVersion, err
}

func normalize(ctx context.Context, tx pgx.Tx, id string) error {
	for _, table := range []string{
		"dataset_profile_duplicate_rows", "dataset_payment_duplicate_rows", "dataset_person_link_duplicate_rows",
		"dataset_profile_conflicts", "dataset_person_link_conflicts",
	} {
		if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE bundle_id=$1", id); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	statements := []string{
		`INSERT INTO dataset_profile_conflicts (bundle_id, synthetic_u, row_count, variant_count)
		 SELECT bundle_id, synthetic_u, count(*), count(DISTINCT to_jsonb(p) - ARRAY['bundle_id', 'row_no'])
		 FROM dataset_company_profiles p WHERE bundle_id=$1
		 GROUP BY bundle_id, synthetic_u
		 HAVING count(DISTINCT to_jsonb(p) - ARRAY['bundle_id', 'row_no']) > 1`,
		`INSERT INTO dataset_profile_duplicate_rows (bundle_id, row_no, canonical_row_no)
		 SELECT bundle_id, row_no, canonical_row_no FROM (
		   SELECT p.bundle_id, p.row_no,
		          first_value(p.row_no) OVER w AS canonical_row_no,
		          row_number() OVER w AS rank
		   FROM dataset_company_profiles p
		   WHERE p.bundle_id=$1 AND NOT EXISTS (
		     SELECT 1 FROM dataset_profile_conflicts c
		     WHERE c.bundle_id=p.bundle_id AND c.synthetic_u=p.synthetic_u
		   )
		   WINDOW w AS (PARTITION BY p.bundle_id, p.synthetic_u ORDER BY p.row_no)
		 ) ranked WHERE rank > 1`,
		`INSERT INTO dataset_payment_duplicate_rows (bundle_id, row_no, canonical_row_no)
		 SELECT bundle_id, row_no, canonical_row_no FROM (
		   SELECT p.bundle_id, p.row_no,
		          first_value(p.row_no) OVER w AS canonical_row_no,
		          row_number() OVER w AS rank
		   FROM dataset_payments p WHERE p.bundle_id=$1
		   WINDOW w AS (PARTITION BY p.bundle_id, p.beneficiary, p.payer, p.pay_amt, p.pay_cnt ORDER BY p.row_no)
		 ) ranked WHERE rank > 1`,
		`INSERT INTO dataset_person_link_conflicts (bundle_id, synthetic_f, synthetic_u, row_count, variant_count)
		 SELECT bundle_id, synthetic_f, synthetic_u, count(*), count(DISTINCT (city_name, rc_name))
		 FROM dataset_person_company_links WHERE bundle_id=$1
		 GROUP BY bundle_id, synthetic_f, synthetic_u
		 HAVING count(DISTINCT (city_name, rc_name)) > 1`,
		`INSERT INTO dataset_person_link_duplicate_rows (bundle_id, row_no, canonical_row_no)
		 SELECT bundle_id, row_no, canonical_row_no FROM (
		   SELECT l.bundle_id, l.row_no,
		          first_value(l.row_no) OVER w AS canonical_row_no,
		          row_number() OVER w AS rank
		   FROM dataset_person_company_links l
		   WHERE l.bundle_id=$1 AND NOT EXISTS (
		     SELECT 1 FROM dataset_person_link_conflicts c
		     WHERE c.bundle_id=l.bundle_id AND c.synthetic_f=l.synthetic_f AND c.synthetic_u=l.synthetic_u
		   )
		   WINDOW w AS (PARTITION BY l.bundle_id, l.synthetic_f, l.synthetic_u ORDER BY l.row_no)
		 ) ranked WHERE rank > 1`,
		`INSERT INTO dataset_normalization_runs
		 (bundle_id, policy_version, normalized_at, profile_conflict_keys, profile_duplicate_rows,
		  payment_duplicate_rows, person_link_conflict_pairs, person_link_duplicate_rows)
		 VALUES ($1, $2, now(),
		   (SELECT count(*) FROM dataset_profile_conflicts WHERE bundle_id=$1),
		   (SELECT count(*) FROM dataset_profile_duplicate_rows WHERE bundle_id=$1),
		   (SELECT count(*) FROM dataset_payment_duplicate_rows WHERE bundle_id=$1),
		   (SELECT count(*) FROM dataset_person_link_conflicts WHERE bundle_id=$1),
		   (SELECT count(*) FROM dataset_person_link_duplicate_rows WHERE bundle_id=$1))
		 ON CONFLICT (bundle_id) DO UPDATE SET
		   policy_version=EXCLUDED.policy_version, normalized_at=EXCLUDED.normalized_at,
		   profile_conflict_keys=EXCLUDED.profile_conflict_keys,
		   profile_duplicate_rows=EXCLUDED.profile_duplicate_rows,
		   payment_duplicate_rows=EXCLUDED.payment_duplicate_rows,
		   person_link_conflict_pairs=EXCLUDED.person_link_conflict_pairs,
		   person_link_duplicate_rows=EXCLUDED.person_link_duplicate_rows`,
	}
	for i, statement := range statements {
		args := []any{id}
		if i == len(statements)-1 {
			args = append(args, normalizationPolicyVersion)
		}
		if _, err := tx.Exec(ctx, statement, args...); err != nil {
			return fmt.Errorf("normalize dataset: %w", err)
		}
	}
	return nil
}
