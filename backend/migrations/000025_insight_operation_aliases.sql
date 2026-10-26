-- Bind keys from a fresh browser session to a recovered operation without
-- creating another paid operation or weakening key/fingerprint semantics.
CREATE TABLE insight_operation_aliases (
 user_id uuid NOT NULL REFERENCES users(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 200),
 request_fingerprint bytea NOT NULL CHECK(octet_length(request_fingerprint)=32),
 request_operation_id uuid NOT NULL REFERENCES request_operations(id) ON DELETE CASCADE,
 PRIMARY KEY(user_id,idempotency_key)
);
CREATE INDEX insight_operation_aliases_operation_idx ON insight_operation_aliases(request_operation_id);
CREATE INDEX request_operations_insight_scope_idx
 ON request_operations(user_id,business_context_id,event_id,created_at DESC,id DESC)
 WHERE operation_type='insight';
