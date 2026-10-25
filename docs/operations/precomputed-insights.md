# Подготовленные demo-insights

Явный предрасчёт выбранного набора: существующий AnalysisService → обязательный
production semantic verifier → PostgreSQL → авторизованный Go GET.
Это служебная подготовка, а не автоматический fan-out после ingestion.

## Окружение и запуск

Работать из корня репозитория. Нужны актуальные Go migrations, импортированные
компании, активные профили с действительным provenance, пользователи с context
grants, индексированные события и неизменяемые версии evidence. UUID fixtures
не являются demo-пользователями.

Собрать служебный бинарник (для контейнера — Linux и соответствующая архитектура):

```sh
cd backend
CGO_ENABLED=0 go build -o /tmp/precompute ./cmd/precompute
CGO_ENABLED=0 go build -o /tmp/migrate ./cmd/migrate
```

Запустить `/tmp/migrate` с обычной Go-конфигурацией. Команда предрасчёта должна
выполняться в окружении ML worker, где доступны исходники этого checkout,
Python-зависимости, pinned tokenizer cache, embedding/reranker services и gateway.
Go API сам Python и модели не загружает.

Переменные команды:

- `DATABASE_URL`: Go database с request_operations/insights и context/auth tables.
- `ML_DATABASE_URL`: существующий ML runtime DSN; не подменяется Go DSN.
- `ML_READ_DATABASE_URL`: опциональный Go DSN для чтения отдельной ML database.
- `BACKEND_CONFIG_FILE`: опциональный путь к существующей Go runtime configuration.
- `ML_ANALYSIS_CONFIG_VERSION`: явная версия processing config, например `analysis-demo-v1`.
- `ML_LLM_API_KEY`, `ML_LLM_BASE_URL`, опциональные `ML_LLM_MODEL` и
  `ML_LLM_VERIFIER_MODEL`: существующие разрешённые production настройки.
- `ML_MODEL_CACHE`/`HF_HOME`, `HF_HUB_OFFLINE=1`, `ML1_EMBEDDING_URL`,
  `ML1_RERANKER_URL`: действующая ML1 конфигурация. Параметры Top-20/Top-5 не меняются.

Из корня checkout, с бинарником внутри этого окружения:

```sh
/tmp/precompute --user-id "$DEMO_USER_ID" \
  --context-id "$DEMO_CONTEXT_ID" --event-id "$DEMO_EVENT_ID" \
  --set-version demo-v1 --python python3
```

Для ограниченного списка явно повторить эту команду для каждого разрешённого
user/context/event. Команда выводит JSON с generation_id, state, insight_id,
elapsed_seconds и reused; код выхода 0 означает готовый Insight.
Никаких секретов не передавать через tracked файлы. Не сохранять сырой stderr
провайдера или скрытые рассуждения модели.

## Идентичность и восстановление

Fingerprint включает владельца, полный доверенный context/profile/role, снимок
события, версию набора, processing config, runtime manifest и hash разрешённого
корпуса. Manifest содержит модели, gateway из allowlist, версии зависимостей и
hash исходников/prompt/contracts/config. Доступ ограничен точным allowlist chunks
выбранного события. Снимок chunks фиксируется перед вызовом AnalysisAgent;
повторного поиска evidence после анализа нет. Цитаты, версии документов и
публичные claims берутся из данного запуска.

Повтор подходящего SUCCEEDED возвращает сохранённую операцию. В одном frozen
слоте user/context/event/set изменённые входы дают CONFLICT: подготовить новую
версию набора. FAILED и INSUFFICIENT_EVIDENCE сохраняются без публикации Insight.
Повтор этих состояний не делает нового платного вызова.

RUNNING после падения — неизвестный исход, даже после истечения lease. Ошибка
процесса записывается FAILED с UNKNOWN_OUTCOME, если база доступна. Обычный API
worker исключает demo-операции из очереди. Нет автоматического повторного
LLM execution; сначала оператор проверяет gateway/БД и принимает явное решение о
новом наборе. Это не exactly-once. Ошибка транзакции не оставляет Insight или
часть evidence; SUCCEEDED и готовый Insight публикуются одним commit транзакции.

## API handoff

На Go API установить `DEMO_INSIGHT_SET_VERSION=demo-v1` и перезапустить API с
обычными session/cursor/origin настройками. Без этой переменной demo-карточки
не выдаются как актуальные.

1. `GET /v0.1/contexts` — доступные контексты текущей cookie-сессии.
2. `GET /v0.1/contexts/{context_id}/insights` — `{ "items": [Insight, ...] }`.
   Не более 100 готовых карточек выбранного набора, от новых к старым. Пустой
   массив — подходящего результата нет. Это не полная лента и не пагинация.
3. `GET /v0.1/insights/{insight_id}` — существующий публичный Insight:

```json
{
  "insight_id": "UUID",
  "revision": "1",
  "primary_event": {"event_id": "UUID", "title": "...", "summary": "...", "occurred_at": null},
  "supporting_event_ids": [],
  "context": {"context_id": "UUID", "kind": "COMPANY", "company_id": "UUID"},
  "role_id": "cfo",
  "profile": {"profile_id": "UUID", "version": "1"},
  "created_at": "2026-10-26T00:00:00Z",
  "response": {
    "trace_id": "UUID", "claims": [], "evidence": [], "sections": [],
    "limitations": [], "model_config_ref": "analysis-demo-v1"
  }
}
```

Пример показывает форму, не готовый verified результат. В готовом ответе claims
и evidence непустые. Точный DTO — `docs/api/schemas.yaml#Insight`.

Статусы:

| HTTP | Значение |
|---|---|
| 200 | Сохранённый Insight либо список items |
| 401 UNAUTHENTICATED | Нет действующей сессии; revoked/expired/disabled user |
| 403 FORBIDDEN_CONTEXT | Нет действующего grant для компании/группы и роли |
| 404 NOT_FOUND | Нет опубликованного результата или он принадлежит другому пользователю |
| 409 CONFLICT | Сохранённый профиль/context/роль/набор несовместим с текущим |
| 409 PROFILE_NOT_READY | Discovery для неактивного/некорректного профиля |

Для открытия подготовленной карточки вызывающая сторона выбирает сохранённый
insight_id из discovery по primary_event.event_id, затем делает GET. Не вызывать
POST /insights, generation polling или fallback на ML, если готового результата
нет. Отображать claims, evidence, sections и limitations из response.
Frontend по указанию пользователя не изменяется; browser integration не проверена.

## Freshness и измерения

Совместимость проверяется при каждом чтении: сессия, владелец, текущий grant,
company/group, роль, активный профиль, его ID/версия/содержимое и версия demo-набора.
Новый профиль требует нового предрасчёта и новой версии набора. Изменение набора
активируется явно через `DEMO_INSIGHT_SET_VERSION`; старый Insight получает 409,
исторический статус не вводится. Fingerprint не обнаруживает новые новости.
Никакой автоматической semantic freshness или мгновенного нового вопроса нет.

Успешный GET возвращает Server-Timing: session (проверка сессии), db (чтение
сохранённого результата), access (проверка текущего context/grant), serialization
(JSON encoding), app (обработка Go без session/network). Декодирование сохранённого
DTO и проверка совместимости входят в app. End-to-end HTTP измерять отдельно;
заголовки не включают браузер и сеть. Для ориентировочной серии достаточно 30 GET,
отдельно записать p50/p95, условия и число ML-вызовов (должно быть 0).

Проверки: `go test ./...` из backend; с TEST_DATABASE_URL интеграции создают и
удаляют отдельные базы. Новый `TestPrecomputedInsightPostgres` проверяет полную
публикацию, повтор, новый pool/store после restart, ACL, freshness, отсутствие ML
при GET, failures/insufficient и rollback финального success update.
`python -m unittest discover -s ml_runtime/tests` включает обязательный verifier
и точный capture evidence; существующие ML1/ML2 suites сохраняются.
