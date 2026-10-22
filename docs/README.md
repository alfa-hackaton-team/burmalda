# Initial design v0.1

Версия: 0.1. Дата: 2026-10-22. Статус комплекта: DRAFT, на review команды.

После review и merge в main согласованные разделы становятся source of truth. Пункты TBD остаются открытыми до отдельного решения; статус DRAFT не разрешает считать все предложенные контракты утверждёнными.

## Как читать

| Документ | Ответственность |
|---|---|
| [Матрица кейса](planning/case-compliance.md) | Требование → ответ дизайна → проверка |
| [Реестр решений](planning/decision-register.md) | Статусы, основания, TBD и эксперименты |
| [PRD](product/PRD.md) | Пользователи, поведение, scope, приёмка |
| [Архитектура](architecture/architecture.md) | Компоненты, границы и отказы |
| [Модель данных](architecture/data-model.md) | Общий словарь, идентичность и provenance |
| [Public OpenAPI](api/openapi.yaml) | Предлагаемый Backend ↔ Frontend contract |
| [Internal OpenAPI](api/ml-openapi.yaml) | Предлагаемый Backend ↔ ML contract |
| [Общие схемы](api/schemas.yaml) | DTO Evidence, Claim, Context и ответы |
| [Правила API](api/README.md) | Границы спецификаций и review gates |
| [ML design](ml/ml-design.md) | ML1/ML2/ML3, эксперименты и verification |
| [Ingestion](ingestion/source-ingestion.md) | Источники, adapters, health и повторная обработка |
| [Observability](analytics/observability.md) | События, trace, качество и контрольная панель |
| [Demo / evaluation](demo/demo-evaluation.md) | Сценарии, разметка и доказательства качества |
| [ADR process](decisions/README.md) | Как фиксировать принятые решения |
| [RFC process](rfc/README.md) | Как обсуждать предложения |

## Классификация

- CASE REQUIREMENT — прямое требование PDF кейса; источник: страница PDF в матрице.
- TEAM DECISION — согласованное исходное решение команды.
- ASSUMPTION — проектное предложение этого комплекта; не подтверждённый факт и не разрешение на реализацию без review.
- TBD — выбор не сделан; варианты, критерии и эксперимент находятся в реестре.
- FUTURE — вне реализации v0.1; сохраняем возможность развития без дополнительной инфраструктуры сейчас.

Требования и решения имеют идентификаторы Cxx, Dxx, Qxx, FRxx, NFRxx. Изменения DTO согласуются одновременно в двух API и использующих их документах. Физическая схема из `feature/db-rfc` предварительная; её согласование и сверка с semantic model обязательны до реализации хранения.

Риски, этапы реализации и распределение ответственности находятся в [архитектуре](architecture/architecture.md#риски-реализации).
