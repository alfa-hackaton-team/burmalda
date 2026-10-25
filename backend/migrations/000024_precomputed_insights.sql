-- Explicit demo preparation is isolated from the retrying HTTP analysis worker.
ALTER TABLE request_operations ADD COLUMN demo_set_version text;
ALTER TABLE request_operations ADD COLUMN runtime_manifest jsonb;
ALTER TABLE request_operations ADD COLUMN evidence_snapshot jsonb;
ALTER TABLE request_operations ADD COLUMN evidence_fingerprint bytea;
ALTER TABLE request_operations ADD CONSTRAINT demo_precompute_metadata CHECK (
 demo_set_version IS NULL OR (
  length(demo_set_version) BETWEEN 1 AND 100 AND operation_type='insight'
  AND runtime_manifest IS NOT NULL AND jsonb_typeof(runtime_manifest)='object'
  AND (state <> 'SUCCEEDED' OR (evidence_snapshot IS NOT NULL AND evidence_fingerprint IS NOT NULL))
 ));
CREATE UNIQUE INDEX request_operations_demo_scope_idx
 ON request_operations(user_id,business_context_id,demo_set_version,request_fingerprint)
 WHERE demo_set_version IS NOT NULL;
CREATE INDEX insights_context_owner_idx ON insights(user_id,business_context_id,created_at DESC);

-- A frozen demo slot cannot silently acquire different profile/evidence inputs.
CREATE UNIQUE INDEX request_operations_demo_slot_idx
 ON request_operations(user_id,business_context_id,event_id,demo_set_version)
 WHERE demo_set_version IS NOT NULL;
