# PostgreSQL table inventory for backend and ML

Snapshot checked against merged backend PR #5 (head `e33715e`) and current ML1 PR #24 (head `74916ef`). Go owns ingestion tables. ML1's migrations own the shared ML schema tables used by indexing and event storage; ML2 adds columns to the shared processing operation table through its separate migration journal.

## Backend-owned tables

Backend migrations create eight tables. They remain owned and migrated by Go.

| Table | Purpose | Relevant columns and later migration additions |
|---|---|---|
| `sources` | Source registry and polling configuration | `id`, `source_type`, `source_role`, `trust_level`, `adapter_type`, `endpoint`, `adapter_config`, `config_version`, polling/lease state |
| `source_polls` | One record per source poll | `source_id`, `status`, trace/timing, HTTP status, article counts, cursor; migration 000006 adds `failed_entry_count` and `PARTIAL` status |
| `source_poll_entry_failures` | Failed entries from a partial poll | `(poll_id, entry_no)`, article URL, error code/message |
| `raw_artifacts` | Original fetched bytes or object URI | `source_id`, SHA-256, content type, size, `content` or `object_uri` |
| `articles` | Stable source-scoped article identity | `source_id`, identity kind/key, external ID, canonical URL, `latest_version_id` |
| `article_versions` | Immutable parsed article versions | `article_id`, `version_no`, `content_hash`, `parser_version`, title/body, published/fetched dates, `raw_artifact_id`; migration 000003 adds `language`, `author`, and extraction `metadata` |
| `article_fetch_jobs` | Retryable fetch work discovered during polling | `source_id`, URL, status/attempt/lease fields, last error and HTTP status |
| `processing_jobs` | Go queue for article processing | `article_version_id`, `pipeline_version`, `ml_processing_id`, document/event IDs and status; later migrations add source snapshot fields, `event_ids`, source config version, and error/retry diagnostics |

Other backend migrations add indexes and constraints to these tables; they do not add more tables.

## ML1 PR #24 tables

PR #24 now includes the production `ML1Pipeline`, persistent chunk indexing, and these ML-owned tables:

| Table | Owner and purpose |
|---|---|
| `ml.schema_migrations` | ML1 migration journal |
| `ml.document_versions` | Immutable canonical document snapshot; IDs preserve Go's `article_id` and `article_version_id` |
| `ml.document_chunks` | Persisted chunks and embeddings |
| `ml.document_version_catalog` | Trusted `version_no` ordering and index readiness |
| `ml.article_processings` | Shared Go-facing operation identity, idempotency, state, result, and error |
| `ml.events` | Candidate/confirmed/disputed events |
| `ml.event_documents` | Event-to-document links, evidence quotes, and pipeline fingerprints |
| `ml.event_facts` | Typed event facts |
| `ml.fact_evidence` | Fact-to-chunk evidence links |

ML1's `ML1Pipeline.index(BackendInput, service_access=True)` persists the canonical document, chunks, embeddings, and readiness; its result contains chunk IDs. `get_chunks()` then returns the exact stored chunk text. The ML2 adapter uses this path and does not maintain a second document or chunk store.

## ML2 additions and ownership boundary

ML2 owns a separate migration journal, `ml.ml2_schema_migrations`. Its migration extends `ml.article_processings` with a byte-exact request payload, `event_ids`, `quality_result`, and `event_pipeline_config_version`. This preserves malformed text for the quality gate and records a successful empty event result without a duplicate operation or event-run table.

ML2 writes event candidates into ML1's existing `ml.events` and `ml.event_documents` tables. It does not create parallel event tables. Go owns ingestion and its queue; ML1 owns document/chunk indexing; ML2 owns event extraction and extends the shared article-processing result. Go and ML exchange processing data over HTTP. ML never copies or mutates Go's `sources`, `articles`, `article_versions`, or `processing_jobs`.

The service applies ML1 migrations first, then ML2 migrations. The runtime role is provisioned from the ML1 grants and receives the additional checkpoint-column updates via `python -m ml_runtime.migrate --runtime-role <role>`.
