# Локальный запуск ML через Docker Compose

Комплект запускает API, отдельный worker и PostgreSQL 18 с pgvector. API принимает задания Go и обслуживает polling; worker загружает локальную ML1 embedding-модель, индексирует документы и вызывает разрешённые LLM через gateway организаторов. Код ML1 PR #24 должен быть в том же checkout (например, после его слияния с ML2).

## Подготовка

Нужны Docker Desktop с Docker Compose v2 и примерно 5 ГБ свободной памяти для Python, PyTorch и модели Qwen. В корне репозитория:

```bash
cp .env.example .env
```

Откройте `.env`, замените `ML_LLM_API_KEY` на ключ команды от организаторов и задайте локальные пароли и `ML_SERVICE_TOKEN`. Ключ остаётся только в `.env`, который исключён из Git. Для модели событий и verifier-а указаны разрешённые модели из приложения 3; заменяйте их только на другие разрешённые ID.

## Подготовить embedding model и запустить сервисы

```bash
docker compose --profile setup run --rm model-cache
docker compose up --build -d
docker compose ps
```

Первая команда скачивает закреплённую версию `Qwen/Qwen3-Embedding-0.6B` в именованный volume `hf-model-cache`; она не использует ключ LLM gateway. Worker работает офлайн относительно Hugging Face и не скачивает модель при обработке статей. Следующая команда собирает общий Python image, включает pgvector, последовательно запускает миграции ML1 и ML2, а затем поднимает API и worker. Для новой пустой базы init-скрипты включают расширение `vector` и создают отдельную роль `ml_runtime`.

API доступен локально по адресу `http://localhost:8000`. `GET /healthz` проверяет доступность HTTP-процесса. Go должен отправлять запросы на `POST /internal/v0.1/article-processings` с `Authorization: Bearer <ML_SERVICE_TOKEN>` и стабильным `Idempotency-Key`.

```bash
docker compose logs -f api worker
docker compose down
```

`docker compose down` сохраняет PostgreSQL и веса моделей в volumes. `docker compose down -v` удаляет и базу, и скачанную модель; используйте это только для сброса локальных данных.

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
- `ML_INDEXER_FACTORY` указывает на `ml_runtime.docker_indexer:create_ml1_pipeline`. Worker создаёт один долгоживущий `ML1Pipeline`.
- API-контейнер не загружает embeddings и не требует LLM API key; модельные зависимости и ключ нужны только worker-у.
- Изменение паролей после первого запуска не обновляет пароль роли в существующем volume автоматически. Для сброса локального стенда используйте `docker compose down -v`.

## Текущий предел проверки

Docker Engine отсутствует в среде разработки, поэтому `docker compose build/up` здесь не выполнялся. YAML и сценарии проверены статически, сквозной код — на объединённом дереве PR24+ML2. Реальную сборку нужно выполнить на компьютере с Docker Desktop. Пока ML1 PR #24 не находится в checkout, Dockerfile не соберётся: он намеренно использует настоящие ML1 зависимости, pipeline и миграции.
