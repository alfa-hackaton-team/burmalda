CREATE TABLE dataset_bank_report_documents (
    bundle_id text NOT NULL,
    name text NOT NULL,
    article_version_id uuid NOT NULL REFERENCES article_versions (id) ON DELETE RESTRICT,
    PRIMARY KEY (bundle_id, name),
    FOREIGN KEY (bundle_id, name) REFERENCES dataset_bank_reports (bundle_id, name) ON DELETE CASCADE
);

CREATE INDEX dataset_bank_report_documents_version_idx
    ON dataset_bank_report_documents (article_version_id);

CREATE TABLE dataset_bank_report_pages (
    bundle_id text NOT NULL,
    name text NOT NULL,
    page_no integer NOT NULL CHECK (page_no > 0),
    start_byte integer NOT NULL CHECK (start_byte >= 0),
    end_byte integer NOT NULL CHECK (end_byte > start_byte),
    PRIMARY KEY (bundle_id, name, page_no),
    FOREIGN KEY (bundle_id, name) REFERENCES dataset_bank_report_documents (bundle_id, name) ON DELETE CASCADE
);
