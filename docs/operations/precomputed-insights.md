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

## Разбор отказов grounding

Миграция `000025` добавляет внутренний `request_operations.analysis_diagnostics`.
Новый precompute сохраняет данные и для FAILED: запрос/ответ поиска, кандидаты,
фактически переданные reranker (до Top-20), его scores, финальный Top-5,
контрактный generated_response, фактически проверяемый candidate_response и
verifier_verdict, если соответствующий этап
выполнился. `evidence_snapshot` сохраняет точные chunks, переданные агенту.
Нет нового retrieval после отказа, скрытых рассуждений или provider credentials.
Публичные Insight и Generation эти диагностические поля не возвращают.
Старые отказы без trace невозможно достоверно восстановить задним числом.

При анализе отказа сначала сопоставить `searches[].reranker_calls[].candidates`
с `searches[].response.results` и `evidence_snapshot`, затем claims/quote_text
из candidate_response с причиной проверки. Наличие факта в исходном документе
не доказывает его наличие в Top-5 данного запуска. Если подтверждение есть в
точном evidence, исправлять generation/grounding; иначе фиксировать retrieval
miss, не подменять evidence новым поиском.

Генератор получает дословные варианты цитат с неизменёнными числами, отрицаниями,
пунктуацией и переносами. Это варианты цитирования, а не автоматически проверенные
claims. Неверные цитаты не исправляются после генерации. Summary собирается дословно
из candidate claims до semantic verification; claims и их knowledge types не
переписываются и не удаляются. Verifier проверяет именно этот итоговый summary. Требование support
присутствует и в generation schema, и в прежнем verifier. Недостаток оснований
о персональном эффекте описывается в gaps/warnings, а не context-only claim.

## Ограниченная проверка перед demo

Из корня репозитория с настроенным production gateway выполнить:

```sh
python -m ml.evaluation.demo_checks --output .local/demo-checks.json
```

При необходимости указать `--tokenizer-cache` с существующим offline-кешем.
Команда один раз проверяет h01/h08/h16 существующим ML2 runner с обязательным
production verifier. Embeddings, reranker и ML1 suite не запускаются. Evidence
берётся из трёх явно выбранных документов `official_holdout_v1.json`; проверяются
замороженные chunk IDs/hashes. Это текущий воспроизводимый набор, не восстановление
исторического Top-5 и не проверка retrieval. Контекст — тестовая fixture, не demo-пользователь.
Повтор с существующим output запрещён; автоматических повторов вызовов нет.

После выполнения вручную сопоставить claims и цитаты с `ml2_evidence`:
h01 — повышение на 200 б.п. до 18,00%; h08 — прогноз ЦБ на 2026 год
(ставка 13,0–15,0%, инфляция 4,0–5,0%), без позиции Альфа-Банка;
h16 — `insufficient_evidence`, без подмены ставки депозита ключевой ставкой.
Проверить также опубликованную карточку реального demo-контекста: числа,
отрицания, источник каждой цитаты, разделение фактов и банковских прогнозов,
limitations персональной применимости. Локальные отчёты остаются в `.local/`.

Для persisted GET открыть discovery и карточку с действующей сессией, записать
30 чтений и счётчик ML-вызовов, один раз перезапустить Go API и повторить чтение.
Payload должен совпасть, счётчик ML — остаться нулевым, число операций/попыток
анализа — не измениться. HTTP-проверка JSON не подтверждает browser integration.
