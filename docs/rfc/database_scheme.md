# Схема базы данных



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

### 1.2. `persons`

Обезличенные физические лица.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `synthetic_fid` | TEXT | UNIQUE, NOT NULL |
| `city` | TEXT | |
| `region` | TEXT | |

### 1.3. `company_person_links`

Связи компаний и физических лиц.

Связь оставляем many-to-many: один человек может быть владельцем, директором или представителем нескольких юридических лиц, а у одной компании может быть несколько связанных лиц. Если исходный набор гарантирует уникальность `person → company`, это ограничение можно добавить отдельно, но схема не должна предполагать его без подтверждения.

| Поле | Тип | Ограничения |
|---|---|---|
| `company_id` | UUID | PK, FK → `companies.id` |
| `person_id` | UUID | PK, FK → `persons.id` |
| `relation_type` | TEXT | PK |

### 1.4. `payment_edges`

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

### 1.5. `sources`

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
| `reliability_score` | NUMERIC | `0..1` |
| `is_active` | BOOLEAN | NOT NULL, DEFAULT `true` |
| `last_success_at` | TIMESTAMPTZ | |

### 1.6. `documents`

Стабильная запись документа независимо от изменений его содержимого.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `source_id` | UUID | FK → `sources.id` |
| `external_id` | TEXT | UNIQUE внутри источника |
| `canonical_url` | TEXT | UNIQUE |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Уникальность: `(source_id, external_id)` и `canonical_url`.

### 1.7. `document_versions`

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
| `content_hash` | TEXT | Хеш нормализованного текста |
| `normalized_text` | TEXT | NOT NULL |
| `metadata` | JSONB | NOT NULL, DEFAULT `{}` |
| `processing_status` | TEXT | `new`, `parsed`, `embedded`, `analyzed`, `failed` |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Уникальность: `(document_id, version_no)` и `(document_id, content_hash)`.

Алгоритм обновления:

1. Повторно получить материал из источника.
2. Вычислить `content_hash` нормализованного текста.
3. Если хеш не изменился — обновление не создавать.
4. Если хеш изменился — загрузить новый оригинал в S3 и создать следующую `document_version`.
5. Создать новые чанки и embeddings только для новой версии.

### 1.8. `document_chunks`

Фрагменты документов для поиска и RAG.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `document_version_id` | UUID | FK → `document_versions.id` |
| `chunk_no` | INTEGER | NOT NULL |
| `chunk_text` | TEXT | NOT NULL |
| `char_start` | INTEGER | |
| `char_end` | INTEGER | |
| `search_vector` | TSVECTOR | GENERATED |
| `embedding` | VECTOR(N) | NULL до построения вектора |
| `embedding_model` | TEXT | |
| `embedding_version` | TEXT | |

Уникальность: `(document_version_id, chunk_no)`.

### 1.9. `events`

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

### 1.10. `event_documents`

Связь событий с документами.

| Поле | Тип | Ограничения |
|---|---|---|
| `event_id` | UUID | PK, FK → `events.id` |
| `document_version_id` | UUID | PK, FK → `document_versions.id` |
| `evidence_role` | TEXT | `support`, `contradict`, `context` |
| `is_primary` | BOOLEAN | DEFAULT `false` |

### 1.11. `event_facts`

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

### 1.12. `fact_evidence`

Фрагменты, подтверждающие факт.

| Поле | Тип | Ограничения |
|---|---|---|
| `fact_id` | UUID | PK, FK → `event_facts.id` |
| `chunk_id` | UUID | PK, FK → `document_chunks.id` |
| `quote_text` | TEXT | NOT NULL |
| `evidence_role` | TEXT | `support`, `contradict`, `context` |

### 1.13. `company_event_assessments`

Оценка влияния события на компанию.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `company_id` | UUID | FK → `companies.id` |
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
| `rationale` | TEXT | NOT NULL |
| `consequences` | JSONB | NOT NULL, DEFAULT `[]` |
| `recommended_actions` | JSONB | NOT NULL, DEFAULT `[]` |
| `score_components` | JSONB | NOT NULL, DEFAULT `{}` |
| `bank_view` | TEXT | |
| `bank_view_document_version_id` | UUID | FK → `document_versions.id` |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Уникальность: `(company_id, event_id, version)`.

### 1.14. `assessment_evidence`

Доказательства выводов оценки.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `assessment_id` | UUID | FK → `company_event_assessments.id` |
| `fact_id` | UUID | FK → `event_facts.id`, nullable |
| `chunk_id` | UUID | FK → `document_chunks.id`, nullable |
| `claim_type` | TEXT | `relevance`, `impact`, `consequence`, `action`, `bank_view` |

Должно быть заполнено хотя бы одно из полей: `fact_id`, `chunk_id`.

### 1.15. `analysis_runs`

Версии и статусы запусков AI-анализа.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `company_id` | UUID | FK → `companies.id` |
| `status` | TEXT | `queued`, `running`, `succeeded`, `failed` |
| `model_name` | TEXT | |
| `prompt_version` | TEXT | |
| `model_parameters` | JSONB | NOT NULL, DEFAULT `{}` |
| `started_at` | TIMESTAMPTZ | NOT NULL |
| `finished_at` | TIMESTAMPTZ | |
| `error_message` | TEXT | |

### 1.16. `analysis_steps`

Шаги внутри запуска AI-анализа.

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | BIGSERIAL | PK |
| `run_id` | UUID | FK → `analysis_runs.id` |
| `step_no` | INTEGER | NOT NULL |
| `step_type` | TEXT | NOT NULL |
| `tool_name` | TEXT | |
| `status` | TEXT | NOT NULL |
| `input_summary` | JSONB | NOT NULL, DEFAULT `{}` |
| `output_summary` | JSONB | NOT NULL, DEFAULT `{}` |

Уникальность: `(run_id, step_no)`.

## 2. Связи

| Таблица A | Связь | Таблица B |
|---|---|---|
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
| `companies` | 1 → N | `company_event_assessments` |
| `events` | 1 → N | `company_event_assessments` |
| `company_event_assessments` | 1 → N | `assessment_evidence` |
| `analysis_runs` | 1 → N | `analysis_steps` |
| `analysis_runs` | 1 → N | `company_event_assessments` |

## 3. JSONB

`JSONB` используется для данных, состав которых может изменяться или содержать вложенные списки и объекты. Основные идентификаторы, статусы, даты и значения, участвующие в связях, остаются обычными колонками.

| Таблица | Поле | Содержимое |
|---|---|---|
| `companies` | `raw_profile` | Дополнительные исходные признаки компании |
| `document_versions` | `metadata` | Теги и дополнительные метаданные конкретной версии документа |
| `events` | `geography` | Список стран и регионов |
| `company_event_assessments` | `consequences` | Список последствий |
| `company_event_assessments` | `recommended_actions` | Список действий |
| `company_event_assessments` | `score_components` | Компоненты оценки релевантности |
| `analysis_runs` | `model_parameters` | Параметры модели |
| `analysis_steps` | `input_summary` | Краткие входные данные шага |
| `analysis_steps` | `output_summary` | Краткий результат шага |

### 3.1. Формат полей

#### `companies.raw_profile`

Хранит поля исходного набора, для которых пока нет отдельных колонок.

```json
{
  "eco_mark_label": "developing",
  "negative_mark": false,
  "group_external_id": "group-42",
  "additional_products": ["guarantee"]
}
```

Если какое-либо значение начинает регулярно использоваться в фильтрах или расчетах, оно переносится из `raw_profile` в отдельную колонку `companies`.

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

#### `company_event_assessments.consequences`

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

#### `company_event_assessments.recommended_actions`

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

#### `company_event_assessments.score_components`

Хранит составляющие итоговой оценки релевантности.

```json
{
  "semantic_similarity": 0.86,
  "industry_match": 1.0,
  "region_match": 0.4,
  "counterparty_match": 0.0,
  "recency": 0.95,
  "source_quality": 0.9
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
document_chunks.embedding VECTOR(N)
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

Один фрагмент соответствует одному embedding. Рекомендуемый размер фрагмента — 300–600 токенов с перекрытием 50–100 токенов. Перекрытие не дает потерять смысл на границе двух фрагментов.

### 4.3. Размерность

`N` — количество чисел в векторе. Оно определяется выбранной моделью:

```sql
embedding vector(1024)
```

Значение `1024` приведено как пример. До создания таблицы команда должна выбрать embedding-модель и установить ее фактическую размерность.

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
| `documents` | UNIQUE: `(source_id, external_id)` |
| `documents` | UNIQUE: `canonical_url` |
| `document_versions` | UNIQUE: `(document_id, version_no)` |
| `document_versions` | UNIQUE: `(document_id, content_hash)` |
| `document_versions` | B-tree: `content_hash` |
| `document_versions` | B-tree: `published_at DESC` |
| `document_chunks` | UNIQUE: `(document_version_id, chunk_no)` |
| `document_chunks` | GIN: `search_vector` |
| `document_chunks` | HNSW: `embedding vector_cosine_ops` |
| `events` | B-tree: `occurred_at DESC` |
| `event_facts` | B-tree: `event_id` |
| `company_event_assessments` | UNIQUE: `(company_id, event_id, version)` |
| `company_event_assessments` | B-tree: `(company_id, created_at DESC)` |
| `analysis_steps` | UNIQUE: `(run_id, step_no)` |

## 7. Что входит в MVP

Обязательные таблицы:

```text
companies
sources
documents
document_versions
document_chunks
events
event_documents
company_event_assessments
assessment_evidence
analysis_runs
analysis_steps
```

Можно добавить позже:

```text
persons
company_person_links
payment_edges
event_facts
fact_evidence
```
