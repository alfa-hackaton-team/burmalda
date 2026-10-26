# ML storage

ML владеет схемой `ml` и напрямую работает с PostgreSQL через psycopg. Go ↔ ML — только HTTP; прямых запросов/FK к таблицам Go нет. Chunking и DTO не менялись.

## Схема

[000001_document_storage.sql](migrations/000001_document_storage.sql) создаёт:

- `ml.document_versions`: `id UUID PK`, `document_id UUID`, `snapshot JSONB`, `created_at`.
- `ml.document_chunks`: поля DocumentChunk, FK на локальный снимок, generated `search_vector` (`simple`), nullable `VECTOR(1024)`/model/version.
- [Migration runner](migrate.py) ведёт собственный `ml.schema_migrations`: version, checksum, applied_at. Повторы безопасны; изменение применённого SQL — ошибка. DDL и журнал транзакционны.

Snapshot — точный `SavedDocumentVersion.to_dict()` без `trace_id`/`pipeline_version`: они относятся к будущему журналу операций. Пустой title, даты-строки, None, source/extraction и Go content_hash сохраняются. ID версии равен article_version_id Go. Изменение snapshot под прежним ID — конфликт; новый trace/pipeline допустим.

## Запуск

Инфраструктура заранее устанавливает pgvector и создаёт две отдельные роли без прав на Go: migration-role владеет ML-схемой, runtime-role работает с данными. Миграционной роли нужны CONNECT/CREATE на БД и USAGE на схему extension. Пароли/DSN хранятся вне Git.

```sh
python -m pip install -r ml/requirements-persistence.txt
# DSN migration-role задан в ML_MIGRATION_DATABASE_URL
python -m ml.migrate --runtime-role ml_runtime
```

Runtime использует только `ML_DATABASE_URL`; fallback на Go DATABASE_URL отсутствует. Grants: USAGE на `ml`, SELECT/INSERT двух data-таблиц, UPDATE только embedding/model/version. Нет DDL, DELETE, изменения снимка/текста/offsets и доступа к журналу. Bootstrap/grants не создают роли и не меняют Go/PUBLIC permissions; runtime нельзя запускать под администратором/владельцем.

```python
from ml.database import connect_runtime
from ml.adapters.postgres_chunks import save_document_version_and_chunks, save_document_embeddings

with connect_runtime() as connection:
    save_document_version_and_chunks(connection, saved_document, chunks)
    vectors = embedder.encode_chunks(chunks)  # вне транзакции БД
    save_document_embeddings(connection, chunks, vectors)
```

## Гарантии

Wrapper проверяет PASS, UUID, полное покрытие body и точные срезы. Снимок + весь набор чанков записываются одной транзакцией READ COMMITTED под locks версии и profile. Повтор идентичного набора — no-op; конфликт или ошибка вставки откатывает также новый снимок. Чанки/created_at не переписываются. Старый `save_document_chunks` оставлен для существующего родителя; схема по умолчанию теперь `ml`.

Embeddings записываются отдельно и атомарно; правила UUID/hash/profile/roundoff — в [EMBEDDINGS.md](EMBEDDINGS.md). Все writers соблюдают один locking protocol. Соединение должно быть idle; чужая транзакция не коммитится/откатывается. Автоматического переноса public-данных и down migrations нет.

## Проверки и границы

```sh
ML1_TEST_POSTGRES_DSN='<disposable admin test DSN>' python -m unittest ml.tests.test_ml_storage -v
```

Тесты применяют настоящий SQL в изолированных схемах: snapshot/provenance, FK/vector, retry/conflict, rollback DDL/данных, конкурентные записи, отдельный migration owner и ограниченный runtime. Тестовой роли нужны CREATE SCHEMA/CREATE ROLE/GRANT; production runtime — нет. Существующая deployment-схема не удаляется. Старые low-level fixture-тесты остаются регрессией; без DSN integration пропускается.

Проверено: 143 runtime PASS без skips, включая pretrained Qwen smoke; 13 doc tests и обе проверки контрактов; PostgreSQL 18.4. Миграция применена также к локальной `ml1_test`, владелец таблиц — ml_migrator, runtime — ml_runtime. PostgreSQL 17 ещё не проверен.

Операции/Idempotency-Key, worker recovery, HTTP lifecycle и ready/SUCCEEDED пока не реализованы. Снимок содержимого не заменяет журнал операций и не предотвращает повторный запуск модели. Vector baseline описан в [RETRIEVAL.md](RETRIEVAL.md); retention и backfill — следующие этапы.

## Каталог и события ML2

Миграция `000002_pipeline_state_and_events.sql` добавляет в ту же ML-схему:

| Таблица | Назначение |
| --- | --- |
| `document_version_catalog` | Backend `version_no`, UUID документа/версии, `ready`, `indexed_at`; pending допускается до quality PASS, поэтому FK на snapshot отсутствует |
| `article_processings` | `processing_id`, постоянный вход, fingerprint SHA-256 (32 bytes), уникальный Idempotency-Key и `(article_version_id,pipeline_version)`, состояния/результат/ошибка |
| `events` | События: тип, заголовок, summary, nullable дата, geography, статус, confidence |
| `event_documents` | Связь события с версией: role/primary, quotes, nullable candidate fingerprint, обе версии конфигурации |
| `event_facts` | Факты события со статусом проверки |
| `fact_evidence` | Точные chunk UUID и цитаты фактов; FK только на ML-таблицы |

`ML1Pipeline.index(payload)` использует `article.version_no` из актуального Backend. Старые fixture-входы без него поддерживаются только при явном `version=DocumentVersion(...)`. SavedDocumentVersion и chunking не меняются. Каталог сначала сохраняет pending; snapshot/chunks/embeddings/readiness затем фиксируются одной транзакцией. Quality FAIL может оставить только pending metadata, без snapshot/chunks. После рестарта `from_local_models` загружает каталог из БД; allowlists по-прежнему передаёт доверенное приложение. Исторические snapshots без номера автоматически не нумеруются: нужна повторная доставка Backend metadata.

Таблица операций подготовлена для HTTP worker, но сама миграция **не реализует** POST/GET, fingerprint computation, захват выполнения или recovery. `trace_id` должен исключаться из fingerprint в обработчике. Входные поля операций immutable для runtime-role; доступны только обновления состояния/результата/ошибки. Runtime не получает DDL/DELETE/доступ к Go.

ML2: `pipeline_version` — версия общей обработки, `event_pipeline_config_version` — отдельная версия event-конфигурации; они не считаются автоматически равными. PK связи `(event_id, document_version_id, event_pipeline_config_version)`; кандидат уникален внутри `(document_version_id,event_pipeline_config_version,candidate_fingerprint)`, если fingerprint задан. Нет глобальной уникальности по общему заголовку при неизвестных дате/географии. Проверку цитаты и claim выполняет ML2; SQL FK не проверяет entailment.

Применение: прежний `ML_MIGRATION_DATABASE_URL=... python -m ml.migrate --runtime-role ml_runtime`. Миграцию выполнять migration-role, не Go/runtime-role.
