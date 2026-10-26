# Локальный запуск ML через Docker Compose

ML запускается как профильный overlay поверх основного Go Compose: Go-сервисы сохраняют свои имена, ML API и worker называются `ml-api` и `ml-worker`. Все сервисы используют общий PostgreSQL 18 с pgvector. Веса хранятся в `docker-volume/models` и доступны model-контейнерам и worker через bind mount; данные PostgreSQL хранятся в `docker-volume/postgres`.

Для локальной машины индексация использует `ML_EMBEDDING_BATCH_SIZE=1`: float32/eager embedding с пачкой из 8 чанков может исчерпать 8 ГБ RAM Docker вместе со второй моделью. Размерность, веса и контракт векторов сохраняются; меняется только размер пачки. Для повышения throughput увеличивайте пачку после выделения памяти и проверки на реальных длинах документов.

## Подготовка

Нужны Docker Engine с Compose. Для двух локальных моделей Qwen рекомендуется 12 ГБ RAM Docker; на 8 ГБ используйте пачку индексации 1 и не запускайте тяжёлые тесты одновременно с импортом. Подготовь кэш весов локально:

```bash
python3.12 -m venv .venv
.venv/bin/python -m pip install -r ml/requirements-chunking.txt
mkdir -p docker-volume/postgres docker-volume/models
ML_MODEL_CACHE="$PWD/docker-volume/models" .venv/bin/python -m ml_runtime.warm_ml1_models
```

Команда скачивает закреплённые веса в `docker-volume/models`; отдельный cache-контейнер не нужен. Затем скопируй `.env.example` в `.env`, замени `ML_LLM_API_KEY` на ключ команды от организаторов и задай локальные пароли и `ML_SERVICE_TOKEN`. Ключ остаётся только в `.env`, который исключён из Git. Для модели событий и verifier-а указаны разрешённые модели из приложения 3; заменяй их только на другие разрешённые ID.

## Скачать веса и запустить сервисы

```bash
docker compose -f compose.yaml -f compose.ml.yaml --profile ml up --build -d
docker compose -f compose.yaml -f compose.ml.yaml --profile ml ps
```

Команда скачивания кладёт закреплённые версии `Qwen/Qwen3-Embedding-0.6B` и `Qwen/Qwen3-Reranker-0.6B` в `docker-volume/models`. Compose монтирует эту папку read-only в отдельные контейнеры `embedding-model` и `reranker-model`; они загружают свои веса в память при старте и доступны только по внутренней Docker-сети. ML worker читает tokenizer из того же кэша и вызывает model-сервисы по HTTP. PostgreSQL хранит данные на диске в `docker-volume/postgres`. Для новой базы init-скрипты включают `vector`, затем Go и ML migration jobs применяют свои миграции.

API доступен локально по адресу `http://localhost:8000`. `GET /healthz` проверяет доступность HTTP-процесса. Go отправляет запросы на `POST /internal/v0.1/article-processings` с `Authorization: Bearer <ML_SERVICE_TOKEN>` и стабильным `Idempotency-Key`.

## Фронтенд → Go → ML

В профиле `ml` API Go автоматически использует `http://ml-api:8000`, общий служебный токен и `ML_ANALYSIS_CONFIG_VERSION=ml-analysis-v1`. ML-миграции выдают роли бэкенда SELECT на события и доказательства в схеме `ml`; отдельный административный DSN в Go не нужен. Фронтенд открывается на `http://127.0.0.1:13001` и по умолчанию работает в режиме `api`. Локальный Vite запускайте с `VITE_DATA_MODE=api VITE_SELF_REGISTRATION_ENABLED=true npm run dev` из `frontend/`; прокси использует API на порту 18080.

Для первого входа зарегистрируйте локальную учётную запись в интерфейсе, указав компанию из загруженного набора (например, `U_76`). Демо-логины из синтетического режима не создаются в реальной БД. Источник можно добавить командой:

```bash
docker compose -f compose.yaml -f compose.ml.yaml --profile ml exec api /app/sourcectl create /app/examples/sources/federal-reserve-press.json
```

`POST /internal/v0.1/feed-rankings` читает реальные события с готовыми документами из общего корпуса. Пока нет откалиброванной персональной модели ранжирования, карточки идут от новых к старым; они не приписывают компании неподтверждённые риски, признаки профиля или позицию банка. Темы определяются по типу события. Пустой корпус даёт пустую ленту без подстановки учебных данных. Курсор связан со снимком выдачи, пользователем, контекстом, профилем и фильтрами, действует 15 минут.

`POST /internal/v0.1/analyses` и `/follow-ups` валидируют канонический AnalysisRequest и сохраняют операцию до обращения к модели. `Idempotency-Key` должен совпадать с `request_operation_id`; повтор возвращает ту же операцию, другой запрос под тем же ключом даёт 409. HTTP-процесс отвечает 202, `GET /internal/v0.1/analyses/{analysis_id}` возвращает состояние и результат. Отдельный `ml-analysis-worker` читает через ML1 get_chunks точные чанки, по которым ML2 уже проверил событие, затем вызывает агент и независимый semantic verifier. Режим по умолчанию `ML_ANALYSIS_RETRIEVAL_MODE=event-evidence` не переэмбеддит заголовок события и не создаёт искусственных similarity scores. Для экспериментов с полным embedding/reranker поиском есть режим `semantic`, требующий достаточного CPU и бюджета времени. Уточнения используют сохранённый исходный снимок доказательств. Веса в analysis-worker не загружаются: embeddings и reranker вызываются через внутренние model-сервисы.

Завершённые результаты сохраняются в PostgreSQL и переживают перезапуск. Операция RUNNING, не завершённая за 5 минут, переходит в FAILED без повторного платного вызова; автоматического переисполнения после потери процесса нет. Проверяйте `logs -f ml-analysis-worker ml-api api`. После готовности моделей дождитесь обработки хотя бы одной статьи: только тогда появятся реальные события и разборы.

```bash
docker compose -f compose.yaml -f compose.ml.yaml logs -f api worker ml-api ml-worker embedding-model reranker-model
docker compose -f compose.yaml -f compose.ml.yaml down
```

`docker compose down` останавливает контейнеры, а содержимое `docker-volume/` остаётся на диске.

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

- Профиль `ml` задаёт Go `ML_PIPELINE_VERSION=ml-pipeline-v1`; ML API принимает эту версию и устанавливает `ML_MAX_TEXT_BYTES=131072`.
- `ML_MIGRATION_DATABASE_URL` использует административную роль только в одноразовом контейнере миграций. API и worker подключаются к БД под отдельной ролью `ml_runtime`.
- `ML_INDEXER_FACTORY` указывает на `ml_runtime.docker_indexer:create_ml1_pipeline`. `ml-worker` создаёт один долгоживущий `ML1Pipeline` с HTTP-клиентами `embedding-model` и `reranker-model`.
- `ml-api` не загружает embeddings и не требует LLM API key. `ml-worker` использует LLM gateway для извлечения событий и смысловой проверки; embedding/reranker веса загружаются отдельно в соответствующие model-контейнеры.
- Изменение паролей после первого запуска не обновляет пароль роли в существующей БД автоматически. Init-скрипты применяются только при инициализации пустой папки PostgreSQL.

## Что проверено

Локально проверены сборка образов, скачивание обеих моделей в `docker-volume/models`, запуск отдельных embedding/reranker контейнеров и их healthchecks. Production worker-клиенты успешно получили embedding и reranker score для evidence-цитаты из сохранённого пресс-релиза Банка России; embedding размерности 1024 и нормирован. Также проверены PostgreSQL/pgvector миграции и API `GET /healthz`. Полный Go → ML2 → gateway прогон статьи не выполнялся: для него нужны реальный Go payload и ключ в локальном `.env`.
