CREATE TABLE dataset_normalization_runs (
    bundle_id text PRIMARY KEY REFERENCES dataset_bundles (id) ON DELETE RESTRICT,
    policy_version integer NOT NULL CHECK (policy_version > 0),
    normalized_at timestamptz NOT NULL DEFAULT now(),
    profile_conflict_keys bigint NOT NULL CHECK (profile_conflict_keys >= 0),
    profile_duplicate_rows bigint NOT NULL CHECK (profile_duplicate_rows >= 0),
    payment_duplicate_rows bigint NOT NULL CHECK (payment_duplicate_rows >= 0),
    person_link_conflict_pairs bigint NOT NULL CHECK (person_link_conflict_pairs >= 0),
    person_link_duplicate_rows bigint NOT NULL CHECK (person_link_duplicate_rows >= 0)
);

CREATE TABLE dataset_profile_conflicts (
    bundle_id text NOT NULL REFERENCES dataset_bundles (id) ON DELETE RESTRICT,
    synthetic_u text NOT NULL,
    row_count bigint NOT NULL CHECK (row_count > 1),
    variant_count bigint NOT NULL CHECK (variant_count > 1),
    PRIMARY KEY (bundle_id, synthetic_u)
);

CREATE TABLE dataset_profile_duplicate_rows (
    bundle_id text NOT NULL,
    row_no bigint NOT NULL,
    canonical_row_no bigint NOT NULL,
    PRIMARY KEY (bundle_id, row_no),
    FOREIGN KEY (bundle_id, row_no) REFERENCES dataset_company_profiles (bundle_id, row_no) ON DELETE CASCADE,
    FOREIGN KEY (bundle_id, canonical_row_no) REFERENCES dataset_company_profiles (bundle_id, row_no) ON DELETE CASCADE
);

CREATE TABLE dataset_payment_duplicate_rows (
    bundle_id text NOT NULL,
    row_no bigint NOT NULL,
    canonical_row_no bigint NOT NULL,
    PRIMARY KEY (bundle_id, row_no),
    FOREIGN KEY (bundle_id, row_no) REFERENCES dataset_payments (bundle_id, row_no) ON DELETE CASCADE,
    FOREIGN KEY (bundle_id, canonical_row_no) REFERENCES dataset_payments (bundle_id, row_no) ON DELETE CASCADE
);

CREATE TABLE dataset_person_link_conflicts (
    bundle_id text NOT NULL REFERENCES dataset_bundles (id) ON DELETE RESTRICT,
    synthetic_f text NOT NULL,
    synthetic_u text NOT NULL,
    row_count bigint NOT NULL CHECK (row_count > 1),
    variant_count bigint NOT NULL CHECK (variant_count > 1),
    PRIMARY KEY (bundle_id, synthetic_f, synthetic_u)
);

CREATE TABLE dataset_person_link_duplicate_rows (
    bundle_id text NOT NULL,
    row_no bigint NOT NULL,
    canonical_row_no bigint NOT NULL,
    PRIMARY KEY (bundle_id, row_no),
    FOREIGN KEY (bundle_id, row_no) REFERENCES dataset_person_company_links (bundle_id, row_no) ON DELETE CASCADE,
    FOREIGN KEY (bundle_id, canonical_row_no) REFERENCES dataset_person_company_links (bundle_id, row_no) ON DELETE CASCADE
);

CREATE VIEW dataset_company_profiles_clean AS
SELECT p.* FROM dataset_company_profiles p
WHERE NOT EXISTS (
    SELECT 1 FROM dataset_profile_conflicts c
    WHERE c.bundle_id = p.bundle_id AND c.synthetic_u = p.synthetic_u
)
AND NOT EXISTS (
    SELECT 1 FROM dataset_profile_duplicate_rows d
    WHERE d.bundle_id = p.bundle_id AND d.row_no = p.row_no
);

CREATE VIEW dataset_payments_clean AS
SELECT p.* FROM dataset_payments p
WHERE NOT EXISTS (
    SELECT 1 FROM dataset_payment_duplicate_rows d
    WHERE d.bundle_id = p.bundle_id AND d.row_no = p.row_no
);

CREATE VIEW dataset_person_company_links_clean AS
SELECT l.* FROM dataset_person_company_links l
WHERE NOT EXISTS (
    SELECT 1 FROM dataset_person_link_conflicts c
    WHERE c.bundle_id = l.bundle_id AND c.synthetic_f = l.synthetic_f AND c.synthetic_u = l.synthetic_u
)
AND NOT EXISTS (
    SELECT 1 FROM dataset_person_link_duplicate_rows d
    WHERE d.bundle_id = l.bundle_id AND d.row_no = l.row_no
);
