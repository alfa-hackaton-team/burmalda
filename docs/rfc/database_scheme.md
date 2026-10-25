# Схема базы данных



Уточнение от 24 октября 2026: этот RFC — целевой SQL-проект ML, а не описание уже выполненных Go-миграций. Фактические ingestion-таблицы Go — sources/articles/article_versions/raw_artifacts/processing_jobs. document_id/document_version_id логических DTO сохраняют ID article/article_version; второй комплект ingestion-таблиц не требуется. [Отображение и ограничения](../ml/article-processing.md) имеют приоритет для текущего входа R02. Оригиналы Go могут храниться в PostgreSQL, поэтому S3-поля и ключи ниже — целевой вариант, не обязательный вход R02. Здесь content_hash следует читать как отпечаток содержимого по правилам Go (заголовок, body, дата), не безусловный SHA-256 только normalized_text. При реализации реального хранения требуется отдельное согласование миграций и FK.

Основная БД — PostgreSQL с расширением `pgvector`.

- Реляционные таблицы используются для основных сущностей и связей.
- `JSONB` используется для изменяемых и вложенных данных.
- `vector` используется для семантического поиска по фрагментам документов.
- Оригинальные PDF и HTML хранятся в приватном S3-совместимом object storage; в БД сохраняются ключ объекта, версия, хеш и извлеченный текст.

## 1. Таблицы

### 1.1. `companies`

Данные анализируемых компаний.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `synthetic_uid` | TEXT | UNIQUE, NOT NULL |
| `org_type` | TEXT | |
| `main_okved_code` | TEXT | |
| `main_okved_name` | TEXT | |
| `region` | TEXT | |
| `city` | TEXT | |
| `revenue` | NUMERIC | |
| `workers_count` | INTEGER | |
| `has_rko` | BOOLEAN | |
| `has_loan` | BOOLEAN | |
| `has_leasing` | BOOLEAN | |
| `has_deposit` | BOOLEAN | |
| `has_salary_project` | BOOLEAN | |
| `has_factoring` | BOOLEAN | |
| `raw_profile` | JSONB | NOT NULL, DEFAULT `{}` |
| `created_at` | TIMESTAMPTZ | NOT NULL |
| `updated_at` | TIMESTAMPTZ | NOT NULL |

### 1.2. `company_groups`

Группы компаний, для которых может строиться агрегированный контекст.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `external_id` | TEXT | UNIQUE, nullable |
| `name` | TEXT | |
| `created_at` | TIMESTAMPTZ | NOT NULL |

### 1.3. `company_group_members`

Состав группы компаний.

| Поле | Тип | Ограничения |
|---|---|---|
| `group_id` | UUID | PK, FK → `company_groups.id` |
| `company_id` | UUID | PK, FK → `companies.id` |
| `is_primary` | BOOLEAN | NOT NULL, DEFAULT `false` |
| `valid_from` | DATE | nullable |
| `valid_to` | DATE | nullable |

### 1.4. `business_profiles`

Версионируемый профиль отдельной компании или агрегированный профиль группы.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `subject_type` | TEXT | `company`, `group` |
| `company_id` | UUID | FK → `companies.id`, nullable |
| `group_id` | UUID | FK → `company_groups.id`, nullable |
| `version` | INTEGER | NOT NULL |
| `status` | TEXT | `draft`, `active`, `superseded` |
| `profile_data` | JSONB | NOT NULL, DEFAULT `{}` |
| `generated_by_run_id` | UUID | FK → `analysis_runs.id`, nullable |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Заполняется ровно одно из полей: `company_id` или `group_id`. Старые версии профиля не перезаписываются.

### 1.5. `business_profile_features`

Отдельные признаки профиля, на которые можно сослаться из конкретного вывода.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `profile_id` | UUID | FK → `business_profiles.id` |
| `feature_key` | TEXT | NOT NULL |
| `feature_value` | JSONB | NOT NULL |
| `source_type` | TEXT | `company_field`, `person_link`, `payment`, `derived` |
| `source_ref` | JSONB | NOT NULL, DEFAULT `{}` |

Уникальность: `(profile_id, feature_key)`.

### 1.6. `business_contexts`

Контекст, для которого формируется insight: компания или группа плюс роль пользователя и версия профиля.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `subject_type` | TEXT | `company`, `group` |
| `company_id` | UUID | FK → `companies.id`, nullable |
| `group_id` | UUID | FK → `company_groups.id`, nullable |
| `profile_id` | UUID | FK → `business_profiles.id` |
| `role_code` | TEXT | `owner`, `ceo`, `cfo`, `chief_accountant`, `treasury`, `other` |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Заполняется ровно одно из полей: `company_id` или `group_id`.

Центральная связь персонализации: `BusinessContext(Company|Group, ProfileVersion, Role) + Event → Insight`.

### 1.7. `persons`

Обезличенные физические лица.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `synthetic_fid` | TEXT | UNIQUE, NOT NULL |
| `city` | TEXT | |
| `region` | TEXT | |

### 1.8. `company_person_links`

Связи компаний и физических лиц.

Связь оставляем many-to-many: один человек может быть владельцем, директором или представителем нескольких юридических лиц, а у одной компании может быть несколько связанных лиц. Если исходный набор гарантирует уникальность `person → company`, это ограничение можно добавить отдельно, но схема не должна предполагать его без подтверждения.

| Поле | Тип | Ограничения |
|---|---|---|
| `company_id` | UUID | PK, FK → `companies.id` |
| `person_id` | UUID | PK, FK → `persons.id` |
| `relation_type` | TEXT | PK |

### 1.9. `payment_edges`

Агрегированные платежные связи компаний.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `payer_company_id` | UUID | FK → `companies.id` |
| `beneficiary_company_id` | UUID | FK → `companies.id` |
| `payment_amount` | NUMERIC | NOT NULL |
| `payment_count` | BIGINT | NOT NULL |
| `period_start` | DATE | |
| `period_end` | DATE | |

### 1.10. `sources`

Источники документов.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `name` | TEXT | NOT NULL |
| `source_type` | TEXT | `news`, `bank_analytics`, `expert`, `regulator` |
| `source_role` | TEXT | `external`, `bank`, `expert` |
| `base_url` | TEXT | |
| `ingestion_type` | TEXT | `rss`, `api`, `html`, `pdf`, `csv`, `manual` |
| `feed_url` | TEXT | |
| `trust_level` | TEXT | `primary`, `trusted`, `standard`, `unknown` |
| `is_active` | BOOLEAN | NOT NULL, DEFAULT `true` |
| `last_success_at` | TIMESTAMPTZ | |

Числовой score не вводится до появления измеримой методики. `trust_level` задает понятную категорию: первоисточник, доверенный источник, обычный источник или источник без оценки.

### 1.11. `documents`

Стабильная запись документа независимо от изменений его содержимого.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `source_id` | UUID | FK → `sources.id` |
| `external_id` | TEXT | UNIQUE внутри источника |
| `canonical_url` | TEXT | UNIQUE |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Уникальность: `(source_id, external_id)` и `canonical_url`.

### 1.12. `document_versions`

Реализованный минимум ML отличается от целевой таблицы ниже: `ml.document_versions(id UUID PK, document_id UUID NOT NULL, snapshot JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`. Snapshot сохраняет canonical R02 без execution-полей trace_id/pipeline_version; прямых FK на Go нет. [Миграция](../../ml/migrations/000001_document_storage.sql) и [запуск/права](../../ml/PERSISTENCE.md). Остальные поля раздела — целевой RFC, не текущий DDL.

Версии содержимого документа. При изменении текста создается новая строка; предыдущая версия не перезаписывается.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `document_id` | UUID | FK → `documents.id` |
| `version_no` | INTEGER | NOT NULL |
| `title` | TEXT | NOT NULL |
| `published_at` | TIMESTAMPTZ | |
| `retrieved_at` | TIMESTAMPTZ | NOT NULL |
| `document_type` | TEXT | NOT NULL |
| `s3_bucket` | TEXT | NOT NULL для сохраненного оригинала |
| `s3_object_key` | TEXT | NOT NULL для сохраненного оригинала |
| `s3_version_id` | TEXT | Версия объекта в S3, nullable |
| `mime_type` | TEXT | Например `text/html`, `application/pdf` |
| `size_bytes` | BIGINT | Размер оригинала |
| `checksum_sha256` | TEXT | Хеш оригинального файла |
| `content_hash` | TEXT | Отпечаток содержимого по правилам Go: title, body и published_at; не только normalized_text |
| `normalized_text` | TEXT | NOT NULL |
| `metadata` | JSONB | NOT NULL, DEFAULT `{}` |
| `processing_status` | TEXT | `new`, `parsed`, `embedded`, `analyzed`, `failed` |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Уникальность: `(document_id, version_no)`. На `(document_id, content_hash)` используется обычный, не уникальный индекс: одинаковое содержимое может повториться в истории документа.

Алгоритм обновления:

1. Повторно получить материал из источника.
2. Go вычисляет `content_hash` по согласованной сериализации title/body/published_at; при определении новой версии также учитывается parser_version. См. article-processing.md.
3. Сравнить content_hash и parser_version с последней сохранённой article_version Go. Если оба не изменились — новую версию не создавать.
4. Если content_hash или parser_version изменились — Go сохраняет следующую article_version со ссылкой на оригинал (в текущем прототипе raw_artifact, в целевом S3-варианте объект). document_version_id ML сохраняет этот ID; новый ML-документ не создаётся. Последовательность A → B → A сохраняется как три версии.
5. Для новой версии после quality PASS создать чанки по выбранному chunking-профилю, затем embeddings. Повтор той же версии и профиля использует прежний набор; новое разбиение той же версии требует нового профиля, без перезаписи исторических чанков.

Сравнение с последней версией и выделение следующего `version_no` выполняются с сериализацией записи по `document_id`, чтобы два одновременных опроса не создавали две версии одного изменения. Ссылки старых доказательств сохраняются.

### 1.13. `document_chunks`

Фрагменты документов для поиска и RAG хранятся в `ml.document_chunks`. [Первая ML-миграция](../../ml/migrations/000001_document_storage.sql) создаёт таблицу с локальным FK на snapshot, [persistence](../../ml/adapters/postgres_chunks.py) атомарно сохраняет снимок и чанки. [Запуск и роли](../../ml/PERSISTENCE.md). Chunker и профиль `v1` не менялись.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK; детерминированный UUIDv5 от ML1, публичный `chunk_id` |
| `document_version_id` | UUID | NOT NULL; FK → локальная `document_versions.id`, ON DELETE RESTRICT |
| `chunking_profile_id` | TEXT | NOT NULL; например `v1`, неизменяемая конфигурация в ML1-коде под Git |
| `chunk_no` | INTEGER | NOT NULL; порядок от 0 внутри версии и профиля |
| `chunk_text` | TEXT | NOT NULL; точный непрерывный срез `normalized_text` |
| `char_start` | INTEGER | NOT NULL; начало диапазона включительно, Unicode code points |
| `char_end` | INTEGER | NOT NULL; конец диапазона исключительно, Unicode code points |
| `token_count` | INTEGER | NOT NULL; число токенов `chunk_text` по tokenizer профиля, без специальных токенов |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now(); время первой записи, не участвует в ID |
| `search_vector` | TSVECTOR | GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, chunk_text)) STORED; без FTS-индекса/поиска |
| `embedding` | VECTOR(1024) | NULL до построения; профиль ML1 Qwen v1 |
| `embedding_model` | TEXT | NULL до построения; идентификатор модели сохранённого вектора |
| `embedding_version` | TEXT | NULL до построения; неизменяемая версия конфигурации embeddings |

Уникальность: `(document_version_id, chunking_profile_id, chunk_no)`. Отдельная таблица `chunking_profiles` и FK на неё пока не нужны. Опубликованный профиль нельзя менять: алгоритм, размеры, overlap, tokenizer/revision и настройки подсчёта закреплены в коде; их изменение требует нового ID профиля.

Профиль `v1`: target **512**, мягкий minimum **128**, жёсткий maximum **768** токенов с учётом overlap; overlap — до **64** токенов по границам предложений, допустим 0. Tokenizer — `Qwen/Qwen3-Embedding-0.6B`, revision `97b0c614be4d77ee51c0cef4e5f07c00f9eb65b3`, `tokenizers==0.22.1`. Пустой title допустим, в `chunk_text` не добавляется. Модель и embeddings на этапе chunking не запускаются.

Проверки строк:

```sql
CHECK (length(btrim(chunking_profile_id)) > 0)
CHECK (chunk_no >= 0)
CHECK (length(chunk_text) > 0)
CHECK (char_start >= 0)
CHECK (char_end > char_start)
CHECK (char_end - char_start = char_length(chunk_text))
CHECK (token_count > 0)
CHECK (
    (embedding IS NULL AND embedding_model IS NULL AND embedding_version IS NULL)
    OR
    (embedding IS NOT NULL AND embedding_model IS NOT NULL AND embedding_version IS NOT NULL)
)
```

Offsets имеют семантику `[char_start, char_end)` в исходном `normalized_text`, не UTF-8 bytes и не позиции токенов. Приложение проверяет точное равенство срезу, полный охват текста без пропусков, последовательность номеров, непробельный текст и лимиты профиля. Обычный CHECK не проверяет родительский текст или весь набор. Если невозможно совместить покрытие, непробельные чанки и maximum, chunker возвращает ошибку без частичного результата.

ID формируется UUIDv5 из `document_version_id`, `chunking_profile_id`, offsets и SHA-256 точного `chunk_text`; namespace и сериализация закреплены в ML1. `trace_id`, часы и embeddings на ID не влияют. Набор одной версии и профиля сохраняется атомарно; retry проверяет совпадение данных без перезаписи текста и `created_at`. Старые профили сохраняются для исторических evidence; retrieval должен выбирать активный готовый профиль. Наличие чанков само по себе не означает ready/SUCCEEDED.

FK указывает на `ml.document_versions(id) ON DELETE RESTRICT`. Provenance/title/document_id хранятся в локальном immutable snapshot, без FK на Go; Go ↔ ML — только HTTP. Миграции имеют отдельный журнал `ml.schema_migrations`. Backfill существующего развёртывания и PDF page mapping остаются отдельными задачами. Для embeddings: Qwen3-Embedding-0.6B, 1024 измерения, `embedding_version=ml1-qwen3-embedding-1024-v1`; [профиль](../../ml/EMBEDDINGS.md).

### 1.14. `events`

Дедуплицированные события.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `event_type` | TEXT | NOT NULL |
| `title` | TEXT | NOT NULL |
| `summary` | TEXT | NOT NULL |
| `occurred_at` | TIMESTAMPTZ | |
| `geography` | JSONB | NOT NULL, DEFAULT `[]` |
| `status` | TEXT | `candidate`, `confirmed`, `disputed`, `rejected` |
| `confidence` | NUMERIC | `0..1` |
| `created_at` | TIMESTAMPTZ | NOT NULL |

### 1.15. `event_documents`

Связь событий с документами.

| Поле | Тип | Ограничения |
|---|---|---|
| `event_id` | UUID | PK, FK → `events.id` |
| `document_version_id` | UUID | PK, FK → `document_versions.id` |
| `evidence_role` | TEXT | `support`, `contradict`, `context` |
| `is_primary` | BOOLEAN | DEFAULT `false` |

### 1.16. `event_facts`

Факты, выделенные из события.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `event_id` | UUID | FK → `events.id` |
| `subject` | TEXT | NOT NULL |
| `predicate` | TEXT | NOT NULL |
| `object_value` | TEXT | NOT NULL |
| `verification_status` | TEXT | `candidate`, `confirmed`, `disputed`, `rejected` |
| `confidence` | NUMERIC | `0..1` |

### 1.17. `fact_evidence`

Фрагменты, подтверждающие факт.

| Поле | Тип | Ограничения |
|---|---|---|
| `fact_id` | UUID | PK, FK → `event_facts.id` |
| `chunk_id` | UUID | PK, FK → `document_chunks.id` |
| `quote_text` | TEXT | NOT NULL |
| `evidence_role` | TEXT | `support`, `contradict`, `context` |

### 1.18. `insights`

Персонализированная интерпретация события для конкретного бизнес-контекста и роли.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `business_context_id` | UUID | FK → `business_contexts.id` |
| `event_id` | UUID | FK → `events.id` |
| `analysis_run_id` | UUID | FK → `analysis_runs.id` |
| `version` | INTEGER | NOT NULL |
| `status` | TEXT | `draft`, `verified`, `rejected`, `superseded` |
| `impact_kind` | TEXT | `risk`, `opportunity`, `mixed`, `neutral` |
| `relevance_score` | NUMERIC | `0..1` |
| `impact_score` | NUMERIC | `-1..1` |
| `confidence` | NUMERIC | `0..1` |
| `severity` | TEXT | `low`, `medium`, `high`, `critical` |
| `time_horizon` | TEXT | |
| `summary` | TEXT | NOT NULL |
| `consequences` | JSONB | NOT NULL, DEFAULT `[]` |
| `recommended_actions` | JSONB | NOT NULL, DEFAULT `[]` |
| `score_components` | JSONB | NOT NULL, DEFAULT `{}` |
| `bank_view` | TEXT | |
| `bank_view_document_version_id` | UUID | FK → `document_versions.id` |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Уникальность: `(business_context_id, event_id, version)`. Одно событие может дать разные insights для CEO и CFO, а также для отдельной компании и всей группы.

### 1.19. `insight_claims`

Отдельные проверяемые утверждения внутри insight или ответа AI в диалоге. Для обоих случаев используются существующие `insight_claims` и `claim_evidence`.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `insight_id` | UUID | FK → `insights.id`, nullable |
| `message_id` | UUID | FK → `messages.id`, nullable |
| `claim_type` | TEXT | `relevance`, `impact`, `consequence`, `action`, `bank_view`; назначение утверждения |
| `knowledge_type` | TEXT | NOT NULL; `FACT`, `ALFA_ANALYTICS`, `AI_INFERENCE`, `FORECAST` |
| `verification_status` | TEXT | NOT NULL; `SUPPORTED`, `PARTIALLY_SUPPORTED` для опубликованных claims |
| `assumptions` | JSONB | NOT NULL, DEFAULT `[]`; явные допущения |
| `time_horizon` | TEXT | nullable; срок утверждения |
| `claim_text` | TEXT | NOT NULL |
| `position` | INTEGER | NOT NULL |
| `confidence` | NUMERIC | `0..1` |

Заполняется ровно одно из полей: `insight_id` или `message_id`; это проверяется ограничением `CHECK`. Для утверждения сообщения `message_id` указывает на ответ с `role = assistant`. Позиция утверждения уникальна внутри соответствующего insight или сообщения.

Поля `knowledge_type`, `verification_status`, `assumptions`, `time_horizon` добавлены в целевую спецификацию редакции ML 1.1 для сохранения смысла публичного API. Миграции пока отсутствуют; backend реализует их до I05. Старые claims нельзя автоматически классифицировать по claim_type: они требуют восстановления метаданных/повторной проверки. PARTIALLY_SUPPORTED требует непустого списка assumptions; банк и прогноз не определяются по confidence.

### 1.20. `claim_evidence`

Связь конкретного claim с источником или признаком бизнес-профиля.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `claim_id` | UUID | FK → `insight_claims.id` |
| `evidence_type` | TEXT | `document_chunk`, `event_fact`, `profile_feature` |
| `chunk_id` | UUID | FK → `document_chunks.id`, nullable |
| `fact_id` | UUID | FK → `event_facts.id`, nullable |
| `profile_feature_id` | UUID | FK → `business_profile_features.id`, nullable |
| `evidence_role` | TEXT | `support`, `contradict`, `context` |
| `quote_text` | TEXT | nullable |

Заполняется ровно одно из полей: `chunk_id`, `fact_id`, `profile_feature_id`. Это позволяет восстановить цепочку `claim → источник/признак клиента`.

Insight нельзя переводить в `verified`, пока каждый его claim не имеет хотя бы одного подтверждающего `claim_evidence`.

Новые выводы уточняющего ответа сохраняются как claims этого сообщения с собственными доказательствами, включая признаки бизнес-профиля. Они не изменяют утверждения исходного insight. Для опубликованных выводов ответа также требуется подтверждающее основание; недостаток данных явно отражается в тексте ответа.

### 1.21. `analysis_runs`

Версии и статусы запусков AI-анализа.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `trace_id` | UUID | UNIQUE, NOT NULL; отдельный для каждого нового запуска |
| `request_operation_id` | UUID | UNIQUE, FK → `request_operations.id`, nullable для запуска вне пользовательского разбора или уточнения |
| `parent_trace_id` | UUID | FK → `analysis_runs.trace_id`, nullable |
| `business_context_id` | UUID | FK → `business_contexts.id`, nullable для построения профиля |
| `request_type` | TEXT | `initial`, `follow_up`, `reanalyze` |
| `status` | TEXT | `queued`, `running`, `succeeded`, `failed` |
| `model_name` | TEXT | |
| `prompt_version` | TEXT | |
| `model_parameters` | JSONB | NOT NULL, DEFAULT `{}` |
| `started_at` | TIMESTAMPTZ | NOT NULL |
| `finished_at` | TIMESTAMPTZ | |
| `error_message` | TEXT | |

Для пользовательского разбора и уточнения ML сохраняет связь с `request_operation_id` до платного вызова модели. Повторное обращение с тем же идентификатором возвращает существующий запуск и его состояние; смена `trace_id` не создает новый запуск.

### 1.22. `analysis_steps`

Шаги внутри запуска AI-анализа.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | BIGSERIAL | PK |
| `run_id` | UUID | FK → `analysis_runs.id` |
| `step_no` | INTEGER | NOT NULL |
| `step_type` | TEXT | `request`, `retrieval`, `tool`, `llm`, `verification`, `response` |
| `tool_name` | TEXT | |
| `status` | TEXT | NOT NULL |
| `input_summary` | JSONB | NOT NULL, DEFAULT `{}` |
| `output_summary` | JSONB | NOT NULL, DEFAULT `{}` |

Уникальность: `(run_id, step_no)`.

### 1.23. `conversations`

Диалог пользователя по конкретному исходному insight и его бизнес-контексту.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `user_id` | UUID | NOT NULL, FK → `users.id`; идентификатор пользователя из проверенной сессии Go |
| `business_context_id` | UUID | NOT NULL, FK → `business_contexts.id` |
| `initial_insight_id` | UUID | NOT NULL, FK → `insights.id` |
| `created_at` | TIMESTAMPTZ | NOT NULL |
| `updated_at` | TIMESTAMPTZ | NOT NULL |

`user_id` — стабильный идентификатор пользователя сервиса, отдельный от обезличенного `persons.id`. Go берет его из проверенной сессии, сохраняет владельца диалога и проверяет владельца вместе с доступом к контексту при чтении и записи. Два пользователя с одной компанией и ролью имеют отдельные диалоги.

`business_context_id` должен совпадать с контекстом `initial_insight_id`; Go проверяет это в транзакции создания диалога. Исходные insight и контекст диалога не меняются при уточняющих вопросах.

### 1.24. `messages`

Сообщения пользователя, AI и инструментов внутри диалога.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `conversation_id` | UUID | FK → `conversations.id` |
| `parent_message_id` | UUID | FK → `messages.id`, nullable |
| `trace_id` | UUID | FK → `analysis_runs.trace_id`, nullable |
| `role` | TEXT | `user`, `assistant`, `tool`, `system` |
| `content` | TEXT | NOT NULL |
| `created_at` | TIMESTAMPTZ | NOT NULL |

### 1.25. `message_citations`

Первоисточники и claims, показанные вместе с ответом.

| Поле | Тип | Ограничения |
|---|---|---|
| `message_id` | UUID | PK, FK → `messages.id` |
| `citation_order` | INTEGER | PK |
| `chunk_id` | UUID | FK → `document_chunks.id`, nullable |
| `claim_id` | UUID | FK → `insight_claims.id`, nullable |

Должно быть заполнено хотя бы одно из полей: `chunk_id`, `claim_id`.

`claim_id` может ссылаться на утверждение текущего сообщения или исходного insight этого диалога. Go проверяет принадлежность ссылки при сохранении. Для нового вывода используется claim текущего сообщения; его источники и признаки профиля доступны через `claim_evidence`.

### 1.26. `feedback`

Обратная связь по insight или конкретному ответу в диалоге.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `user_id` | UUID | NOT NULL, FK → `users.id`; автор оценки из проверенной сессии Go |
| `insight_id` | UUID | FK → `insights.id`, nullable |
| `message_id` | UUID | FK → `messages.id`, nullable |
| `rating` | SMALLINT | `-1`, `0`, `1` |
| `reason_code` | TEXT | `useful`, `irrelevant`, `incorrect`, `unclear`, `other` |
| `comment` | TEXT | nullable |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Должно быть заполнено хотя бы одно из полей: `insight_id`, `message_id`. `reason_code` нужен, чтобы feedback можно было использовать для оценки качества, а не только считать лайки.

Go определяет автора по сессии и проверяет доступ к оцениваемому insight либо принадлежность сообщения диалогу этого пользователя. Если заполнены оба поля, сообщение должно относиться к диалогу указанного insight.

### 1.27. `request_operations`

Сохраненные пользовательские операции разбора и уточнения, которые защищают от повторного платного запуска и связывают запрос с результатом.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK; стабильный идентификатор операции Go |
| `user_id` | UUID | NOT NULL, FK → `users.id`; из проверенной сессии Go |
| `operation_type` | TEXT | NOT NULL; `insight`, `follow_up` |
| `idempotency_key` | TEXT | NOT NULL; непустой обязательный `Idempotency-Key` запроса |
| `request_fingerprint` | BYTEA | NOT NULL; SHA-256 нормализованных клиентских параметров, ровно 32 байта |
| `business_context_id` | UUID | NOT NULL, FK → `business_contexts.id` |
| `event_id` | UUID | FK → `events.id`, обязателен для разбора, nullable для уточнения |
| `conversation_id` | UUID | FK → `conversations.id`, nullable для разбора |
| `request_message_id` | UUID | FK → `messages.id`, nullable для разбора |
| `request_snapshot` | JSONB | NOT NULL; зафиксированные параметры и версии выполнения |
| `status` | TEXT | NOT NULL; `queued`, `running`, `succeeded`, `insufficient_evidence`, `failed` |
| `insight_id` | UUID | FK → `insights.id`, nullable до результата разбора |
| `response_message_id` | UUID | FK → `messages.id`, nullable до результата уточнения |
| `error_message` | TEXT | Очищенная причина ошибки, nullable |
| `created_at` | TIMESTAMPTZ | NOT NULL |
| `updated_at` | TIMESTAMPTZ | NOT NULL |

Уникальность: `(user_id, operation_type, idempotency_key)`. Go сохраняет операцию транзакционно до обращения к ML. Для разбора обязателен `event_id`. Для уточнения в той же транзакции один раз сохраняется пользовательское сообщение; `conversation_id` и `request_message_id` обязательны и относятся к диалогу владельца операции с тем же бизнес-контекстом.

`request_snapshot` фиксирует параметры события или вопроса, роль, идентификатор и версию профиля, версию конфигурации обработки; для уточнения также фиксируется граница истории диалога. Эти значения сохраняются при первом запросе и повторно не вычисляются. `request_fingerprint` считается по правилам `fingerprint_v1` из [RFC пользовательского API](0004-user-api-storage.md#3-идемпотентность-и-диалоги); технический `trace_id` в него не входит.

Повтор с тем же ключом и параметрами возвращает прежнюю операцию или результат после повторной проверки прав. Другие параметры с тем же ключом дают `409 Conflict`; отсутствие ключа — `400`. Go передает `request_operations.id` как внутренний `Idempotency-Key`; ML сохраняет его в `analysis_runs.request_operation_id` до платного вызова и устраняет одновременные повторы. При неизвестном исходе вызова модели новый платный вызов не запускается вслепую: проверяется сохраненный результат либо возвращается контролируемая ошибка.

Для успешного разбора обязателен `insight_id`, для успешного уточнения — `response_message_id`. Результат относится к тому же бизнес-контексту и доступен пользователю операции; insight соответствует указанному событию, сообщение ответа принадлежит указанному диалогу. Сохранение результата и перевод операции в `succeeded` выполняются в одной транзакции. Ключи незавершенных операций сохраняются; ключи завершенных хранятся не меньше установленного в настройках окна повторов.

## 2. Связи

| Таблица A | Связь | Таблица B |
|---|---|---|
| `company_groups` | N ↔ M | `companies` через `company_group_members` |
| `companies` / `company_groups` | 1 → N | `business_profiles` |
| `business_profiles` | 1 → N | `business_profile_features` |
| `business_profiles` | 1 → N | `business_contexts` |
| `companies` | N ↔ M | `persons` через `company_person_links` |
| `companies` | 1 → N | `payment_edges` как плательщик |
| `companies` | 1 → N | `payment_edges` как получатель |
| `sources` | 1 → N | `documents` |
| `documents` | 1 → N | `document_versions` |
| `document_versions` | 1 → N | `document_chunks` |
| `document_versions` | N ↔ M | `events` через `event_documents` |
| `events` | 1 → N | `event_facts` |
| `event_facts` | 1 → N | `fact_evidence` |
| `document_chunks` | 1 → N | `fact_evidence` |
| `business_contexts` | 1 → N | `insights` |
| `events` | 1 → N | `insights` |
| `insights` | 1 → N | `insight_claims` |
| `messages` | 1 → N | `insight_claims` для выводов уточняющего ответа |
| `insight_claims` | 1 → N | `claim_evidence` |
| `business_profile_features` | 1 → N | `claim_evidence` |
| `analysis_runs` | 1 → N | `analysis_steps` |
| `analysis_runs` | 1 → N | `insights` |
| `business_contexts` | 1 → N | `conversations` |
| `conversations` | 1 → N | `messages` |
| `messages` | 1 → N | `message_citations` |
| `insights` / `messages` | 1 → N | `feedback` |
| `business_contexts` | 1 → N | `request_operations` |
| `conversations` | 1 → N | `request_operations` для уточнений |
| `request_operations` | 1 → 0..1 | `analysis_runs` |
| `request_operations` | N → 0..1 | `insights` или `messages` как результат |

## 3. JSONB

`JSONB` используется для данных, состав которых может изменяться или содержать вложенные списки и объекты. Основные идентификаторы, статусы, даты и значения, участвующие в связях, остаются обычными колонками.

| Таблица | Поле | Содержимое |
|---|---|---|
| `companies` | `raw_profile` | Дополнительные исходные признаки компании |
| `business_profiles` | `profile_data` | Версионируемый профиль компании или группы |
| `business_profile_features` | `feature_value` | Значение отдельного признака |
| `business_profile_features` | `source_ref` | Ссылка на исходные данные признака |
| `document_versions` | `metadata` | Теги и дополнительные метаданные конкретной версии документа |
| `events` | `geography` | Список стран и регионов |
| `insights` | `consequences` | Список последствий |
| `insights` | `recommended_actions` | Список действий |
| `insights` | `score_components` | Компоненты оценки релевантности |
| `analysis_runs` | `model_parameters` | Параметры модели |
| `analysis_steps` | `input_summary` | Краткие входные данные шага |
| `analysis_steps` | `output_summary` | Краткий результат шага |
| `request_operations` | `request_snapshot` | Параметры запроса, зафиксированные роль, профиль, конфигурация и граница истории |

### 3.1. Формат полей

#### `companies.raw_profile`

Хранит поля исходного набора, для которых пока нет отдельных колонок.

```json
{
  "eco_mark_label": "developing",
  "negative_mark": false,
  "additional_products": ["guarantee"]
}
```

Если какое-либо значение начинает регулярно использоваться в фильтрах или расчетах, оно переносится из `raw_profile` в отдельную колонку `companies`.

#### `business_profiles.profile_data`

Хранит рассчитанный и переиспользуемый профиль компании или группы. Конкретные признаки дополнительно раскладываются в `business_profile_features`, чтобы на них могли ссылаться claims.

```json
{
  "industries": ["construction"],
  "regions": ["77", "50"],
  "products": ["loan", "guarantee"],
  "financial_position": {
    "revenue": 1250000000,
    "debt_exposure": "high"
  },
  "group_summary": {
    "company_count": 4,
    "total_revenue": 3100000000
  }
}
```

#### `document_versions.metadata`

Хранит необязательные данные, зависящие от конкретного источника.

```json
{
  "section": "Экономика",
  "tags": ["ключевая ставка", "кредитование"],
  "original_language": "ru",
  "parser_version": "1.2",
  "image_url": "https://example.ru/image.jpg"
}
```

#### `events.geography`

Хранит список географических объектов, связанных с событием.

```json
[
  {"type": "country", "code": "RU", "name": "Россия"},
  {"type": "region", "code": "77", "name": "Москва"}
]
```

#### `insights.consequences`

Хранит возможные последствия события для компании.

```json
[
  {
    "type": "financial",
    "description": "Рост стоимости обслуживания кредитов",
    "probability": 0.75,
    "time_horizon": "short"
  }
]
```

#### `insights.recommended_actions`

Хранит предлагаемые действия. Это рекомендации, а не автоматически выполняемые команды.

```json
[
  {
    "action": "Пересмотреть график привлечения заемных средств",
    "priority": "high",
    "reason": "Ожидается рост стоимости финансирования"
  }
]
```

#### `insights.score_components`

Хранит составляющие итоговой оценки релевантности.

```json
{
  "semantic_similarity": 0.86,
  "industry_match": 1.0,
  "region_match": 0.4,
  "counterparty_match": 0.0,
  "recency": 0.95,
  "source_trust_level": "primary"
}
```

#### `analysis_runs.model_parameters`

Хранит параметры, с которыми была вызвана модель.

```json
{
  "temperature": 0.2,
  "max_tokens": 4000,
  "response_format": "json"
}
```

#### `analysis_steps.input_summary` и `output_summary`

Хранят сокращенную трассировку шага. Полные тексты документов и embeddings в эти поля не записываются.

```json
{
  "document_version_ids": ["uuid-1", "uuid-2"],
  "candidate_count": 12
}
```

```json
{
  "selected_document_version_ids": ["uuid-1"],
  "event_id": "uuid-3",
  "confidence": 0.84
}
```

### 3.2. Правила использования

- В `JSONB` не хранятся внешние ключи вместо нормальных FK-колонок.
- Поля, используемые в `JOIN`, частой фильтрации или сортировке, хранятся отдельными колонками.
- Для ожидаемого объекта используется `DEFAULT '{}'::jsonb`.
- Для ожидаемого списка используется `DEFAULT '[]'::jsonb`.
- Тип содержимого проверяется через `CHECK (jsonb_typeof(...) = 'object')` или `array`.
- GIN-индекс добавляется только для реально используемых JSONB-запросов.
- API должно валидировать структуру JSON по своей схеме до записи в БД.
- При изменении структуры JSON старые записи должны оставаться читаемыми; при необходимости добавляется версия формата.

Пример проверки дополнительного признака компании:

```sql
SELECT id, synthetic_uid
FROM companies
WHERE raw_profile @> '{"negative_mark": true}'::jsonb;
```

Для MVP достаточно GIN-индекса только на `companies.raw_profile`, если по дополнительным признакам действительно выполняется поиск. Остальные JSONB-поля читаются по идентификатору родительской записи и отдельного индекса не требуют.

## 4. Векторные данные

### 4.1. Что хранится

В MVP используется одна векторная колонка:

```text
document_chunks.embedding VECTOR(1024)
```

Она хранит embedding — массив чисел, который embedding-модель строит из `chunk_text`.

```text
chunk_text:
"Банк России сохранил ключевую ставку на уровне 14%"

embedding:
[0.018, -0.274, 0.091, 0.442, ..., -0.037]
```

Вектор хранит не сам текст и не результат анализа, а его числовое представление для сравнения смысловой близости.

Связанные поля:

| Поле | Назначение |
|---|---|
| `chunk_text` | Исходный текст фрагмента |
| `embedding` | Числовое представление смысла |
| `embedding_model` | Название embedding-модели |
| `embedding_version` | Версия модели или конфигурации |

### 4.2. Создание вектора

```text
Документ
  → разделение на фрагменты
  → передача chunk_text в embedding-модель
  → получение массива чисел
  → запись массива в document_chunks.embedding
```

Один фрагмент соответствует одному сохранённому embedding. Параметры разбиения задаёт неизменяемый `chunking_profile_id`: для ML1 `v1` target=512, minimum=128, maximum=768, overlap до 64 токенов (см. раздел 1.13). Перекрытие сохраняет контекст на границе фрагментов; короткий документ или последний хвост могут быть меньше minimum.

### 4.3. Размерность

`N` — количество чисел в векторе. Оно определяется выбранной моделью:

```sql
embedding vector(1024)
```

Для ML1 выбрана `Qwen/Qwen3-Embedding-0.6B`, dimension=1024, версия профиля `ml1-qwen3-embedding-1024-v1`. Revision совпадает с chunking v1; pooling, нормализация и query instruction закреплены в [embedding profile](../../ml/embeddings/profile.py). Тип VECTOR(1024) включён в отдельную ML-миграцию; vector retrieval пока не реализован.

Все строки одного HNSW-индекса должны использовать одну модель и одну размерность. Векторы разных моделей нельзя корректно сравнивать между собой.

### 4.4. Использование

При поиске текст запроса также преобразуется в embedding. PostgreSQL сравнивает его с embeddings фрагментов и возвращает ближайшие по смыслу записи.

```sql
SELECT
    id,
    document_version_id,
    chunk_text,
    1 - (embedding <=> :query_embedding) AS similarity
FROM document_chunks
WHERE embedding IS NOT NULL
ORDER BY embedding <=> :query_embedding
LIMIT 20;
```

Оператор `<=>` вычисляет cosine distance. Чем меньше расстояние, тем ближе тексты. Выражение `1 - distance` используется как условная similarity.

Векторный поиск применяется вместе с обычными фильтрами:

```sql
SELECT dc.id, dc.chunk_text
FROM document_chunks dc
JOIN document_versions dv ON dv.id = dc.document_version_id
JOIN documents d ON d.id = dv.document_id
WHERE dc.embedding IS NOT NULL
  AND dv.published_at >= now() - interval '30 days'
  AND dv.processing_status = 'analyzed'
ORDER BY dc.embedding <=> :query_embedding
LIMIT 20;
```

### 4.5. Индекс

Для MVP используется HNSW-индекс по cosine distance:

```sql
CREATE INDEX document_chunks_embedding_hnsw_idx
ON document_chunks
USING hnsw (embedding vector_cosine_ops)
WHERE embedding IS NOT NULL;
```

На небольшом тестовом наборе можно сначала использовать точный поиск без HNSW. Индекс добавляется, когда количество фрагментов начинает заметно влиять на задержку поиска.

### 4.6. Частота операций

| Операция | Частота |
|---|---|
| Создание embedding | Один раз после создания фрагмента |
| Чтение через similarity search | При каждом поиске релевантных материалов |
| Обновление | При изменении текста или embedding-модели |
| Удаление | Вместе с фрагментом или при полной переиндексации |

### 4.7. Смена embedding-модели

При смене модели старые и новые векторы нельзя смешивать в одном индексе. Для MVP выполняется полная переиндексация `document_chunks`.

Если одновременно потребуется несколько моделей, embeddings следует вынести в отдельную таблицу:

```text
chunk_embeddings
├── chunk_id
├── model_name
├── model_version
├── dimensions
└── embedding
```

Для текущего MVP отдельная таблица не требуется: одна колонка в `document_chunks` проще и не добавляет лишний `JOIN`.

## 5. Хранение оригиналов в S3

Для MVP используется один приватный S3-совместимый bucket. Подойдет облачное Object Storage с S3 API или локальный MinIO, если проект запускается полностью в Docker.

Рекомендуемое имя bucket:

```text
news-analytics-raw-{environment}
```

Пример ключа объекта:

```text
raw/{source_id}/{document_id}/{version_no}/{checksum_sha256}.{extension}
```

Пример:

```text
raw/cbr/9a2f.../3/a84c....html
```

В PostgreSQL сохраняются:

| Поле | Назначение |
|---|---|
| `s3_bucket` | Имя bucket |
| `s3_object_key` | Неизменяемый ключ объекта |
| `s3_version_id` | Версия объекта S3, если включено bucket versioning |
| `mime_type` | Тип файла |
| `size_bytes` | Размер файла |
| `checksum_sha256` | Проверка целостности оригинала |

Полный публичный URL в БД не хранится. Bucket остается закрытым, а backend при необходимости создает временный pre-signed URL на скачивание.

```text
Загрузчик
  → получает HTML/PDF
  → вычисляет SHA-256
  → загружает оригинал в S3
  → получает object key и version ID
  → создает document_version в PostgreSQL
```

Правила:

- S3-объекты не перезаписываются; новая версия документа получает новый object key.
- Версионирование в `document_versions` является основным прикладным механизмом. S3 versioning включается дополнительно как защита от случайного перезаписывания или удаления.
- Доступ к bucket разрешен только backend и загрузчику.
- Access key и secret key хранятся в переменных окружения или secret manager, но не в PostgreSQL.
- Для MVP lifecycle-удаление оригиналов не настраивается, чтобы не потерять материалы, на которых основаны выводы.
- Временные и неуспешные multipart-загрузки можно удалять lifecycle-правилом.

## 6. Индексы

| Таблица | Индекс |
|---|---|
| `companies` | B-tree: `synthetic_uid` |
| `companies` | B-tree: `(main_okved_code, region)` |
| `company_group_members` | UNIQUE: `(group_id, company_id)` |
| `business_profiles` | B-tree: `(company_id, version DESC)` WHERE `company_id IS NOT NULL` |
| `business_profiles` | B-tree: `(group_id, version DESC)` WHERE `group_id IS NOT NULL` |
| `business_profile_features` | UNIQUE: `(profile_id, feature_key)` |
| `business_contexts` | UNIQUE: `(company_id, profile_id, role_code)` WHERE `company_id IS NOT NULL` |
| `business_contexts` | UNIQUE: `(group_id, profile_id, role_code)` WHERE `group_id IS NOT NULL` |
| `documents` | UNIQUE: `(source_id, external_id)` |
| `documents` | UNIQUE: `canonical_url` |
| `document_versions` | UNIQUE: `(document_id, version_no)` |
| `document_versions` | B-tree: `(document_id, content_hash)` |
| `document_versions` | B-tree: `content_hash` |
| `document_versions` | B-tree: `published_at DESC` |
| `document_chunks` | UNIQUE: `(document_version_id, chunking_profile_id, chunk_no)` |
| `document_chunks` | GIN: `search_vector` |
| `document_chunks` | HNSW: `embedding vector_cosine_ops` |
| `events` | B-tree: `occurred_at DESC` |
| `event_facts` | B-tree: `event_id` |
| `insights` | UNIQUE: `(business_context_id, event_id, version)` |
| `insights` | B-tree: `(business_context_id, created_at DESC)` |
| `insight_claims` | UNIQUE: `(insight_id, position)` WHERE `insight_id IS NOT NULL` |
| `insight_claims` | UNIQUE: `(message_id, position)` WHERE `message_id IS NOT NULL` |
| `claim_evidence` | B-tree: `claim_id` |
| `analysis_runs` | UNIQUE: `trace_id` |
| `analysis_runs` | UNIQUE: `request_operation_id` WHERE `request_operation_id IS NOT NULL` |
| `analysis_steps` | UNIQUE: `(run_id, step_no)` |
| `conversations` | B-tree: `(user_id, updated_at DESC)` |
| `messages` | B-tree: `(conversation_id, created_at)` |
| `feedback` | B-tree: `(insight_id, created_at DESC)` |
| `request_operations` | UNIQUE: `(user_id, operation_type, idempotency_key)` |

## 7. Что входит в MVP

Обязательные таблицы:

```text
companies
company_groups
company_group_members
business_profiles
business_profile_features
business_contexts
persons
company_person_links
payment_edges
sources
documents
document_versions
document_chunks
events
event_documents
insights
insight_claims
claim_evidence
analysis_runs
analysis_steps
conversations
messages
message_citations
feedback
request_operations
```

Можно добавить позже:

```text
event_facts
fact_evidence
```

## Реализованное дополнение ML — 25 октября 2026

[Миграция 000002](../../ml/migrations/000002_pipeline_state_and_events.sql) добавляет `ml.document_version_catalog`, `ml.article_processings`, `ml.events`, `ml.event_documents`, `ml.event_facts`, `ml.fact_evidence`; [точные поля и права](../../ml/PERSISTENCE.md#каталог-и-события-ml2). Это ML-owned таблицы без FK на Go, миграционный журнал — `ml.schema_migrations`.

В отличие от целевого раздела 1.15, фактическая `ml.event_documents` включает также `evidence_quotes`, `candidate_fingerprint`, `pipeline_version` и отдельный `event_pipeline_config_version`. PK: `(event_id, document_version_id, event_pipeline_config_version)`; candidate fingerprint уникален в пределах версии документа/event-конфигурации. Поля `evidence_role` и `is_primary` сохранены. Версию event-конфигурации задаёт ML2; общая `pipeline_version` её не подменяет. Схема не реализует семантическую дедупликацию или проверку claim.
