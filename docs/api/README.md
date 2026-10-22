# API contracts v0.1

Status: DRAFT. TEAM DECISION D11: два OpenAPI 3.1 с общими схемами. Paths, IDs, enum деталей, pagination и error mapping — ASSUMPTION D16, предложены для contract review и FE mocks. Наличие YAML не означает одобрение всех полей и готовность к запуску production API.

- [openapi.yaml](openapi.yaml): Frontend ↔ Backend; business context, feed, alerts, Insight, conversation, feedback, safe trace, analytics и admin.
- [ml-openapi.yaml](ml-openapi.yaml): Backend ↔ ML; profiles, ranking/retrieval, generation/follow-up, registry/sync и observability.
- [schemas.yaml](schemas.yaml): общие BusinessContext, ProfileRef, Evidence, Claim, AgentResponse и вспомогательные DTO. Относительные `$ref` — единое определение, не копии структур.

## Почему две спецификации

Внешний API задаёт пользовательские ресурсы, внутренний — trusted ML requests и tooling границу. Frontend не передаёт роль, User или profile snapshot как доверенный input; Backend определяет их и формирует TrustedRequestContext. Shared schemas позволяют ML1 вернуть EvidencePackage, ML2 — AgentResponse, а Backend — Insight без ручного преобразования смысла claims. Не следует выставлять internal endpoints наружу вместе с public API.

## Что намеренно не выбрано

Q01: нет securityScheme/login/session endpoints и конкретной auth transport схемы. Это открытый блокер, а не декларация анонимного доступа. Все client/admin/internal операции требуют проверки доверенного контекста; проверка grant для Group и разделение admin/client должны быть согласованы отдельно. Mocks не доказывают безопасность.

Q07: sync response `200` и async `202` — альтернативы initial design. Нельзя считать их выбранным hybrid без benchmark/review. Candidate Generation/status endpoints помечены; frontend может мокировать оба поведения для UX review. До реализации выбрать вариант, зафиксировать RFC/ADR при принятии, убрать невыбранные ветви и завершить response/Location/retry/result semantics.

Q14: opaque string ID не определяет тип SQL PK; physical mapping и версии рассматриваются со схемой БД. ProfileRef(version) и document_version_id обязательны для provenance, конкретный формат версий — draft.

## Contract invariants

1. Company XOR Group моделируется `oneOf`, field kind отличает scope. Context ID не является авторизацией.
2. Все существенные публичные Claim имеют evidence links на объекты в AgentResponse.evidence. Ссылки материалов и features версионные; произвольные source URLs без provenance не заменяют grounding.
3. Claim type и verification различаются. UNSUPPORTED доступен во внутренней проверке, но не публикуется как factual result. Для PARTIALLY_SUPPORTED нужны caveats/limitations; forecast support — поддержка оснований.
4. Section.claim_ids разрешаются в claims; исключённый claim не остаётся в section. ALFA_ANALYTICS claims имеют bank evidence; inference явно различим.
5. Insight фиксирует Event/context/role/profile. Conversation наследует их; body message не может менять underlying scope. История ordered и сохраняется между сессиями; concurrent turn semantics требуют review.
6. AgentResponse/Insight не содержат secrets/private reasoning. Public trace только safe projection после проверки прав.
7. Idempotency-Key proposed для платных/создающих операций; scope/retention/replay semantics согласовать до реализации. Он не разрешает cross-user cache финальных ответов.
8. Cursor pagination для коллекций; для feed cursor привязан к snapshot ranking/profile/config. Изменение context/filter требует нового прохода. Stable tie-breaker и expiry review при implementation.
9. SourceConfig — adapter allowlist, schema validation, safe endpoints/redirects и secret references. `adapter_config` не обход проверки сети/credentials.
10. Ошибки едины; отсутствие evidence отличается от ML outage. Safe limited response может быть успешным результатом, полный провал grounding — INSUFFICIENT_EVIDENCE. HTTP mapping и retry policy review перед freeze.

## FE mocks / BE / ML handoff

FE может строить review mocks по schemas: feed success/empty/profile-not-ready, full/partial Insight, loading/failed generation, follow-up, source degraded и no-data quality metrics. Эти mocks — fixtures, не обещание реализации всех candidate paths. BE и ML3 совместно принимают HTTP/error/lifecycle boundary; ML1/ML2 проверяют EvidencePackage/AgentResponse и claim invariants. Интеграционные tests реализации должны проверять serialization, external refs, scope isolation, duplicate requests и version consistency.

Начальные полностью синтетические примеры: [feed](examples/feed.json),
[Insight с ограничениями](examples/insight-limited.json),
[EvidencePackage](examples/evidence-package.json). Это иллюстрации DTO, не результаты работы модели
и не реальные новости/данные банка. `requires-review` в примере — fixture label, не выбранная продуктовая шкала.

## Требования к проверке контрактов

Перед contract freeze проверять OpenAPI 3.1 и JSON Schema, разрешение `$ref`, уникальность operationId, обязательные path parameters и соответствие fixtures схемам. В CI выбранного стека добавить contract tests для serialization, scope isolation, idempotency и version consistency. Проверка структуры DTO не заменяет проверку авторизации и поведения работающих сервисов.
