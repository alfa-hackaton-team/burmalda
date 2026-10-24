CREATE VIEW active_dataset_company_profiles AS
SELECT p.* FROM dataset_company_profiles_clean p
JOIN active_dataset_bundle a ON a.bundle_id = p.bundle_id
JOIN dataset_normalization_runs n ON n.bundle_id = p.bundle_id;

CREATE VIEW active_dataset_payments AS
SELECT p.* FROM dataset_payments_clean p
JOIN active_dataset_bundle a ON a.bundle_id = p.bundle_id
JOIN dataset_normalization_runs n ON n.bundle_id = p.bundle_id
WHERE NOT EXISTS (
    SELECT 1 FROM dataset_profile_conflicts c
    WHERE c.bundle_id = p.bundle_id AND c.synthetic_u IN (p.payer, p.beneficiary)
);

CREATE VIEW active_dataset_person_company_links AS
SELECT l.* FROM dataset_person_company_links_clean l
JOIN active_dataset_bundle a ON a.bundle_id = l.bundle_id
JOIN dataset_normalization_runs n ON n.bundle_id = l.bundle_id
WHERE NOT EXISTS (
    SELECT 1 FROM dataset_profile_conflicts c
    WHERE c.bundle_id = l.bundle_id AND c.synthetic_u = l.synthetic_u
);
