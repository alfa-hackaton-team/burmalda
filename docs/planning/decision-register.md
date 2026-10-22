# Decision register

Дата: 2026-10-22. Классификация: [правила](../README.md). Source CASE = условия кейса; TEAM = исходные решения команды; DESIGN = предложение initial design. Колонка Needs question? указывает, что требует согласования до принятия или реализации.

## Определённые границы

| ID / Decision | Status | Source | Impact | Needs question? |
|---|---|---|---|---|
| D01. Operational DB PostgreSQL, ML Python/FastAPI/Pydantic, Backend↔ML HTTP/JSON | TEAM DECISION | TEAM; [архитектура](../architecture/architecture.md) | Границы сервисов; backend/frontend stack не задан | Нет |
| D02. Insight = primary Event + Company XOR Group + Role; supporting events допустимы | TEAM DECISION | TEAM; [модель данных](../architecture/data-model.md) | Семантика персонализации | Нет |
| D03. CompanyProfile заранее построен, сохранён и переиспользуется; refresh/rebuild нужен | TEAM DECISION | TEAM; [ML design](../ml/ml-design.md) | ML1 и provenance | Нет |
| D04. Person/company/group/payment context входит в ML1 | TEAM DECISION | TEAM; [ML design](../ml/ml-design.md) | EDA и модель связей; алгоритмы не выбраны | Нет |
| D05. EvidencePackage, Claim grounding, trace_id; факты/банк/AI/forecast разделены | TEAM DECISION | TEAM; [модель данных](../architecture/data-model.md), [observability](../analytics/observability.md) | Контракты и verification; реализует C05/C06/C11 | Нет |
| D06. Full analysis on demand, preview заранее; без слепого финального cache | TEAM DECISION | TEAM; [PRD](../product/PRD.md) | Стоимость/latency | Нет; sync/async Q07 |
| D07. Insight-bound dialogue, persistence history, feedback, alerts | TEAM DECISION | TEAM; [PRD](../product/PRD.md) | UX и хранение | Нет; thresholds TBD |
| D08. Registry + adapters + scheduled configurable polling (~15 min ordinary source) | TEAM DECISION | TEAM; [ingestion](../ingestion/source-ingestion.md) | Ingestion; не обновление client CSV | Нет |
| D09. Source Management и Quality control plane входят в v0.1 | TEAM DECISION | TEAM; [PRD](../product/PRD.md) | Admin API/UI | Нет; разрешения Q01 |
| D10. Только шесть LLM из кейса; бюджет 500 ₽/день | CASE REQUIREMENT | CASE с.12,18 | Gateway и лимиты | Нет; provider capabilities Q05 |
| D11. Два OpenAPI с общими схемами | TEAM DECISION | TEAM; [API contracts](../api/README.md) | Public/internal contracts | Нет |
| D12. Авторизация и group entitlement требуют отдельного согласования | TEAM DECISION | TEAM; [security boundaries](../architecture/architecture.md#security-boundaries) | Auth contract остаётся TBD | Gate Q01 до реализации |
| D13. Собственная оркестрация; один агент с tools как проектный baseline | ASSUMPTION | DESIGN; [ML design](../ml/ml-design.md) | ML2; CASE допускает одного/несколько | Review; не технология/framework |
| D14. Story и behavioral ranking не реализуются v0.1 | FUTURE | TEAM; [PRD](../product/PRD.md) | Расширение модели | Нет |
| D15. Kafka/Kubernetes/ClickHouse/Redis/dedicated vector DB не обязательны | TEAM DECISION | TEAM; [архитектура](../architecture/architecture.md) | Не добавлять runtime зависимости автоматически | Нет |
| D16. Opaque string IDs, DTO, endpoint names, pagination и lifecycle ниже | ASSUMPTION | DESIGN | API v0.1 DRAFT; не физические типы БД | Совместный contract review |
| D17. Raw versions и profile snapshots доступны для воспроизведения claims | ASSUMPTION | DESIGN + CASE C06/C11 | Неизменяемые ссылки и reproducibility | Сверить с финальной схемой |

## Открытые решения

Каждая строка имеет Status: TBD. Никакой вариант в колонке Candidates не выбран.

| ID / Decision | Source / Impact | Candidates | Decision criteria | Required experiment / EDA | Needs question? |
|---|---|---|---|---|---|
| Q01. Auth, роли, доступ к Company/Group, admin и trace | TEAM / security, все protected API | Явные demo grants; банковский trusted context позже; способ session/token TBD | Отсутствие scope escalation; role source; отделение Person от User | Составить access matrix; проверить group/member и admin/client negative cases | Да, до реализации auth |
| Q02. Backend/frontend stacks | TEAM / runtime | Предложения владельцев компонентов; repo пока ничего не фиксирует | Компетенции команды, сроки, API/tooling, Docker | Короткий feasibility review backend/frontend owners | Да, до stack-specific реализации |
| Q03. Vector storage | TEAM / retrieval infrastructure | pgvector; dedicated vector storage; exact local index для baseline | Recall/latency, corpus size, память, operational cost | Одинаковый corpus/query set и benchmark; RFC 0001 | Review после результатов |
| Q04. Retrieval/reranker/chunking/weights | TEAM / ML quality | Lexical; embeddings; hybrid; optional reranking; chunk variants | Recall/NDCG/MRR, grounding coverage, latency/cost | Held-out eval; ablation с/без graph/role/Alfa | Нет сейчас; выбор после eval |
| Q05. LLM/embedding model и provider mapping | CASE+TEAM / budget и compatibility | Для LLM только allowlist; embeddings/reranker только после проверки допустимости | Quality, tools/JSON reliability, доступность, context, cost | Проверить gateway model IDs/limits, benchmark extraction/generation/verification, уточнить разрешённость non-LLM models | Да, до выбора моделей |
| Q06. Graph features, periods, group aggregation | TEAM / profiles, indirect exposure | Направленные aggregate edges; validated concentration/exposure features; более сложные алгоритмы только при пользе | Обоснованность, data completeness, no double count | EDA PAY_AMT/PAY_CNT/currency/time; membership, unknown endpoints; feature ablation | Нет сейчас |
| Q07. Sync/async generation | TEAM / public/internal API behavior | Sync; async status resource; hybrid | p95 observed latency, request timeout, retry/cancel behavior, cost | Benchmark end-to-end agent + slow tools; RFC 0002 | До contract freeze; сейчас варианты в OpenAPI |
| Q08. Cache implementation и invalidation | TEAM / reuse и isolation | In-process; PostgreSQL-backed; отдельный cache только при необходимости | Isolation, profile/evidence/model changes, memory, hit rate | Measure repeated requests; adversarial cross-role/scope tests | Нет сейчас |
| Q09. Client dataset updates / profile freshness | TEAM / data acquisition | Versioned manual import; provided update mechanism если появится | Фактическая поставка, snapshots, freshness | Изучить поставку и согласовать с организаторами; не придумывать polling | Да, перед refresh automation |
| Q10. Raw storage и retention | DESIGN / storage | Private object storage (в т.ч. RFC S3); persisted local volumes для demo; DB metadata + chosen raw layer | Reproducibility, privacy, Docker, size, recovery | Corpus size/format EDA; recovery test; retention requirements | До infrastructure freeze |
| Q11. Alerts/levels/metrics thresholds и SLO | TEAM / UX, quality gates | Explainable qualitative scale; calibrated rule/model thresholds | False alarms, false negatives, expert usefulness, budget | Label high-impact subset; measure latency and cost distributions | Нет сейчас; не показывать random scores |
| Q12. Input field semantics и payment filename | CASE / import correctness | Фактические manifests и data dictionary | Units, bins, nulls, duplicates, IDs, dates | EDA files; workers gradation ≠ точный headcount; NEGATIVEMARK не трактовать по названию | До import mapping |
| Q13. Retention/chat context budget/disclaimer copy | TEAM / privacy, UX | Policies по классам данных; bounded history + grounded summary; conditional disclaimer | Auditability, минимизация, prompt budget, legal/product review | Token/history eval; recovery/provenance; review copy | До production; demo сохраняет history |
| Q14. ID mapping и physical schema | DESIGN / DB ↔ API | Opaque domain IDs + explicit synthetic import keys; physical types по схеме | Referential integrity, no ambiguous Company/Group IDs | Совместить final DB RFC и DTO; migrations позже | Да, перед schema/contract implementation freeze |

## Contract readiness

API paths/fields предложены для review и mocks, не выдаются за согласованные командой решения. Авторизационные поля/схемы отсутствуют намеренно (Q01), это не публичный анонимный API. Q07 оставляет transport lifecycle невыбранным; в specs помечены альтернативные ответы. Q14 не позволяет считать DTO физической схемой БД.

Для начала реализации нужны review стабильной части semantic contracts, отдельное принятие lifecycle и auth boundary. Для экспериментов ML доступна независимая работа с синтетическими scoped fixtures.
