# ML v1 и API v0.1: границы и преобразование

Статус: рабочее решение редакции 1.1 до первого выпуска ML v1. Адаптеры и миграции ещё не реализованы. Изменения фиксируют контракт для разработки, а не подтверждают готовность интеграции.

## Источники истины

| Область | Документ | Правило |
|---|---|---|
| ML1 ↔ ML2, вход/выход анализа | `contracts/v1/contracts.schema.json` и `contracts.md` | Канонические логические DTO v1 |
| Go ↔ ML по HTTP | `../api/ml-openapi.yaml` | Для анализа/уточнения ссылается на те же DTO v1; путь HTTP v0.1 не означает другую форму сообщения |
| Браузер ↔ Go | `../api/openapi.yaml`, `../api/schemas.yaml` | Публичные DTO v0.1; `AgentResponse` — представление результата для клиента |
| Хранение | `../rfc/database_scheme.md` | Целевая схема, не выполненные миграции |
| Владельцы | `architecture.md`, разделы 2 и 4 | Два ML-разработчика; обязанности прежнего ML3 распределены явно |
| Старый подробный план | `../rfc/ml_work_plan_and_contracts.md`, `reference/*` | Историческое обоснование; не переопределяет актуальные DTO |

`contract_version: v1` относится к логическим ML-сообщениям. Версия публичного API остаётся v0.1. Этот комплект ещё не выпущен: новые обязательные поля входят в редакцию 1.1 контракта v1, а не добавляются скрытно после фиксации версии. После выпуска несовместимые изменения требуют новой версии.

## Приём материала от Go

`article_id`, `article_version_id` и `source_id` — UUID. `raw_ref` имеет вид `raw-artifact:<UUID>` и ссылается на сохранённый оригинал. `article.version_no` — положительный номер из `article_versions.version_no`: начинается с 1, увеличивается для новой версии внутри статьи и не меняется при повторах или смене пайплайна. Миграция `000021_ml_version_number.sql` фиксирует его в новых и ещё не отправленных заданиях. Ранее отправленные задания сохраняют прежний payload без этого поля, чтобы не изменить параметры уже принятой операции.

При настроенной версии пайплайна каждая новая сохранённая версия статьи получает ML-задание в той же транзакции. Go не принимает решение о прохождении quality gate или готовности к поиску: это результат ML. Технические ограничения загрузки, извлечения и размера запроса продолжают действовать; материал сверх лимита не обрезается и не отправляется.

Go отправляет `NormalizedArticle` через `/internal/v0.1/article-processings`. Согласованный предел `ML_MAX_TEXT_BYTES=131072` считается по `article.body` в байтах UTF-8 без заголовка; превышение даёт `413 PAYLOAD_TOO_LARGE` без обрезания. Конкретная версия пайплайна ещё не объявлена ML-командой. До её подтверждения Go оставляет `ML_PIPELINE_VERSION` пустой и не создаёт новые ML-задания. При развёртывании выбранное значение Go должно входить в список `ML_PIPELINE_VERSIONS` ML-сервиса; иначе POST отклоняется с 422. Принятие версии проверяется запросом в целевое ML-окружение, а не только совпадением настроек.

Для новых заданий `article.metadata` содержит `extraction_method` (`full_article`, `rss_summary` или `pdf_extracted`) и `extraction_errors` — список известных кодов проблем, пустой `[]` при отсутствии известных ошибок. Поля формируются из сохранённого снимка происхождения текста; неизвестное происхождение или несопоставимая ошибка останавливают отправку с диагностикой. Миграция `000020_ml_extraction_snapshot.sql` добавляет снимок новым заданиям и ранее не отправлявшимся заданиям без попыток. Уже отправленные старые задания сохраняют прежний payload, включая `{}`, чтобы не менять параметры под прежним ключом идемпотентности; их совместимость с новым валидатором требует отдельного решения.

Объект `source` отдельно содержит `source_id`, `name`, `source_type`, `source_role`, `trust_level` и `config_version`; Go сохраняет этот снимок в задании. Повтор с тем же `Idempotency-Key` отправляет прежний снимок, даже если реестр источников изменился. `pipeline_version` передаётся в POST, входит в ключ `(article_version_id, pipeline_version)` и возвращается без изменения в ответе POST/GET. Адрес тестового сервиса, токен и пример реального успешного прогона пока не опубликованы в репозитории.

## Вход анализа и уточнения

Go проверяет пользователя и права, разрешает публичные идентификаторы в UUID хранения, сохраняет `request_operations` и формирует `AnalysisRequest`. `Idempotency-Key` внутреннего HTTP-запроса равен `request_operation_id` в теле. Несовпадение отклоняется до вызова модели. Сессионные данные и произвольный `role_id` из клиентского JSON не используются как доверенный контекст.

`profile_version` получается из сохранённого профиля; `role_code` — из проверенной роли контекста. Нельзя получать их слепым приведением внешней строки к числу или enum. Снимок события и версии документов фиксируются в операции. HTTP `/analyses` принимает только initial/reanalyze, `/follow-ups` — только follow_up.

Для follow_up Go загружает сохранённый исходный insight, контекст, событие, claims, evidence и точные исторические чанки. Они передаются в `follow_up.initial_insight`. `initial_insight_id` должен совпасть с ID вложенного insight. История до `history_through_message_id` включает структурированные claims/citations и чанки ответов; новый вопрос — последнее сообщение. Клиент не конструирует этот снимок.

Исходный профиль и событие берутся из сохранённой операции первоначального разбора, а не из текущего состояния компании/события. Недоступные доказательства нельзя заменять похожими свежими чанками. Если снимок не восстановлен, адаптер возвращает контролируемую ошибку; запрос не передаётся агенту с выдуманным контекстом.

Каждое новое выполнение имеет собственный `trace_id`. Уточнение получает `parent_trace_id` исходного анализа. При повторе одной операции ML возвращает первоначальные идентификаторы, даже если транспорт принёс новый trace; такой транспортный trace связывается с сохранённым в журнале. Проверка совпадения trace в fixtures относится к первому выполнению, не разрешает создание второго запуска на повторе.

## Claim: три независимых признака

| ML v1 | Назначение | Публичный Claim v0.1 |
|---|---|---|
| `claim_type`: relevance/impact/consequence/action/bank_view | Роль утверждения в бизнес-разборе | Используется при формировании разделов, не копируется в публичный `claim_type` |
| `knowledge_type`: FACT/ALFA_ANALYTICS/AI_INFERENCE/FORECAST | Факт, аналитика банка, вывод модели или прогноз | Копируется в публичный `claim_type` |
| `verification_status`: SUPPORTED/PARTIALLY_SUPPORTED | Результат проверки опубликованного утверждения | Копируется в `verification_status` |

Публичное UNSUPPORTED допустимо для внутренней проверки старого DTO, но не передаётся как опубликованный claim ML v1. PARTIALLY_SUPPORTED требует явных `assumptions` и условной формулировки. Прогноз с проверенными основаниями остаётся прогнозом. Тип FACT нельзя назначать по наличию ссылки или высокому confidence.

`id → claim_id`, `claim_text → text`; `assumptions` передаются без потери. Nullable `time_horizon` при null не включается в публичный Claim, где это необязательная строка. `confidence` не превращается в иной публичный процент. Для сохранения этих признаков в целевой `insight_claims` описаны отдельные поля; миграция должна быть реализована до интеграции, терять их при записи запрещено.

## Evidence и представление результата

Backend создаёт стабильные ID записей `claim_evidence`; они используются как публичные `evidence_id`. Повторная доставка результата не создаёт новые evidence. Один источник с разными цитатами/ролями может дать разные записи: нельзя объединять доказательства только по `chunk_id`.

| Основание ML v1 | Данные для публичного evidence |
|---|---|
| document_chunk | По проверенному chunk_id загрузить source_id, document_version_id, точный quote_text, published_at и retrieved_at → fetched_at |
| profile_feature | По profile_feature_id и зафиксированному профилю получить FeatureRef и происхождение признака |
| evidence_role | support/contradict/context → SUPPORT/CONTRADICT/CONTEXT в evidence_links |

Материал банка становится ALFA_ANALYTICS только при подтверждённом source_role=bank; остальные материалы — EXTERNAL_NEWS. Признак компании — COMPANY_DATA. Для Provenance нужны исходный source_snapshot_id, record_refs и derivation_version, для групп — подтверждённые contributing_company_ids. Их предоставляет импорт/построение профиля через source_ref либо связанный снимок данных. Отсутствующие значения не выдумываются ради сериализации. Синтетический fixture использует синтетическое происхождение.

`AgentResponse` собирается из проверенного результата: claims, evidence, sections, limitations, trace_id, model_config_ref. Последний берётся из processing_config_version сохранённой операции. Предупреждения и gaps превращаются в limitations с явным code/message/related_claim_ids (пустой список, если ограничение общее).

Sections ссылаются только на существующие claim_ids. Рабочее правило: action → SCENARIOS_ACTIONS, relevance → WHY_RELEVANT, bank_view/ALFA_ANALYTICS → BANK_VIEW, FACT → WHAT_HAPPENED; impact/consequence при impact_kind=risk → RISKS, при opportunity → OPPORTUNITIES, при mixed/neutral → AI_ANALYSIS без выдуманного разделения риска и возможности. Одно утверждение может входить в несколько подходящих разделов. Весь существенный текст summary/consequences/recommended_actions должен быть покрыт claims до преобразования; адаптер не генерирует новые факты.

Публичный Insight получает insight_id/revision из id/version (revision — строковое представление номера), primary_event и context/profile/role_id из сохранённого запроса и доверенного отображения ID, created_at из слоя записи. supporting_event_ids остаётся [] в первом сценарии. Не все внутренние поля имеют публичный аналог; исходный результат сохраняется для аудита вместе с версиями конфигурации.

## Статусы и сохранение

- `AnalysisResponse.status=succeeded` означает проверенный подготовленный результат ML. Только Go после транзакционного сохранения выставляет публичное SUCCEEDED.
- `insufficient_evidence` не создаёт verified-insight. Gaps доступны в ограничениях/причине операции; сообщение разрешено только для существующего диалога.
- `failed` означает контролируемую ошибку вычисления. Ошибка аутентификации/валидации транспорта возвращается HTTP-ошибкой до запуска.
- `AnalysisOperation.response` содержит полный AnalysisResponse; при terminal state статус оболочки согласован с response.status. QUEUED/RUNNING ещё не имеют response. Внутренний analysis_id соответствует analysis_run_id; внешний generation_id — ID операции Go.
- Backend одной транзакцией сохраняет публичный результат, claims, evidence, citations и статус операции. ML сохраняет подготовленный результат надёжно до подтверждения; механизм хранения и lease выбираются совместно до I05/I06.

## Приёмка адаптера

До I05 проверить: преобразование AI_INFERENCE/action и FORECAST/impact без потери смысла; отображение partial support и assumptions; реальные provenance и банковские источники; восстановление сохранённого разбора после рестарта; отсутствие второго запуска/набора evidence при повторе; совпадение публичного ответа с публичной схемой. Проверка fixtures ML не заменяет эти тесты будущего адаптера.

## Explicit demo precompute

`backend/cmd/precompute` runs the existing synchronous Python `AnalysisService`
with the production `LLMSemanticVerifier`; it does not require or introduce an
analysis HTTP service. The command persists the exact agent evidence alongside
`AnalysisResponse`, then reuses Go's public DTO projection. The existing
`GET /v0.1/insights/{insight_id}` reads only PostgreSQL. A bounded discovery route,
`GET /v0.1/contexts/{context_id}/insights`, lists up to 100 compatible saved
Insights from the server-selected `DEMO_INSIGHT_SET_VERSION`.

Optional source metadata on public `MaterialEvidence` preserves the exact URL,
title, source name, role, type and trust level from that analysis snapshot.
The public Insight remains distinct from the internal AnalysisResponse.
See [runbook](../operations/precomputed-insights.md) for freshness and failures.
