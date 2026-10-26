-- Operator-only trace of the exact run, including failed verification.
-- Public GET continues to return only the persisted public Insight payload.
ALTER TABLE request_operations ADD COLUMN analysis_diagnostics jsonb
 CHECK (analysis_diagnostics IS NULL OR jsonb_typeof(analysis_diagnostics)='object');
