ALTER TABLE dataset_bank_reports DROP CONSTRAINT dataset_bank_reports_text_status_check;
ALTER TABLE dataset_bank_reports ADD CONSTRAINT dataset_bank_reports_text_status_check
    CHECK (text_status IN ('EXTRACTED', 'NEEDS_OCR', 'OCR', 'PROVIDED'));
ALTER TABLE dataset_bank_reports ADD COLUMN text_issues jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(text_issues) = 'array');
