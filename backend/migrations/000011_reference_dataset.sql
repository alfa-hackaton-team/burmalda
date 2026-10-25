CREATE TABLE dataset_bundles (
    id text PRIMARY KEY,
    manifest_sha256 text NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
    imported_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE active_dataset_bundle (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    bundle_id text NOT NULL REFERENCES dataset_bundles (id) ON DELETE RESTRICT
);

CREATE TABLE dataset_files (
    bundle_id text NOT NULL REFERENCES dataset_bundles (id) ON DELETE RESTRICT,
    name text NOT NULL,
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    row_count bigint,
    PRIMARY KEY (bundle_id, name)
);

CREATE TABLE dataset_company_profiles (
    bundle_id text NOT NULL REFERENCES dataset_bundles (id) ON DELETE RESTRICT,
    synthetic_u text NOT NULL,
    org_type text, okfs_name text, company_age_years_gradation text,
    eco_mark_label text, mainokved_code text, mainokved_name text,
    negativemark text, amount_workers_fns_gradation text,
    amount_workers_spark_gradation text, cl_type text,
    cl_product_rko_mark text, cl_product_gar_mark text,
    cl_product_loa_mark text, cl_product_lea_mark text,
    cl_product_dep_mark text, cl_product_zpp_mark text,
    fts_flag text, fts_country_name text, factoring_mark text,
    revenue_company text, group_id text, city text, region text,
    PRIMARY KEY (bundle_id, synthetic_u)
);

CREATE TABLE dataset_payments (
    bundle_id text NOT NULL REFERENCES dataset_bundles (id) ON DELETE RESTRICT,
    row_no bigint NOT NULL CHECK (row_no > 0),
    beneficiary text NOT NULL,
    payer text NOT NULL,
    pay_amt numeric NOT NULL,
    pay_cnt bigint NOT NULL CHECK (pay_cnt >= 0),
    PRIMARY KEY (bundle_id, row_no)
);
CREATE INDEX dataset_payments_payer_idx ON dataset_payments (bundle_id, payer);
CREATE INDEX dataset_payments_beneficiary_idx ON dataset_payments (bundle_id, beneficiary);

CREATE TABLE dataset_person_company_links (
    bundle_id text NOT NULL REFERENCES dataset_bundles (id) ON DELETE RESTRICT,
    row_no bigint NOT NULL CHECK (row_no > 0),
    synthetic_f text NOT NULL,
    city_name text,
    rc_name text,
    synthetic_u text NOT NULL,
    PRIMARY KEY (bundle_id, row_no)
);
CREATE INDEX dataset_person_company_links_company_idx ON dataset_person_company_links (bundle_id, synthetic_u);

CREATE TABLE dataset_bank_reports (
    bundle_id text NOT NULL REFERENCES dataset_bundles (id) ON DELETE RESTRICT,
    name text NOT NULL,
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    content bytea,
    object_uri text,
    body_text text NOT NULL DEFAULT '',
    text_status text NOT NULL CHECK (text_status IN ('EXTRACTED', 'NEEDS_OCR')),
    PRIMARY KEY (bundle_id, name),
    CONSTRAINT dataset_bank_reports_storage CHECK ((content IS NOT NULL) <> (object_uri IS NOT NULL))
);
