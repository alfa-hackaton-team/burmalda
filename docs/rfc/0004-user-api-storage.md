# Хранение пользовательского API

**Статус:** проект схемы перед миграциями

**Область:** данные, которыми владеет Go API

**Вне области:** загрузка источников, версии статей, чанки, embeddings и внутреннее состояние ML

## Основные решения

- Пользователь определяется только по серверной сессии. Клиент не передаёт доверенный `user_id`.
- В cookie находится случайный непрозрачный токен. В БД сохраняется только его SHA-256, поэтому утечка таблицы не раскрывает действующие cookie.
- Доступ задаётся к конкретной компании **или** группе и конкретной роли. Доступ к группе не создаёт неявный доступ к её компаниям.
- Роль (`CEO`, `CFO` и другие) задаёт ракурс персонализации, но не заменяет право доступа.
- Административные разрешения хранятся отдельно от доступа к бизнес-контекстам.
- Сессия не содержит снимок прав. На каждом запросе API заново проверяет действующие grants, поэтому отзыв закрывает и ранее созданные ресурсы.
- Идентификаторы событий, профилей, документов и доказательств ссылаются на предметные таблицы из [общей схемы БД](database_scheme.md). Go не создаёт их повторные копии.

## 1. Пользователь, сессия и права

### 1.1. `users`

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `identity_issuer` | TEXT | NOT NULL |
| `identity_subject` | TEXT | NOT NULL |
| `display_name` | TEXT | nullable |
| `disabled_at` | TIMESTAMPTZ | nullable |
| `created_at` | TIMESTAMPTZ | NOT NULL |
| `updated_at` | TIMESTAMPTZ | NOT NULL |

Уникальность: `(identity_issuer, identity_subject)`. Пара однозначно связывает локального пользователя с доверенным механизмом входа. `persons.id` из обезличенного набора не является идентификатором пользователя сервиса.

### 1.2. `user_sessions`

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK, внутренний идентификатор |
| `user_id` | UUID | NOT NULL, FK → `users.id` |
| `session_token_hash` | BYTEA | UNIQUE, NOT NULL, ровно 32 байта |
| `csrf_token_hash` | BYTEA | NOT NULL, ровно 32 байта |
| `authenticated_at` | TIMESTAMPTZ | NOT NULL |
| `last_seen_at` | TIMESTAMPTZ | NOT NULL |
| `expires_at` | TIMESTAMPTZ | NOT NULL |
| `revoked_at` | TIMESTAMPTZ | nullable |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Действующая сессия удовлетворяет условиям `revoked_at IS NULL`, `expires_at > now()` и пользователь не отключён. Сырой session token и сырой CSRF token в БД не сохраняются. При выходе или компрометации заполняется `revoked_at`; строка не удаляется немедленно, чтобы отзыв был однозначным.

### 1.3. `roles`

| Поле | Тип | Ограничения |
|---|---|---|
| `code` | TEXT | PK |
| `display_name` | TEXT | NOT NULL |
| `enabled` | BOOLEAN | NOT NULL, DEFAULT `true` |

Начальный набор: `owner`, `ceo`, `cfo`, `chief_accountant`, `treasury`, `other`. Таблица является справочником ракурсов персонализации, а не списком разрешений.

### 1.4. `context_access_grants`

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `user_id` | UUID | NOT NULL, FK → `users.id` |
| `company_id` | UUID | nullable, FK → `companies.id` |
| `group_id` | UUID | nullable, FK → `company_groups.id` |
| `role_code` | TEXT | NOT NULL, FK → `roles.code` |
| `granted_by` | UUID | nullable, FK → `users.id` |
| `created_at` | TIMESTAMPTZ | NOT NULL |
| `expires_at` | TIMESTAMPTZ | nullable |
| `revoked_at` | TIMESTAMPTZ | nullable |

Заполняется ровно одно из полей `company_id` или `group_id`. Активный grant уникален для `(user_id, company_id/group_id, role_code)`. История отозванных grants сохраняется. Проверка группы никогда не заменяет отдельную проверку компании.

### 1.5. `admin_permission_grants`

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `user_id` | UUID | NOT NULL, FK → `users.id` |
| `permission` | TEXT | `sources:read`, `sources:manage`, `sources:trust` |
| `granted_by` | UUID | nullable, FK → `users.id` |
| `created_at` | TIMESTAMPTZ | NOT NULL |
| `expires_at` | TIMESTAMPTZ | nullable |
| `revoked_at` | TIMESTAMPTZ | nullable |

Каждое разрешение выдаётся и отзывается независимо. `sources:manage` не включает неявно `sources:trust`; проверка составных политик выполняется Go явно.

## 2. Контекст и пользовательские результаты

Таблицы `business_contexts`, `insights`, `insight_claims`, `claim_evidence`, `analysis_runs`, `analysis_steps`, `conversations`, `messages`, `message_citations`, `feedback` и `request_operations` уже определены в [общей схеме](database_scheme.md). Для пользовательского API применяются следующие уточнения.

### 2.1. Изменения существующих таблиц

| Таблица | Уточнение |
|---|---|
| `business_contexts` | `role_code` становится FK → `roles.code`; контекст сам по себе не предоставляет доступ |
| `conversations` | `user_id UUID NOT NULL` и FK → `users.id` |
| `feedback` | `user_id UUID NOT NULL` и FK → `users.id`; добавить nullable `insight_version INTEGER`, обязательный вместе с `insight_id`, и составной FK → `insights(id, version)` |
| `request_operations` | `user_id UUID NOT NULL` и FK → `users.id`; `request_fingerprint` хранится как 32-байтовый SHA-256 |
| `analysis_runs` | `request_operation_id` связывает ML-запуск со стабильной операцией Go до платного вызова |

Для составной ссылки feedback таблица `insights` получает UNIQUE `(id, version)`. Если feedback относится к сообщению, `insight_id` и `insight_version` остаются `NULL`.

### 2.2. `alerts`

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `business_context_id` | UUID | NOT NULL, FK → `business_contexts.id` |
| `event_id` | UUID | NOT NULL, FK → `events.id` |
| `impact` | JSONB | NOT NULL, объект `ImpactState` |
| `policy_version` | TEXT | NOT NULL |
| `created_at` | TIMESTAMPTZ | NOT NULL |
| `expires_at` | TIMESTAMPTZ | nullable |

Уникальность: `(business_context_id, event_id, policy_version)`. Доступ к alert определяется через актуальный grant к его `business_context_id`.

### 2.3. `feed_snapshots`

| Поле | Тип | Ограничения |
|---|---|---|
| `id` | UUID | PK |
| `user_id` | UUID | NOT NULL, FK → `users.id` |
| `business_context_id` | UUID | NOT NULL, FK → `business_contexts.id` |
| `filters_fingerprint` | BYTEA | NOT NULL, ровно 32 байта |
| `ranking_version` | TEXT | NOT NULL |
| `created_at` | TIMESTAMPTZ | NOT NULL |
| `expires_at` | TIMESTAMPTZ | NOT NULL |

Snapshot фиксирует выдачу, к которой относится курсор. Он привязан к пользователю, контексту и фильтрам, поэтому подмена курсора не даёт прочитать чужую ленту.

### 2.4. `feed_snapshot_items`

| Поле | Тип | Ограничения |
|---|---|---|
| `snapshot_id` | UUID | PK, FK → `feed_snapshots.id`, ON DELETE CASCADE |
| `position` | INTEGER | PK, `>= 0` |
| `preview_id` | UUID | NOT NULL |
| `event_id` | UUID | NOT NULL, FK → `events.id` |
| `alert_id` | UUID | nullable, FK → `alerts.id` |
| `payload` | JSONB | NOT NULL, объект `FeedItem` |

Порядок определяется только `position`. После создания snapshot строки не меняются; новый профиль или ранжирование создают новый snapshot.

### 2.5. `trace_projections`

| Поле | Тип | Ограничения |
|---|---|---|
| `trace_id` | UUID | PK |
| `request_operation_id` | UUID | UNIQUE, NOT NULL, FK → `request_operations.id` |
| `stages` | JSONB | NOT NULL, массив разрешённых `TraceStage` |
| `created_at` | TIMESTAMPTZ | NOT NULL |
| `updated_at` | TIMESTAMPTZ | NOT NULL |

Таблица содержит только публичную проекцию: статусы, безопасные описания и ссылки на разрешённые evidence/profile features. Промпты, токены, внутренние URL, стеки и скрытые рассуждения здесь не хранятся. Пользователь и контекст определяются через `request_operations`, после чего доступ проверяется заново.

## 3. Идемпотентность и диалоги

Для `request_operations` сохраняются следующие инварианты:

- уникальность `(user_id, operation_type, idempotency_key)`;
- тот же ключ и тот же `request_fingerprint` возвращают прежнюю операцию;
- тот же ключ с другим fingerprint приводит к `409 Conflict`;
- для `insight` обязателен `event_id`, для `follow_up` — `conversation_id` и `request_message_id`;
- пользовательское сообщение и операция follow-up создаются один раз в одной транзакции;
- состояние `succeeded` требует ссылки на сохранённый insight либо assistant message;
- повторное чтение операции, insight или сообщения всегда заново проверяет действующий доступ.

Для `messages` добавляется уникальность `(conversation_id, sequence)`. `parent_message_id`, если заполнен, должен указывать на сообщение того же диалога. Смена контекста, роли или исходной версии insight внутри существующего диалога запрещена.

## 4. Продуктовая аналитика

### 4.1. `client_analytics_events`

| Поле | Тип | Ограничения |
|---|---|---|
| `analytics_event_id` | TEXT | PK, непустой |
| `user_id` | UUID | NOT NULL, FK → `users.id` |
| `business_context_id` | UUID | NOT NULL, FK → `business_contexts.id` |
| `schema_version` | TEXT | NOT NULL |
| `event_type` | TEXT | `FEED_OPENED`, `INSIGHT_OPENED`, `SOURCE_CLICKED`, `ALERT_OPENED` |
| `occurred_at` | TIMESTAMPTZ | NOT NULL |
| `received_at` | TIMESTAMPTZ | NOT NULL |
| `insight_id` | UUID | nullable, FK → `insights.id` |
| `source_id` | UUID | nullable, FK → `sources.id` |
| `alert_id` | UUID | nullable, FK → `alerts.id` |

Пользователь берётся из сессии, а не из события. Для `INSIGHT_OPENED`, `SOURCE_CLICKED` и `ALERT_OPENED` обязательна соответствующая ссылка. Произвольные поля и произвольные типы событий не сохраняются.

## 5. Общие лимиты нескольких экземпляров API

### 5.1. `rate_limit_buckets`

| Поле | Тип | Ограничения |
|---|---|---|
| `subject_hash` | BYTEA | PK, хеш пользователя, сессии или IP |
| `limit_name` | TEXT | PK |
| `window_started_at` | TIMESTAMPTZ | PK |
| `used` | INTEGER | NOT NULL, `>= 0` |
| `reserved` | INTEGER | NOT NULL, `>= 0` |
| `expires_at` | TIMESTAMPTZ | NOT NULL |
| `updated_at` | TIMESTAMPTZ | NOT NULL |

Счётчик изменяется атомарно в PostgreSQL и поэтому общий для всех экземпляров API. Для платных ML-операций используются отдельные `limit_name`; слот сначала резервируется, затем подтверждается или освобождается. Таблица не заменяет `request_operations`: первая ограничивает бюджет, вторая устраняет повтор конкретного действия.

## 6. Связи

| Таблица A | Связь | Таблица B |
|---|---|---|
| `users` | 1 → N | `user_sessions` |
| `users` | 1 → N | `context_access_grants` |
| `users` | 1 → N | `admin_permission_grants` |
| `roles` | 1 → N | `context_access_grants` и `business_contexts` |
| `business_contexts` | 1 → N | `alerts`, `feed_snapshots`, `insights` |
| `feed_snapshots` | 1 → N | `feed_snapshot_items` |
| `users` | 1 → N | `conversations`, `feedback`, `request_operations` |
| `request_operations` | 1 → 0..1 | `analysis_runs` и `trace_projections` |
| `users` | 1 → N | `client_analytics_events` |

## 7. Обязательные индексы

| Таблица | Индекс |
|---|---|
| `user_sessions` | UNIQUE `(session_token_hash)`; B-tree `(user_id, expires_at)` WHERE `revoked_at IS NULL` |
| `context_access_grants` | UNIQUE `(user_id, company_id, role_code)` WHERE `revoked_at IS NULL AND company_id IS NOT NULL`; отдельный такой же индекс для `group_id`; B-tree `(user_id, expires_at)` WHERE `revoked_at IS NULL` |
| `admin_permission_grants` | UNIQUE active grant `(user_id, permission)`; B-tree `(user_id, expires_at)` WHERE `revoked_at IS NULL` |
| `alerts` | B-tree `(business_context_id, created_at DESC, id DESC)` |
| `feed_snapshots` | B-tree `(user_id, business_context_id, expires_at)` |
| `feed_snapshot_items` | UNIQUE `(snapshot_id, preview_id)` |
| `conversations` | B-tree `(user_id, updated_at DESC, id DESC)` |
| `messages` | UNIQUE `(conversation_id, sequence)` |
| `request_operations` | UNIQUE `(user_id, operation_type, idempotency_key)` |
| `client_analytics_events` | B-tree `(user_id, occurred_at DESC)` |
| `rate_limit_buckets` | B-tree `(expires_at)` для пакетной очистки |

## 8. Порядок будущих миграций

1. `users`, `roles`, `user_sessions`.
2. `context_access_grants`, `admin_permission_grants`.
3. Изменение FK пользовательских таблиц и `business_contexts.role_code`.
4. `alerts`, `feed_snapshots`, `feed_snapshot_items`.
5. `trace_projections`, `client_analytics_events`, `rate_limit_buckets`.

Перед созданием FK должны существовать предметные таблицы `companies`, `company_groups`, `business_contexts`, `events`, `insights` и `request_operations`. Миграции будут отдельным PR после согласования этой схемы.
