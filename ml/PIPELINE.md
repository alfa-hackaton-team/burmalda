# ML1 v1: indexing → evidence

`ml.pipeline.ML1Pipeline` — синхронный library entry point. Готовый путь: BackendInput → существующий quality gate → chunking v1 → Qwen embeddings → одна PostgreSQL-транзакция (snapshot + chunks + embeddings) → vector Top-20 → Qwen reranker → Top-5 evidence.

```python
from ml.database import connect_runtime
from ml.pipeline import ML1Pipeline
from ml.retrieval.interfaces import DocumentVersion

with connect_runtime() as connection:  # ML_DATABASE_URL, отдельная ML runtime-role
    pipeline = ML1Pipeline.from_local_models(
        connection,
        versions=trusted_versions,                 # существующий DocumentVersion catalog
        context_chunks=trusted_context_chunks,     # существующий allowlist
    )
    indexed = pipeline.index(
        backend_input,
        version=DocumentVersion(document_id, document_version_id, version_no, ready=False),
        business_context_ids=[context_id],          # только доверенное приложение назначает доступ
    )
    response = pipeline.search('Как изменились условия кредитования?',
                               business_context_id=context_id)  # default top_k=5
    # Либо pipeline.search(existing_SearchRequest): существующий Retriever interface.
    detailed = pipeline.search_with_scores('Как изменились условия кредитования?',
                                           business_context_id=context_id)
    # detailed.response — неизменный SearchResponse v1;
    # detailed.reranker_scores — отдельный mapping chunk_id → relevance logit.
```

Это примеры альтернативных search-вызовов, каждый выполняет inference. Для ML2 достаточно передать экземпляр pipeline как `Retriever`: `search(SearchRequest)` и `get_chunks(GetChunksRequest)`. Результаты содержат UUID документа/версии/чанка, текст, URL, источник, даты и cosine `retrieval_score`. После reranking порядок задаёт `rank`; cosine не обязан убывать. При необходимости ML2 вызывает `search_with_scores` для sidecar scores; в wire DTO новые поля не добавлены. Relevance не подтверждает истинность claim. [Evidence cases ML2](evaluation/data/ml2_evidence_cases_v1.json) сохранены без изменений; semantic verification остаётся в ML2.

## Конфигурация и сохранение

- `PipelineConfig(candidate_k=20, final_k=5, reranker_enabled=True)`; `top_k` от 1 до `candidate_k`. `reranker_enabled=False` возвращает vector-only; ошибка загрузки/inference/валидности scores тоже возвращает vector-only с безопасным предупреждением. Ошибки embedding/БД остаются ошибками, не пустой выдачей.
- Embedding: `Qwen/Qwen3-Embedding-0.6B`, `ml1-qwen3-embedding-1024-v1`, revision `97b0c614be4d77ee51c0cef4e5f07c00f9eb65b3`, 1024, прежние tokenizer и query instruction.
- Reranker: `Qwen/Qwen3-Reranker-0.6B`, revision `e61197ed45024b0ed8a2d74b80b4d909f1255473`, подтверждённый `ml1-exp-cpu-sdpa-b4-v1`: CPU/float32/SDPA/batch 4. Это повторное использование завершённого профиля, без tuning. Общая inference-реализация — `ml/reranking.py`; прежний evaluation import сохранён.
- Зависимости: `ml/requirements-embeddings.txt`; модели уже должны быть в стандартном HF cache. `from_local_models` offline-first; reranker загружается лениво, его отсутствие допускает fallback.
- Используется имеющаяся production migration `ml/migrations/000001_document_storage.sql` через `python -m ml.migrate --runtime-role ml_runtime`. Infrastructure заранее устанавливает pgvector и роли; DSN миграций — `ML_MIGRATION_DATABASE_URL`. Каталог/readiness и события добавляет миграция 000002.
- `save_indexed_document` переиспользует проверки/locks прежнего persistence. Никаких изменений текста/offsets/tokens/UUID. Идентичный retry — без дубликатов; конфликт — exception и rollback, без перезаписи. Quality FAIL возвращает `IndexResult(indexed=False)`, не пишет snapshot/chunks и не вызывает модели; pending metadata версии сохраняются. Storage/inference exceptions не публикуют новую readiness/access.

## Backend metadata

Актуальный Backend передаёт `article.version_no`. Используйте `pipeline.index(backend_input, business_context_ids=[context_id])`: явный `version=` больше не требуется. Для старых fixtures он остаётся допустимым. Номер проверяется и хранится в `ml.document_version_catalog`; readiness выставляет ML только после атомарного сохранения snapshot/chunks/embeddings. Pending/failed latest блокирует выдачу старой версии, в том числе после рестарта. `from_local_models` по умолчанию восстанавливает каталог из БД; trusted allowlists/context_chunks по-прежнему передаёт приложение. Версии старых snapshots без номера не угадываются.

Миграция `000002_pipeline_state_and_events.sql` также готовит `article_processings` и четыре таблицы событий для ML2; [поля и ограничения](PERSISTENCE.md#каталог-и-события-ml2). HTTP worker/журнал выполнения ещё нужно подключить: наличие таблицы не означает работающий POST/GET. ML1 retrieval-ready не означает SUCCEEDED всей обработки с ML2.

## Один локальный smoke

С уже кешированными моделями и **disposable admin test DB** PostgreSQL+pgvector:

```sh
HF_HUB_OFFLINE=1 TOKENIZERS_PARALLELISM=false \
ML1_TEST_POSTGRES_DSN='<disposable PostgreSQL DSN>' ML1_TEST_PIPELINE_MODEL=1 \
python -m unittest ml.tests.test_pipeline.PipelineRealSmokeTests -v
```

Тест создаёт временную схему имеющейся migration, строит реальные embeddings для 18 чанков существующей публикации, сохраняет их, выполняет реальный query/reranking и проверяет финальные 5 evidence. Удаляет временную схему; datasets не меняет. Это acceptance smoke, не evaluation/benchmark. Другие тесты файла проверяют quality FAIL, retry, conflict, rollback, пустую выдачу и fallback.

Проверено 25 октября 2026: PostgreSQL 18.4 / pgvector 0.8.5; полный suite 211 tests, 208 passed / 3 skipped (отдельные прежние model smokes), новый real end-to-end smoke прошёл. 13 documentation tests и обе contract checks прошли.

## Known limitations

1. Каталог/readiness восстанавливаются из ML-БД; allowlists после рестарта по-прежнему передаёт доверенное приложение. HTTP integration ещё не подключена.
2. Последовательная синхронная работа с одним connection/instance; CPU inference может быть медленным. HTTP/worker recovery/operation Idempotency-Key не реализуются этим library entry point; retry повторно считает embeddings, но не создаёт дубликатов в БД.
3. Candidate Top-20 может не содержать нужный чанк; reranker не восстанавливает отсутствующие кандидаты.
4. No-answer abstention/semantic verifier отсутствуют; scores не гарантируют достаточность evidence, проверка claim обязательна в ML2.
5. PostgreSQL 17 не проверен; локальная проверка выполнена на PostgreSQL 18.4/pgvector 0.8.5.
