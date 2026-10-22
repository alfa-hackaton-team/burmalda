# Case compliance matrix

Источник: предоставленный PDF «Альфа Хак_Новостная аналитика», страницы 1–19. Это матрица покрытия дизайном, не отчёт о работающем продукте. TEAM additions указаны отдельно; веса критериев жюри не опубликованы в PDF.

| ID / Case requirement | Design response | Document where covered | Status / проверка |
|---|---|---|---|
| C01. Крупный бизнес >15 млрд ₽, люди с разными ролями, с. 4–5 | Контекст Company/Group и доверенная роль | [PRD](../product/PRD.md), [model](../architecture/data-model.md) | CASE REQUIREMENT; сравнить роли на одном событии |
| C02. Бизнес-контекст и значимость событий, с. 6, 9 | CompanyProfile, связи, explainable ranking | [ML](../ml/ml-design.md) | CASE REQUIREMENT; EDA/разметка ещё нужны |
| C03. Парсер внешних новостей и аналитики, с. 6 | Registry, adapters, raw/normalized, event processing | [ingestion](../ingestion/source-ingestion.md) | CASE REQUIREMENT; publishers TBD |
| C04. Последствия, риски и возможности с несколькими факторами, с. 9, 11, 14 | Structured Insight и claims на feature + news + bank evidence | [PRD](../product/PRD.md), [ML](../ml/ml-design.md) | CASE REQUIREMENT; экспертная рубрика |
| C05. Видимая позиция банка, отличимая от AI, с. 9, 11, 14 | ALFA_ANALYTICS отдельно от AI_INFERENCE/FORECAST | [model](../architecture/data-model.md), [API](../api/README.md) | CASE REQUIREMENT; attribution check |
| C06. Источники, минимум фактологических ошибок, с. 9, 14 | Claim-level grounding, immutable references, verification | [model](../architecture/data-model.md), [ML](../ml/ml-design.md) | CASE REQUIREMENT; claim audit |
| C07. Неизвестные ранее события, с. 9 | Held-out events/time split, ingestion новых материалов | [demo](../demo/demo-evaluation.md) | CASE REQUIREMENT; без заготовленных ответов |
| C08. Полный сценарий и заметная постоянная точка входа, с. 9–10 | Alfa-Business shell → контекст → лента → инсайт | [PRD](../product/PRD.md) | CASE REQUIREMENT; интерактивное E2E |
| C09. Follow-up, иной разрез, первоисточник, с. 10 | Insight-bound conversation, retrieval tools, source links | [API](../api/openapi.yaml), [ML](../ml/ml-design.md) | CASE REQUIREMENT; grounded dialogue |
| C10. Планирование, tools, context, self-check/adaptation, с. 7, 10 | Собственная оркестрация, verification → повторный retrieval | [ML](../ml/ml-design.md) | CASE REQUIREMENT; наблюдаемый recovery trace |
| C11. Трассируемость действий и оснований, с. 10 | trace_id, stages, версионные Evidence и Profile refs | [observability](../analytics/observability.md) | CASE REQUIREMENT; восстановить цепочку |
| C12. Масштабирование, сопровождение, метрики и feedback, с. 10 | Adapter boundaries, eval, quality UI, Feedback | [architecture](../architecture/architecture.md), [observability](../analytics/observability.md) | CASE REQUIREMENT; workflow мониторинга |
| C13. Неполные, неоднозначные, шумные данные, с. 12 | missingness, contradictions, insufficient evidence | [model](../architecture/data-model.md), [demo](../demo/demo-evaluation.md) | CASE REQUIREMENT; adverse tests |
| C14. Разрешённые LLM и дневной бюджет 500 ₽, с. 12, 18 | Allowlist, gateway, token/cost accounting, controlled retries | [ML](../ml/ml-design.md) | CASE REQUIREMENT; model IDs/prices проверить у провайдера |
| C15. Собственная агентная логика без готовой платформы как ядра, с. 18 | Один orchestrating agent как baseline, собственные tools | [ML](../ml/ml-design.md) | CASE REQUIREMENT; code review при реализации |
| C16. Права, чувствительные данные, tool logs и ограничения действий, с. 12 | Проверки backend, read-only analysis tools, redaction | [architecture](../architecture/architecture.md) | CASE REQUIREMENT; auth Q01 открыт, production gate |
| C17. Компания, физлица, payments и банковская аналитика, с. 15–17 | Раздельные исходные сущности, связи, profile provenance | [model](../architecture/data-model.md), [ML](../ml/ml-design.md) | CASE: данные предоставлены; TEAM: graph в v0.1; EDA не проведена |
| C18. GitHub/history/pinned version, README, Docker, locks, notices, synthetic E2E, с. 8 | Release checklist и единый запуск | [demo](../demo/demo-evaluation.md), [architecture](../architecture/architecture.md) | CASE REQUIREMENT; runtime deliverables позже |
| C19. Внешний сервер, с. 8 | Возможная demo deployment после выбора stacks | [architecture](../architecture/architecture.md) | Рекомендация кейса, не обязательный выбранный hosting |

## Дополнения команды, не приписываемые кейсу

TEAM DECISION: on-demand full generation, high-impact alerts, Source Management и Quality UI, Python/FastAPI/Pydantic для ML, PostgreSQL, HTTP/JSON, два OpenAPI, polling около 15 минут, сохранённый CompanyProfile, Article/Event/Insight semantics и scope-bound chat.

FUTURE: Story engine, behavioral recommender, webhook ingestion, возможное расширение analytics storage. Число агентов, наличие vector DB или число сервисов сами по себе не являются критериями успеха кейса.
