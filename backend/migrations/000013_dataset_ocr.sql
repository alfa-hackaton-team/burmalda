ALTER TABLE dataset_bank_reports DROP CONSTRAINT dataset_bank_reports_text_status_check;
ALTER TABLE dataset_bank_reports ADD CONSTRAINT dataset_bank_reports_text_status_check
    CHECK (text_status IN ('EXTRACTED', 'NEEDS_OCR', 'OCR'));
ALTER TABLE dataset_bank_reports ADD COLUMN text_sha256 text
    CHECK (text_sha256 IS NULL OR text_sha256 ~ '^[0-9a-f]{64}$');
