# PostgreSQL table inventory for backend and ML

Snapshot checked against merged backend PR #5 (head `e33715e`) and open ML1 R02 PR #24 (head `36c85bc`). The backend migrations are the source of truth for ingestion tables; PR #24 adds the R02 adapter, DTOs, rules, and fixtures, but no SQL migrations or database tables.

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

## ML1 PR #24

PR #24 does not create or alter tables. Its `document_from_backend()` adapter maps Go's `articles` and `article_versions` IDs to the logical `document_id` and `document_version_id` in `SavedDocumentVersion`. R02 validates the submitted body and extraction metadata in memory. It does not create a second copy of Go's article tables, and it does not persist chunks or embeddings.

The target schema in [database_scheme.md](../rfc/database_scheme.md) describes additional future tables such as `document_chunks`, `event_facts`, and analysis storage. Those are not migrations from PR #5 or PR #24 and must not be treated as deployed tables.

## ML-owned tables in this branch

ML migrations own the `ml` schema and currently define:

| Table | Purpose |
|---|---|
| `ml.schema_migrations` | ML migration version and checksum journal, created by the ML migration runner |
| `ml.article_processing_operations` | HTTP operation state, idempotency key, saved request, quality result, and final document/event IDs |
| `ml.events` | Candidate events extracted from indexed chunks |
| `ml.event_documents` | Event-to-document-version links with chunk evidence and evidence role |
| `ml.event_extraction_runs` | First saved result and stable event IDs for `(document_version_id, event_pipeline_config_version)` retries |

## Ownership boundary

The application uses one PostgreSQL server with separate ownership: Go migrations manage ingestion and `processing_jobs`; ML migrations manage schema `ml`, operation records, and ML results. Go and ML exchange article-processing data over HTTP. ML does not copy or mutate `sources`, `articles`, `article_versions`, or Go's queue. For the current R02 input, `article_id = document_id` and `article_version_id = document_version_id` are preserved as IDs, as specified in the backend/ML contract.

ML1's future indexer must return its indexed chunks to ML2 through `IndexedArticleVersion`; this does not imply that PR #24 already provides a chunk table or a production indexer.
