# Vector retrieval baseline

`PostgresRetriever` реализует существующий `Retriever.search/get_chunks` и DTO v1, без HTTP и новых миграций.

Путь: `SearchRequest.query → Embedder.encode_queries → vector(1024) → pgvector <=> → SearchResponse.results`.
Документы кодируются прежним `encode_chunks` и сохраняются через `save_document_embeddings`.
Query instruction, tokenizer и revision берутся из неизменяемого embedding profile `ml1-qwen3-embedding-1024-v1`.

```python
from ml.adapters.postgres_retrieval import PostgresRetriever
from ml.database import connect_runtime
from ml.embeddings import Embedder
from ml.embeddings.qwen import QwenBatchEncoder

with connect_runtime() as connection:
    retriever = PostgresRetriever(
        connection, Embedder(QwenBatchEncoder()),
        versions=trusted_version_catalog, context_chunks=trusted_context_allowlists,
    )
    response = retriever.search(search_request)  # SearchRequest v1
```

- Поиск точный, без ANN-индекса: cosine distance по возрастанию, при равенстве — UUID. `retrieval_score = 1 - distance`; больше — ближе, это не вероятность. `top_k`: 1–50. Абсолютного порога нет.
- Допускаются только chunking `v1` и текущие embedding model/version. Все чанки активного профиля версии должны иметь совместимые ненулевые embeddings.
- Allowlist, выбор последней версии, готовность, source/date filters применяются до `LIMIT`. Неизвестный context запрещён; `None` требует явно заданного service allowlist.
- В текущем DDL нет `version_no/ready`: приложение передаёт полный доверенный каталог `DocumentVersion`. Последняя версия выбирается до проверки готовности; fallback на старую запрещён. При неизвестном сохранённом snapshot поиск документа закрывается до обновления каталога. Каталог нельзя брать из поискового запроса.
- `get_chunks` читает точные исторические UUID независимо от текущего embedding profile/готовности, с тем же контролем доступа.
- Пустой title отображается как «Без заголовка» с warning: snapshot и текст чанка не меняются. Пустая выдача отличается от ошибки модели/БД.

## Проверки

```sh
ML1_TEST_POSTGRES_DSN='<disposable admin test DSN>' python -m unittest ml.tests.test_postgres_retrieval -v
# Реальная модель из HF cache; без загрузки весов по сети:
ML1_TEST_POSTGRES_DSN='<disposable admin test DSN>' ML1_TEST_QWEN_MODEL=1 HF_HUB_OFFLINE=1 python -m unittest ml.tests.test_postgres_retrieval -v
```

Тесты создают изолированные схемы существующей миграцией; pgvector — prerequisite тестовой БД. Проверяются семантический порядок реальной Qwen, SQL ranking/top_k, несовместимые/неполные embeddings, пустая база, фильтры, access и историческое чтение.

Воспроизводимый synthetic baseline — в [evaluation/README.md](evaluation/README.md): реальные Qwen + pgvector с fixture-каталогом и trusted allowlist. Production-каталог версий/готовности и права приложения подключаются отдельно и не блокируют evaluation. Автоматического `ready/SUCCEEDED` и abstention threshold нет. Масштабирование через ANN требует отдельного решения.
