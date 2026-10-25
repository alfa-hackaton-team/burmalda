ALTER TABLE dataset_company_profiles DROP CONSTRAINT dataset_company_profiles_pkey;
ALTER TABLE dataset_company_profiles ADD COLUMN row_no bigint NOT NULL CHECK (row_no > 0);
ALTER TABLE dataset_company_profiles ADD PRIMARY KEY (bundle_id, row_no);
CREATE INDEX dataset_company_profiles_company_idx ON dataset_company_profiles (bundle_id, synthetic_u);
