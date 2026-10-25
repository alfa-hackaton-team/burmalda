CREATE TABLE request_operations (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 business_context_id uuid NOT NULL REFERENCES business_contexts(id),
 operation_type text NOT NULL CHECK (operation_type IN ('insight','follow_up')),
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
 request_fingerprint bytea NOT NULL CHECK (octet_length(request_fingerprint)=32),
 request_snapshot jsonb NOT NULL CHECK (jsonb_typeof(request_snapshot)='object'),
 state text NOT NULL DEFAULT 'QUEUED' CHECK (state IN ('QUEUED','RUNNING','SUCCEEDED','INSUFFICIENT_EVIDENCE','FAILED')),
 trace_id uuid NOT NULL UNIQUE,
 event_id uuid NOT NULL,
 conversation_id uuid,
 request_message_id uuid,
 ml_analysis_id text,
 response_snapshot jsonb,
 insight_id uuid,
 message_id uuid,
 error jsonb,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_token uuid,
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz,
 UNIQUE(user_id,operation_type,idempotency_key),
 CHECK ((lease_token IS NULL)=(lease_until IS NULL)),
 CHECK (state <> 'SUCCEEDED' OR ((insight_id IS NOT NULL) <> (message_id IS NOT NULL))),
 CHECK (state <> 'FAILED' OR error IS NOT NULL)
);
CREATE INDEX request_operations_due_idx ON request_operations(next_attempt_at,id) WHERE state IN ('QUEUED','RUNNING');
CREATE TABLE insights (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 business_context_id uuid NOT NULL REFERENCES business_contexts(id),
 request_operation_id uuid NOT NULL UNIQUE REFERENCES request_operations(id),
 version integer NOT NULL CHECK(version>0),
 event_id uuid NOT NULL,
 payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object'),
 ml_snapshot jsonb NOT NULL CHECK(jsonb_typeof(ml_snapshot)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(id,version)
);
CREATE TABLE conversations (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 insight_id uuid NOT NULL REFERENCES insights(id),
 idempotency_key text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,insight_id,idempotency_key)
);
CREATE TABLE messages (
 id uuid PRIMARY KEY,
 conversation_id uuid NOT NULL REFERENCES conversations(id),
 sequence integer NOT NULL CHECK(sequence>0),
 parent_message_id uuid,
 author text NOT NULL CHECK(author IN ('USER','ASSISTANT')),
 payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object'),
 ml_snapshot jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(conversation_id,sequence),
 UNIQUE(conversation_id,id),
 FOREIGN KEY(conversation_id,parent_message_id) REFERENCES messages(conversation_id,id)
);
ALTER TABLE request_operations ADD FOREIGN KEY(conversation_id) REFERENCES conversations(id);
ALTER TABLE request_operations ADD FOREIGN KEY(request_message_id) REFERENCES messages(id);
ALTER TABLE request_operations ADD FOREIGN KEY(insight_id) REFERENCES insights(id);
ALTER TABLE request_operations ADD FOREIGN KEY(message_id) REFERENCES messages(id);
CREATE UNIQUE INDEX request_operations_active_conversation_idx ON request_operations(conversation_id) WHERE state IN ('QUEUED','RUNNING');
CREATE TABLE feedback (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 insight_id uuid NOT NULL REFERENCES insights(id),
 insight_version integer NOT NULL,
 idempotency_key text,
 payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(insight_id,insight_version) REFERENCES insights(id,version),
 UNIQUE(user_id,insight_id,idempotency_key)
);
CREATE TABLE feed_snapshots (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 business_context_id uuid NOT NULL REFERENCES business_contexts(id),
 profile_id uuid NOT NULL REFERENCES business_profiles(id),
 filters_fingerprint bytea NOT NULL CHECK(octet_length(filters_fingerprint)=32),
 ranking_version text NOT NULL,
 items jsonb NOT NULL CHECK(jsonb_typeof(items)='array'),
 alerts jsonb NOT NULL CHECK(jsonb_typeof(alerts)='array'),
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '1 hour'
);
CREATE INDEX feed_snapshots_expiry_idx ON feed_snapshots(expires_at);
CREATE TABLE client_analytics_events (
 analytics_event_id text PRIMARY KEY CHECK(length(analytics_event_id) BETWEEN 1 AND 200),
 user_id uuid NOT NULL REFERENCES users(id),
 business_context_id uuid NOT NULL REFERENCES business_contexts(id),
 schema_version text NOT NULL,
 event_type text NOT NULL CHECK(event_type IN ('FEED_OPENED','INSIGHT_OPENED','SOURCE_CLICKED','ALERT_OPENED')),
 occurred_at timestamptz NOT NULL,
 payload jsonb NOT NULL,
 received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX client_analytics_events_user_time_idx ON client_analytics_events(user_id,occurred_at DESC);
CREATE TABLE rate_limit_buckets (
 subject_hash bytea NOT NULL,
 limit_name text NOT NULL,
 window_started_at timestamptz NOT NULL,
 used integer NOT NULL CHECK(used>0),
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(subject_hash,limit_name,window_started_at)
);
CREATE INDEX rate_limit_buckets_expiry_idx ON rate_limit_buckets(expires_at);
CREATE TABLE source_sync_requests (
 id uuid PRIMARY KEY,
 source_id uuid NOT NULL REFERENCES sources(id),
 user_id uuid NOT NULL REFERENCES users(id),
 idempotency_key text,
 poll_id uuid REFERENCES source_polls(id) ON DELETE SET NULL,
 state text NOT NULL DEFAULT 'QUEUED' CHECK(state IN ('QUEUED','RUNNING','SUCCEEDED','PARTIAL','FAILED')),
 trace_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,source_id,idempotency_key)
);
CREATE TABLE user_passwords (
 user_id uuid PRIMARY KEY REFERENCES users(id),
 login text NOT NULL UNIQUE CHECK(length(login) BETWEEN 1 AND 200),
 salt bytea NOT NULL CHECK(octet_length(salt)=32),
 password_hash bytea NOT NULL CHECK(octet_length(password_hash)=32),
 iterations integer NOT NULL CHECK(iterations>=600000),
 updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION preserve_source_sync_state() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE source_sync_requests SET state=NEW.status WHERE poll_id=NEW.id;
 RETURN NEW;
END;
$$;
CREATE TRIGGER source_polls_sync_state AFTER UPDATE OF status ON source_polls
FOR EACH ROW EXECUTE FUNCTION preserve_source_sync_state();
