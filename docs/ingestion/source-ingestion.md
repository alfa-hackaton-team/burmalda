# Source ingestion

Status: DRAFT. TEAM DECISION D08/D09: registry, adapters и configurable scheduled polling; ordinary news около 15 минут, конкретный интервал на Source. Publishers/endpoints ещё не проверены и не перечисляются как действующие интеграции.

## Pipeline и ownership

`SourceRegistry → Scheduler → Adapter → RawArticle → Normalizer → NormalizedArticle → TechnicalDedup → Persistent corpus → ML1 event processing/index`

ML3 owns scheduling/adapter infrastructure, retries/raw/health. ML1 owns downstream semantic dedup, Event extraction/classification, index content. Contract input/output source-independent. Backend exposes admin contract after permission checks; source management не agent tool.

TEAM DECISION: preferred access — publisher API/feed → RSS → Atom → другой официальный structured feed → допустимый HTML fallback. Generic RSS adapter обслуживает несколько конфигураций. Нет обещания распарсить любой URL; неподдержанный adapter требует разработки и review.

## Source Registry semantic fields

ASSUMPTION DTO: source_id, name, source_role (external/bank/expert), source_type, trust_level, adapter_type, endpoint/config (без secrets в API), enabled, polling_interval_seconds, last_sync_at, last_success_at, optional cursor, health, sanitized last_error, config_version. Enum trust_level и дополнительные adapters TBD; API использует string с документированными классами без выдуманной калибровки. Credentials — secret reference, не строка секрета в editable config.

TEAM DECISION: различать Alfa/internal, trusted external и другие валидированные классы. Trust source и relevance material — разные вещи; bank source не делает любой прогноз актуальным навсегда. Новый source не становится trusted автоматически только потому, что его добавили.

## Raw / normalized / versions

TEAM DECISION: raw сохраняется для replay без повторной внешней загрузки. ASSUMPTION raw provenance: source_id, fetched_at, request cursor, content checksum, media type и raw storage ref. Storage Q10 TBD; S3 из DB RFC пока только вариант.

NormalizedArticle содержит article/version ID, source_id, external_id при наличии, canonical_url при наличии, title, body, published_at при наличии, fetched_at, language/author при наличии, metadata, raw reference. Publication date не заменяется fetched_at незаметно. Документ Альфы может быть аналитическим PDF, а не новостной Article: общий document/evidence boundary сохраняет его тип.

Technical identity: source + external ID; canonical URL; stable fingerprint fallback. Метод fallback и normalization config version review после corpus EDA. Смена текста известной публикации создаёт content version, а не новый независимый Event. Идентичный материал не должен повторно оплачивать embedding/extraction; семантические дубликаты между publishers решает ML1 отдельно.

ASSUMPTION: stages имеют идемпотентный ключ и persisted outcome; два overlapping polls не публикуют дубли. Scheduling lock/cursor transaction/reconciliation technology TBD по выбранному lifecycle/storage. Сохранять cursor только после подтверждённого persist соответствующей порции, иначе возможен пропуск материала.

## Polling, errors и health

TEAM DECISION: intervals per Source; news polling не применяется к client datasets. FUTURE: webhook. Нет обязательного broker.

ASSUMPTION health: `UNKNOWN`, `HEALTHY`, `DEGRADED`, `UNAVAILABLE`; enabled отдельно от health. Disable останавливает новые автоматические sync, не удаляет старый corpus. Manual sync отключённого source — правило TBD admin UX; предлагается отклонять до enable, явно описано API как draft.

Bounded retries/backoff с учётом provider rate limits; thresholds/concurrency TBD измерениями. Permanent unsupported/config errors не нужно бесконечно повторять. Rate-limit, fetch, parse, persistence и downstream-processing errors учитываются отдельно. Fetch success не означает index readiness; Source.last_success и processing health не смешивать.

Падение adapter изолировано по source; сохранённые новости не удаляются. Retry same input возвращает тот же processing identity. Raw saved/metadata failed восстанавливается reconciliation. Изменение parser/index config требует replay нужных версий с trace, без перезаписывания evidence старых Insight.

## Add source workflow

1. Bank Admin/Analyst с разрешением (Q01) выбирает поддержанный adapter и вводит валидируемую config.
2. Backend/ML3 проверяют schema, allowed network target/redirects, secret refs, polling interval и permissions.
3. Создаётся Source, trust assignment проходит agreed policy (пока TBD), UI показывает initial health.
4. Manual sync/scheduler создаёт отслеживаемую sync operation; результаты и ошибки видны в control plane.
5. Новый custom adapter получает fixture tests: success/empty/malformed/timeout/rate-limit/duplicate/changed content/unsafe URL. Эти tests выполняются при реализации, сейчас только план.

SourceManagement показывает list/detail, type/trust/enabled, last sync/success, errors, adapter capabilities, config edit и manual sync state. Нет secrets/raw stack traces. Retention Q10/Q13 TBD, automatic deletion original materials не вводится без политики, сохраняющей claim provenance.
