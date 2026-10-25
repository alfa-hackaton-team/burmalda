# ML operation storage

ML operations use the shared PostgreSQL server through the separately owned `ml` schema. Go continues to own its queue and communicates with ML only over HTTP. The ML runtime uses `ML_DATABASE_URL`; schema setup uses the separate `ML_MIGRATIONS_DATABASE_URL` role.

## Ownership and setup

- Backend owns its tables, migrations, and queue.
- ML1 owns `ml.schema_migrations` and the shared document/event tables. ML2 has an independent `ml.ml2_schema_migrations` journal and extends `ml.article_processings` with its byte-exact input and event checkpoint fields.
- Infrastructure runs ML migrations with a DDL-capable role. The runtime role receives only the required DML permissions on schema `ml`.
- Go must not read or write ML tables directly.

Run ML1 migrations first, then the ML2 extension, using the migration role:

```bash
.venv/bin/python -m ml.migrate --runtime-role ml_runtime
.venv/bin/python -m ml_runtime.migrate --runtime-role ml_runtime
```

`ml_runtime.article_processing_runtime.create_article_processing_runtime(indexer)` is the composition root. It wires the ML1 R02 quality gate and indexer adapter, the ML2 extractor/verifier, PostgreSQL operation/event stores, authenticated FastAPI app, and worker. `ML_SERVICE_TOKEN`, `ML_MAX_TEXT_BYTES`, and comma-separated `ML_PIPELINE_VERSIONS` configure the API; model access uses `ML_LLM_API_KEY`, optional `ML_LLM_BASE_URL`, `ML_LLM_EVENT_MODEL`, `ML_LLM_EVENT_VERIFIER_MODEL`, and `ML_EVENT_PIPELINE_CONFIG_VERSION`. PostgreSQL stores use `ML_DATABASE_URL`. The app accepts HTTP requests while a separate worker process calls `runtime.worker.run_once()` to claim and execute one durable queued operation. `POST /internal/v0.1/article-processings` validates and persists the request, then returns `202` with `QUEUED`; it does not run model work in the request process. `GET /internal/v0.1/article-processings/{processing_id}` returns the same stored operation.

Deployment code exports a no-argument factory returning an `ml.pipeline.ML1Pipeline` and sets `ML_INDEXER_FACTORY=python.module:factory_name`. `ML1IndexerAdapter` sends the full validated BackendInput to `ML1Pipeline.index(..., service_access=True)`, then resolves the returned chunk IDs through `ML1Pipeline.get_chunks()`. ML1 and ML2 run as separate API and worker processes:

```bash
.venv/bin/python -m ml_runtime.serve_article_processing
.venv/bin/python -m ml_runtime.run_article_processing_worker
```

Apply both migration sets before either process starts. Configure `ML_MIGRATIONS_DATABASE_URL` with the migration role; application processes use `ML_DATABASE_URL`.

ML1 migration 000002 creates `article_processings`, `events`, and `event_documents` as shared ML tables. ML2 migration `000001_article_processing_checkpoints.sql` extends `article_processings` with the exact request bytes, quality result, event configuration version, and event IDs, including `[]`. ML2 writes candidates/evidence into ML1's existing `events` and `event_documents` tables. A replay returns IDs from the operation row without invoking the model again. `ML1IndexerAdapter` resolves the newly persisted chunks and the coordinator passes those exact chunks to ML2. Cross-document event merging is deferred.

The production indexer is in ML1 PR #24. Its API accepts `version_no` from Go, persists the document and chunks, and exposes those exact chunks through the existing `get_chunks` contract. Repeating the same immutable document version returns the deterministic chunk set. The API/worker entrypoints can now be run once both PRs are present and ML1 model dependencies/cache, PostgreSQL migrations, and environment configuration are available.

Each file in `ml_runtime/migrations/` has an independent ML version and checksum. The runner applies pending files in one transaction under a PostgreSQL advisory lock and refuses to run an already applied version whose contents changed.

## Operation guarantees

`PostgresOperationStore` uses ML1's `article_processings` row and unique `Idempotency-Key`; the same operation key and fingerprint return the same `processing_id`, while changed parameters return 409. ML2 stores the 32-byte fingerprint in the ML1 column and the original ASCII-escaped request in its `request_payload` extension. `trace_id` is excluded from the fingerprint.

The exact request is stored as ASCII-escaped JSON bytes. The JSONB `request_snapshot` remains a readable snapshot for valid text; NUL or unpaired surrogate input uses a base64 snapshot marker while `request_payload` retains the exact request. ML1 R02 can then return its deterministic quality FAIL instead of PostgreSQL rejecting the request before assessment.

Workers claim operations atomically. `claim_next()` uses `FOR UPDATE SKIP LOCKED` to let multiple ML workers drain QUEUED operations without double execution. A repeated claim of a RUNNING or terminal operation does not execute it again. The GET response exposes only the public operation fields; internal `quality_result` remains stored in ML.

Queued operations survive a restart and can be claimed again. A RUNNING operation is **not automatically replayed**: a process may have crashed after a paid/model call was sent but before its result was saved. The input remains available through `get_input()` for reconciliation. Automatic recovery from an in-flight stage needs persisted stage checkpoints and stage-level idempotency; it must not blindly call the model again.

The PostgreSQL integration tests run only when `ML_TEST_DATABASE_URL` points to a disposable test database. No test database URL is committed.
