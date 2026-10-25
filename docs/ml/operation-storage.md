# ML operation storage

ML operations use the shared PostgreSQL server through the separately owned `ml` schema. Go continues to own its queue and communicates with ML only over HTTP. The ML runtime uses `ML_DATABASE_URL`; schema setup uses the separate `ML_MIGRATIONS_DATABASE_URL` role.

## Ownership and setup

- Backend owns its tables, migrations, and queue.
- ML owns `ml.schema_migrations`, ML operation state, saved request snapshots, and processing results.
- Infrastructure runs ML migrations with a DDL-capable role. The runtime role receives only the required DML permissions on schema `ml`.
- Go must not read or write ML tables directly.

Run migrations from the repository root after configuring the migration role locally:

```bash
.venv/bin/python -m ml_runtime.migrate
```

The authenticated Go-facing routes are provided by `ml_runtime.article_processing_api.create_article_processing_app`. The caller injects an `ArticleProcessingCoordinator`, service token, text-byte limit, and supported pipeline-version set. `POST /internal/v0.1/article-processings` validates and persists the request, then returns `202` with `QUEUED`; it does not run model work in the request process. `GET /internal/v0.1/article-processings/{processing_id}` returns the same stored operation. A worker calls `ArticleProcessingWorker.run_once()` to claim and execute one durable queued operation. `create_article_processing_app_from_env` reads `ML_SERVICE_TOKEN`, `ML_MAX_TEXT_BYTES`, and comma-separated `ML_PIPELINE_VERSIONS` while still requiring the coordinator to be supplied.

Migration `000002_event_candidates.sql` adds ML-owned candidate events and document links. Migration `000003_unify_event_processing_state.sql` puts the event configuration version and saved event IDs on `ml.article_processing_operations`, adds uniqueness for `(article_version_id, pipeline_version)`, and removes the redundant extraction-run table. The operation row therefore also records a successful empty event result, so replay does not call the model again. ML1's `Indexer` returns `IndexedArticleVersion` with its chunks; the coordinator validates the document version and passes this value to ML2. `ML2EventProcessor` uses the typed result in [event-extraction-contract.md](event-extraction-contract.md). Cross-document event merging is deferred.

This repository does not yet provide a complete deployment bootstrap for the article pipeline: the app factory must be given concrete ML1 indexing and ML2 event-processing adapters. The current ML1 R02 adapter validates the Go payload and quality; its R01 retriever serves configured search/chunk requests, but it does not index a newly submitted article or return its chunks. ML1 therefore still needs to provide the production indexing boundary used by this worker. Until those adapters are wired, the HTTP layer can be exercised with test adapters, but it is not a standalone production service.

Each file in `ml_runtime/migrations/` has an independent ML version and checksum. The runner applies pending files in one transaction under a PostgreSQL advisory lock and refuses to run an already applied version whose contents changed.

## Operation guarantees

`PostgresOperationStore` stores the unique `Idempotency-Key`, input fingerprint, original trace, full input snapshot, state, quality result, and terminal outcome. A unique constraint serializes concurrent POSTs: the same key and fingerprint return the same `processing_id`; the same key with a different fingerprint returns 409. `trace_id` is excluded from the fingerprint.

The input snapshot is stored as ASCII-escaped JSON bytes. This preserves NUL and malformed Unicode in `article.body` so ML1 R02 can return its deterministic quality FAIL instead of PostgreSQL rejecting the request before assessment.

Workers claim operations atomically. `claim_next()` uses `FOR UPDATE SKIP LOCKED` to let multiple ML workers drain QUEUED operations without double execution. A repeated claim of a RUNNING or terminal operation does not execute it again. The GET response exposes only the public operation fields; internal `quality_result` remains stored in ML.

Queued operations survive a restart and can be claimed again. A RUNNING operation is **not automatically replayed**: a process may have crashed after a paid/model call was sent but before its result was saved. The input remains available through `get_input()` for reconciliation. Automatic recovery from an in-flight stage needs persisted stage checkpoints and stage-level idempotency; it must not blindly call the model again.

The PostgreSQL integration tests run only when `ML_TEST_DATABASE_URL` points to a disposable test database. No test database URL is committed.
