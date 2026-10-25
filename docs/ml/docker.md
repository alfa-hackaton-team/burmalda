# Локальный запуск ML через Docker Compose

Комплект запускает Go-facing API, отдельный worker, два внутренних ML1 model-сервиса и PostgreSQL 18 с pgvector. Worker отправляет embedding и reranking запросы сервисам моделей по внутренней Docker-сети; веса в worker не загружаются. Веса и база хранятся в обычных локальных папках `docker-volume/models` и `docker-volume/postgres`, смонтированных внутрь контейнеров.

## Подготовка

Нужны Docker Engine с Compose и Docker VM с 12 ГБ RAM для двух локальных моделей Qwen на CPU и inference. Подготовь кэш весов локально:

```bash
python3.12 -m venv .venv
.venv/bin/python -m pip install -r ml/requirements-chunking.txt
mkdir -p docker-volume/postgres docker-volume/models
ML_MODEL_CACHE="$PWD/docker-volume/models" .venv/bin/python -m ml_runtime.warm_ml1_models
```

Команда скачивает закреплённые веса в `docker-volume/models`; отдельный cache-контейнер не нужен. Затем скопируй `.env.example` в `.env`, замени `ML_LLM_API_KEY` на ключ команды от организаторов и задай локальные пароли и `ML_SERVICE_TOKEN`. Ключ остаётся только в `.env`, который исключён из Git. Для модели событий и verifier-а указаны разрешённые модели из приложения 3; заменяй их только на другие разрешённые ID.

## Скачать веса и запустить сервисы

```bash
docker compose up --build -d
docker compose ps
```

Команда скачивания кладёт закреплённые версии `Qwen/Qwen3-Embedding-0.6B` и `Qwen/Qwen3-Reranker-0.6B` в `docker-volume/models`. Compose монтирует эту папку read-only в отдельные контейнеры `embedding-model` и `reranker-model`; они загружают свои веса в память при старте и доступны только по внутренней Docker-сети. Worker читает tokenizer из того же кэша и вызывает model-сервисы по HTTP. PostgreSQL хранит данные на диске в `docker-volume/postgres`. Для новой базы init-скрипты включают `vector`, затем migration job применяет ML1 и ML2 миграции.

API доступен локально по адресу `http://localhost:8000`. `GET /healthz` проверяет доступность HTTP-процесса. Go должен отправлять запросы на `POST /internal/v0.1/article-processings` с `Authorization: Bearer <ML_SERVICE_TOKEN>` и стабильным `Idempotency-Key`.

```bash
docker compose logs -f api worker embedding-model reranker-model
docker compose down
```

`docker compose down` останавливает контейнеры, а содержимое `docker-volume/` остаётся на диске. Чтобы полностью очистить локальные данные, удалите `docker-volume/` вручную после остановки Compose.

## Проверка обработки

Сохраните **реальный Go payload** одной статьи в `article-processing.json` по текущей схеме из `docs/api/ml-openapi.yaml` и `docs/api/schemas.yaml`: он обязан включать `version_no`, `source.config_version`, `pipeline_version=ml-pipeline-v1`, `metadata.extraction_method` и `metadata.extraction_errors` (пустой массив, если ошибок извлечения не было). Отправьте его с тем же ключом идемпотентности, который использует Go:

```bash
set -a
source .env
set +a
ARTICLE_VERSION_ID=$(jq -r '.article.article_version_id' article-processing.json)
IDEMPOTENCY_KEY="${ARTICLE_VERSION_ID}:ml-pipeline-v1"
curl -sS -X POST http://localhost:8000/internal/v0.1/article-processings \
  -H "Authorization: Bearer ${ML_SERVICE_TOKEN}" \
  -H "Idempotency-Key: ${IDEMPOTENCY_KEY}" \
  -H 'Content-Type: application/json' \
  --data-binary @article-processing.json
```

POST возвращает `202 QUEUED`; сохраните `processing_id` и опрашивайте `GET /internal/v0.1/article-processings/{processing_id}` до `SUCCEEDED` или `FAILED`. При успехе ответ содержит `document_version_id` и `event_ids`. Если проверяете LLM-поведение, используйте материал, который можно отправлять gateway организаторов; в запросах передаётся текст статьи.

В Docker worker использует реальную ML1 индексацию и реальные вызовы двух разрешённых LLM для извлечения и смысловой проверки событий. Этот путь может расходовать дневной бюджет gateway. Не отправляйте много новых idempotency keys без необходимости.

## Служебные настройки

- `ML_PIPELINE_VERSIONS` и лимит `ML_MAX_TEXT_BYTES=131072` установлены в Compose по текущему контракту.
- `ML_MIGRATION_DATABASE_URL` использует административную роль только в одноразовом контейнере миграций. API и worker подключаются к БД под отдельной ролью `ml_runtime`.
- `ML_INDEXER_FACTORY` указывает на `ml_runtime.docker_indexer:create_ml1_pipeline`. Worker создаёт один долгоживущий `ML1Pipeline` с HTTP-клиентами `embedding-model` и `reranker-model`.
- API-контейнер не загружает embeddings и не требует LLM API key. Worker использует LLM gateway для извлечения событий и смысловой проверки; embedding/reranker веса загружаются отдельно в соответствующие model-контейнеры.
- Изменение паролей после первого запуска не обновляет пароль роли в существующем volume автоматически. Для сброса локального стенда используйте `docker compose down -v`.

## Что проверено

Локально проверены сборка образов, скачивание обеих моделей в `docker-volume/models`, запуск отдельных embedding/reranker контейнеров и их healthchecks. Production worker-клиенты успешно получили embedding и reranker score для evidence-цитаты из сохранённого пресс-релиза Банка России; embedding размерности 1024 и нормирован. Также проверены PostgreSQL/pgvector миграции и API `GET /healthz`. Полный Go → ML2 → gateway прогон статьи не выполнялся: для него нужны реальный Go payload и ключ в локальном `.env`.
