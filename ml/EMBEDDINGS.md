# ML1 Qwen embeddings

Этап: готовые `DocumentChunk[]` → batch inference → `ChunkEmbedding[]` → заполнение embedding-полей уже сохранённых чанков. Chunking, DocumentChunk и публичные R01/R02 DTO не изменены. Импорт `ml.embeddings` не требует torch и не загружает веса.

## Неизменяемый профиль

| Параметр | Значение |
|---|---|
| embedding_version | `ml1-qwen3-embedding-1024-v1` |
| embedding_model | `Qwen/Qwen3-Embedding-0.6B` |
| model/tokenizer revision | `97b0c614be4d77ee51c0cef4e5f07c00f9eb65b3`, тот же, что у chunking v1 |
| dimension | 1024, без сокращения размерности |
| Runtime | torch 2.9.0, transformers 4.57.1, tokenizers 0.22.1; зависимости отдельно в requirements-embeddings.txt |
| Inference | CPU float32, eager attention, eval + inference_mode, без dropout |
| Tokenization | Тот же tokenizer.json, add_special_tokens=False, left padding; нет truncation |
| Input budget | 8192 токена вместе с query instruction; превышение — ошибка |
| Pooling/output | Последняя реальная позиция; L2 normalization; float32, проверка 1024/finite/nonzero |
| Document input | Точный chunk_text; без title, instruction, trim и исправлений |
| Query input | `Instruct: Given a web search query, retrieve relevant passages that answer the query\nQuery:{query}` |
| Batching | По умолчанию 8; задаётся вызывающим кодом |

Профиль задаётся в `ml/embeddings/profile.py`; изменение revision, dimension, dtype/device, pooling, нормализации, tokenizer-настроек, query instruction или input budget требует новой версии. Это воспроизводимая конфигурация reference runtime, а не обещание побитового равенства на разном железе/BLAS или при другом batch padding. Векторы приводятся к float32. При retry допустима абсолютная погрешность <=1e-6 на компоненту; сохранённые векторы при этом не переписываются. Большее отличие — конфликт, а не скрытая замена.

Last-token pooling, L2 normalization и формат query instruction следуют [официальному примеру Qwen](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B). Отдельный prompt документа не добавляется. Наши CPU/dtype/token budget/version — фиксированные решения ML1; ускорение на GPU/MPS относится к будущему профилю.

## Использование

```sh
python -m pip install -r ml/requirements-embeddings.txt
```

Веса не поставляются в репозитории. Подготовить cache вне checkout можно отдельным явным вызовом:

```python
from ml.embeddings.qwen import QwenBatchEncoder

# Выполняется отдельно при подготовке окружения, требует сети и места под веса.
QwenBatchEncoder(cache_dir='/absolute/path/outside/repository', local_files_only=False)
```

Обычный запуск работает offline-first и не скачивает отсутствующие файлы:

```python
from ml.embeddings import Embedder
from ml.embeddings.qwen import QwenBatchEncoder
from ml.adapters.postgres_chunks import save_document_version_and_chunks, save_document_embeddings

encoder = QwenBatchEncoder(cache_dir='/absolute/path/outside/repository')
embedder = Embedder(encoder, batch_size=8)

save_document_version_and_chunks(connection, saved_document, chunks)  # snapshot + полный набор, схема ml
embeddings = embedder.encode_chunks(chunks)      # модель вызывается вне транзакции БД
save_document_embeddings(connection, chunks, embeddings)

# Подготовка следующего этапа: то же пространство векторов, без поиска/фильтров.
query_vectors = embedder.encode_queries(['Что изменилось в банковском секторе?'])
```

Inference допускает несколько документов в одном вызове; persistence принимает полный набор одной пары `(document_version_id, chunking_profile_id)` за вызов, как и раньше. Пустой inference-вход даёт пустой tuple без вызова модели; пустой набор для записи отклоняется.

## Проверки и запись

- BatchEncoder получает keyed inputs и возвращает keyed vectors. Embedder проверяет полный набор ключей каждого batch, отсутствие дублей/чужих ID и восстанавливает исходный порядок независимо от порядка результата encoder.
- ChunkEmbedding immutable: UUID, SHA-256 точного исходного текста, tuple из 1024 конечных нормализованных float, модель и версия. Хеш — внутренняя привязка к тексту, не новое поле таблицы.
- NaN/Inf, zero norm, неверная размерность, пропавшие/лишние ID и ошибка любого batch прерывают весь inference-вызов без частичного результата. Unit-тесты используют fake encoder, не веса.
- `save_document_embeddings` расширяет существующий persistence adapter. Требует уже сохранённые чанки: полный набор восьми полей DTO сверяется с БД; UUID и text hash связывают вектор с точным чанком.
- Запись тройки `embedding`, `embedding_model`, `embedding_version` выполняется в одной транзакции под тем же advisory lock, что chunk persistence. Обновляются только отсутствующие тройки. Идентичный retry — no-op; иной профиль/вектор — конфликт; ошибка UPDATE откатывает все UPDATE этого вызова.
- При частично заполненных embeddings допустимо заполнить отсутствующие, если существующие совпадают. Текст, offsets, token_count, ID, created_at и search_vector не изменяются. Запись чанков и заполнение embeddings — две отдельные атомарные операции; ошибка inference не удаляет ранее сохранённые чанки.
- PostgreSQL-колонка имеет тип VECTOR(1024) в [первой ML-миграции](migrations/000001_document_storage.sql). SQL передаёт vector literal как параметр; Python pgvector adapter не нужен. Нет индекса, distance queries или retrieval. Миграции и роли применяются отдельным deployment job, не inference-процессом; [порядок подключения](PERSISTENCE.md).

Готовность версии/HTTP SUCCEEDED здесь не выставляется. Профиль не может молча заменить другую модель в одной embedding-колонке; правила переиндексации нужно определить перед retrieval.

## Тесты

```sh
# Unit-тесты доступны без весов; pooling-тестам нужен optional torch.
python -m unittest ml.tests.test_embeddings ml.tests.test_embedding_persistence -v

# Отдельная disposable БД с доступным pgvector и правом установки extension.
# Если extension уже установлен, тесты используют его без удаления.
ML1_TEST_POSTGRES_DSN='<test DSN>' python -m unittest ml.tests.test_embedding_persistence -v

# Только если закреплённые веса заранее подготовлены вне checkout.
ML1_TEST_QWEN_MODEL=1 ML1_MODEL_CACHE='/absolute/model/cache' python -m unittest ml.tests.test_embeddings.QwenRealModelSmokeTests -v
```

Integration-тесты используют fake inference и настоящий VECTOR(1024). Данные/таблицы создаются в отдельной тестовой схеме. Если pgvector ещё не установлен, extension создаётся там же и удаляется вместе со схемой; существующий extension не изменяется. Smoke использует настоящую pretrained модель строго из cache: документы/запросы, размерность/нормализация и согласованность разных batch sizes. Без явного opt-in он пропускается; включённый smoke без корректного cache должен завершиться ошибкой, а не скачивать веса.

ML-миграция, локальный снимок версии/FK и schema ml теперь реализованы в storage-этапе. Перед vector retrieval остаются применение миграций к целевой БД, политика активного embedding profile и переиндексации, правила готовности, metadata filters/доступ и SQL-адаптер существующего Retriever. Retrieval, FTS/hybrid, reranking и evaluation этим этапом не реализуются.

Проверка embedding-этапа 25 октября 2026: 125 runtime-тестов, 124 успешны, 1 smoke явно пропущен из-за отсутствия подготовленных pretrained weights. Запущены все PostgreSQL integration-тесты с настоящим VECTOR(1024) на PostgreSQL 18.4 и cached tokenizer acceptance. Дополнительно проверены loader arguments и inference на миниатюрной Qwen3 architecture со случайными весами без сети; это не smoke pretrained Qwen3-Embedding-0.6B и не проверка качества embeddings. Прошли 13 документальных тестов, check_ml_contracts.py --full (16 fixtures), check_api_contracts.py (252 local references), git diff --check. Веса не скачивались и в checkout не добавлялись. Новая ML-миграция проверяется также storage-тестами; PostgreSQL 17 пока не проверен.
