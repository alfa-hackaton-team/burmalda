# RFC-0002: On-demand Insight generation lifecycle

Status: DRAFT

Authors: TBD. Reviewers / approvers: TBD.

Created: 2026-10-22. Last updated: 2026-10-22.

## Context

TEAM DECISION D06: full analysis on demand; latency agent/retrieval/verification не измерена. Sync/async implementation explicitly TBD Q07. Прототип должен быть интерактивным, не бесконечным spinner.

## Problem

Выбор влияет на оба API, frontend states, retry/idempotency, persistence, concurrency и ресурс worker. Нельзя молча выбрать hybrid из-за наличия двух response codes в draft OpenAPI.

## Goals

Понятный status/result, контролируемый расход, отсутствие дублирующих платных calls, восстановление после disconnect, consistent trace и model failures.

## Non-goals

Не выбор broker/worker framework, не автоматическое добавление Redis/Kafka, не финальный cache персональных ответов.

## Options considered

### A. Synchronous request

Плюсы: единый response, меньше transport states. Минусы: timeout/disconnect, повторная оплата без idempotency; ограничение максимальной длительности upstream HTTP. Приемлемость доказывается latency distribution, не коротким happy-path вызовом.

### B. Async operation/status resource

Плюсы: долгий анализ и recovery после reload, явное исполнение/результат. Минусы: нужен persisted state, result retrieval и согласованное повторное чтение, UX polling/другая доставка. Отдельный broker не следует автоматически.

### C. Hybrid

Плюсы: быстрые результаты сразу, медленные продолжаются с status. Минусы: обе ветви в client/server, гонки/повторные ответы и дополнительный test surface.

## Evaluation criteria

Observed p50/p95/max и timeout rate, adaptation loops, concurrent requests, disconnect/retry recovery, idempotency, token/cost waste, реализация/эксплуатация и качество UX.

## Evidence / Experiments

Результатов нет. Измерить real allowed-model baseline с retrieval/verification и slow/error cases. Смоделировать reload и duplicate request при незавершённой генерации. Benchmarks для full Insight и follow-up отдельно.

## Proposed solution

Решение не выбрано. Draft specs описывают candidate 200/202 и status DTO для review. До freeze выбрать A/B/C, решить generation/result identity, terminal state mapping, Location/retry/deadline, concurrency и bounded retries. Cancellation policy может остаться FUTURE, если явно отсутствует в v0.1.

## Consequences

Обновить public/internal OpenAPI, FE states, Backend persistence и ML execution boundary, budgets/trace/acceptance. Удалить невыбранные candidate paths/response branches. ADR только после принятия.

## Risks

Оптимизация по одному быстрому вызову; потерянный result после timeout; duplicate paid analysis; status SUCCEEDED без доступного result; internal/public ID mismatch.

## Open questions

Transport timeouts, permitted LLM service latency/capabilities, result ownership, concurrency/lock policy и partial result semantics.

## Decision

Не принято; требует benchmark и review BE/FE/ML3/ML2.

## Related

[Q07](../planning/decision-register.md), [API rules](../api/README.md), [architecture](../architecture/architecture.md).
