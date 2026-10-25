# PostgreSQL chunk persistence

ML1 сохраняет готовый полный набор `DocumentChunk` одной версии документа и одного профиля. Chunker и DTO не меняются; embeddings, HTTP lifecycle и готовность остаются за пределами этого адаптера.

## Схема и границы

Production-миграции `document_chunks` пока нет. Адаптер реализован под согласованную [целевую схему](../docs/rfc/database_scheme.md#113-document_chunks): восемь полей DTO, `created_at DEFAULT now()`, generated `search_vector`, nullable embedding-поля. Перед подключением необходимо применить отдельную согласованную миграцию. Адаптер не создаёт таблицы, версии документов или provenance.

Записываются только `id`, `document_version_id`, `chunking_profile_id`, `chunk_no`, `chunk_text`, `char_start`, `char_end`, `token_count`. UUID передаются PostgreSQL как UUID, остальные значения — без пересчёта и нормализации. `created_at`/FTS формирует БД; embedding-поля адаптер не читает и не меняет. Нет runtime-проверки схемы или автоматической миграции: несовместимая таблица приводит к ошибке с rollback.

## Использование

Установить `ml/requirements-persistence.txt`. DSN получает вызывающий процесс из доверенной конфигурации; адаптер не читает environment и не логирует DSN/текст.

```python
import psycopg
from ml.adapters.postgres_chunks import save_document_chunks

# chunks — полный результат ранее выполненного chunk_document(...).
with psycopg.connect(configured_dsn) as connection:
    save_document_chunks(connection, chunks, schema='public')
```

Соединение должно быть открытым и idle, без текущей транзакции; autocommit допускается в обоих режимах. Функция сама открывает и завершает одну транзакцию, не закрывает соединение. Активная/ошибочная транзакция вызывающего кода отклоняется без commit/rollback чужой работы. Название схемы экранируется как SQL identifier.

## Атомарность и идемпотентность

1. До SQL проверяются непустой typed-набор, одна версия/профиль, уникальные UUID, номера от 0, продвижение диапазонов без пропусков и совпадение overlap. Значения не исправляются, набор не сортируется.
2. В транзакции READ COMMITTED берётся `pg_advisory_xact_lock` по стабильному SHA-256 ключу `(schema, document_chunks, document_version_id, chunking_profile_id)`. Все writers этого набора должны использовать тот же протокол. Lock автоматически снимается при commit/rollback.
3. Существующий набор читается целиком по номеру с FOR UPDATE. Полное совпадение всех восьми полей DTO — успешный no-op. Любое различие, недостающие/лишние строки — `ChunkPersistenceConflict`; частичный набор не дополняется автоматически.
4. Если строк нет, весь переданный набор вставляется без UPDATE/UPSERT. Ошибка любой вставки откатывает весь набор. Глобальная коллизия UUID/позиции также считается конфликтом и откатывает транзакцию.

READ COMMITTED нужен для видимости результата первого writer после ожидания блокировки вторым. Две идентичные параллельные записи успешны; для двух разных наборов одной пары успешен один, второй получает конфликт. Блокировка действует только на согласованных writers: внешние процессы не должны изменять immutable чанки в обход протокола.

`ChunkPersistenceError` обозначает ошибку PostgreSQL, `ChunkPersistenceConflict` — конфликт immutable данных. Сообщения безопасные, без SQL diagnostics/текста/DSN; детали исходной ошибки не передаются в публичный контракт. Ошибки формы входа — ValueError. Persistence не меняет статусы ready/SUCCEEDED.

Сверку с полным body, quality PASS, точность token_count и соответствие профилю обеспечивает предшествующий chunker и вызывающий код. Без определённого контракта локального снимка версии адаптер не может установить, является ли впервые переданный prefix полным документом. Нельзя разбивать вызов на несколько batches для одной версии/профиля.

## Тесты

```sh
python -m pip install -r ml/requirements-persistence.txt
python -m unittest ml.tests.test_chunk_persistence -v
ML1_TEST_POSTGRES_DSN='<DSN отдельной тестовой БД>' python -m unittest ml.tests.test_chunk_persistence -v
```

Без DSN integration-тесты явно пропускаются; unit-тестам нужен только psycopg. При отсутствии optional драйвера persistence-тесты также пропускаются, R01/R02/chunking остаются доступны.

Integration-тесты создают уникальную схему `ml1_test_chunks_<UUID>` и удаляют только её, public-таблицы не изменяются. Тестовой роли требуется CREATE SCHEMA. [DDL fixture](tests/fixtures/persistence.sql) — не migration: родительская таблица содержит только UUID, FTS использует test-only `simple`, embedding заменён nullable массивом, поэтому pgvector не нужен. Это проверка persistence восьми полей DTO на реальном PostgreSQL, не доказательство совместимости с ещё не созданной production-миграцией.

Покрыты все поля/Unicode, идентичный retry с сохранением created_at/FTS/существующего embedding, конфликт каждого изменяемого поля, неполный/расширенный набор, rollback второй вставки, FK failure, UUID collision, разные версии/профили, оба autocommit-режима, dict row_factory и параллельные identical/conflicting writers.

Проверено 25 октября 2026: 98 ML/runtime-тестов без skips, включая 18 новых persistence-тестов (4 unit, 14 integration) на временном PostgreSQL 18.4 и настоящий cached Qwen tokenizer. Также прошли 13 документальных тестов, `check_ml_contracts.py --full` (16 fixtures), `check_api_contracts.py` (252 local references) и `git diff --check`. Тестовый PostgreSQL остановлен после прогона. Backend PostgreSQL 17 и будущая production-миграция этим прогоном не проверены.

До production-миграции остаются решения о локальном снимке версии/FK, FTS-конфигурации, N и embedding_version, а также backfill при наличии старых строк. Согласованный состав DocumentChunk не менялся.
