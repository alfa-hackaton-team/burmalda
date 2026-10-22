# RFC-0001: Vector storage boundary

Status: DRAFT

Authors: TBD. Reviewers / approvers: TBD.

Created: 2026-10-22. Last updated: 2026-10-22.

## Context

TEAM DECISION: PostgreSQL operational DB. Vector storage TBD Q03. DB RFC другой ветки предлагает pgvector, но не принят. Retrieval approach/embedding dimensions/corpus scale ещё не измерены.

## Problem

Выбор storage до EDA может добавить ненужную зависимость или ограничить retrieval quality/latency. Нужно согласовать index adapter boundary и критерии, не фиксируя технологию.

## Goals

Сравнимое качество поиска, version isolation embeddings, citations к стабильным material refs, измеренные latency/resource cost, воспроизводимый Docker deployment.

## Non-goals

Не выбор embedding/reranker модели, не ranking weights, не отдельная graph DB.

## Options considered

### A. PostgreSQL + pgvector

Плюсы: потенциально единый operational stack и удобство filters/joins. Минусы: нужно измерить index tuning/resource contention и фильтрацию на фактическом corpus; наличие PostgreSQL само по себе не доказывает достаточность этого варианта.

### B. Dedicated vector storage

Плюсы: потенциальная независимая эксплуатация/масштабирование индекса. Минусы: дополнительный runtime/service, синхронизация IDs/version consistency и recovery. Конкретный продукт не выбран.

### C. Exact/local index для экспериментального baseline

Плюсы: контроль качества/простой reproducible benchmark на небольшом corpus. Минусы: production filtering, concurrency, persistence и память могут не соответствовать потребности. Не заявляется как окончательный runtime выбор.

## Evaluation criteria

Recall@K/NDCG@K на одинаковых vectors/queries, p50/p95 latency с фильтрами, ingestion/rebuild cost, память, operational contention, воспроизводимость/recovery, сложность сопровождения.

## Evidence / Experiments

Пока результатов нет. Нужны corpus EDA, допустимая embedding model, одинаковые dimension/config, filtered queries и load/rebuild tests. Не смешивать в одном сравнении смену модели, ranking и storage без ablation.

## Proposed solution

Вариант не выбран. Предлагается согласовать retrieval adapter contract с versioned document/chunk refs и провести benchmark A/B/C. Это предложение процесса, не принятие storage.

## Consequences

После решения обновить Q03, architecture, deployment, ML design, final physical schema и ADR при значимости. До этого не добавлять обязательный pgvector/Qdrant/другой runtime dependency.

## Risks

Нерепрезентативный tiny corpus, leakage labels, сравнение разных embeddings, игнорирование citation consistency и общей стоимости поддержки.

## Open questions

Corpus size, update frequency, target latency, filters, memory budget, модель/размерность и допустимость embedding tooling.

## Decision

Не принято. Review и результаты benchmark требуются.

## Related

[Q03–Q05](../planning/decision-register.md), [ML design](../ml/ml-design.md), [data model](../architecture/data-model.md).
