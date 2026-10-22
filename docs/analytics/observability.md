# Analytics, trace и observability

Status: DRAFT. TEAM DECISION D05/D07/D09: три уровня наблюдаемости, trace_id, feedback и внутренний Quality UI. В v0.1 operational/analytics data могут храниться в PostgreSQL; отдельная analytics DB не выбрана.

## Semantic event contract

ASSUMPTION AnalyticsEvent: analytics_event_id, event_type, occurred_at, pseudonymous user_id при наличии, context_id при наличии, insight_id/revision при наличии, trace_id при наличии, schema_version и валидируемая metadata. Не использовать event_id (новости) как primary ID telemetry. Backend выводит user/context из trusted request, не доверяет присланной identity. Client timestamps не являются серверным временем; записывать received_at на backend.

| Level / Event type | Producer | Минимальный payload / смысл |
|---|---|---|
| Product: feed_opened | FE→Backend | context, feed snapshot ref при наличии |
| Product: insight_opened | FE→Backend | insight/event preview ref, context |
| Product: source_clicked | FE→Backend | insight/message, source/evidence ref |
| Product: follow_up_asked | Backend | conversation/message ref, без текста question |
| Product: feedback_submitted | Backend | feedback_id, insight revision, rating/reason |
| Product: alert_opened | FE→Backend | alert_id, context |
| Quality: retrieval_completed | ML1/ML3 | config version, candidate/top-K counts, rank refs, latency |
| Quality: claims_verified | ML2/ML3 | counts по type/status, evidence gaps, verifier version |
| Technical: generation_completed/failed | ML3 | status/error code, duration, model/config refs, tokens/cost если есть |
| Technical: ingestion_completed/failed | ML3 | source/sync/stage refs, counts, error category |

ASSUMPTION: duplicate analytics_event_id дедуплицируется; event version и metadata schema согласованы. Public batch endpoint принимает только client product events; ML quality/system events — internal producers. Feedback хранится отдельно и порождает telemetry сервером, чтобы FE retry не удваивал бизнес-сигнал.

## Trace

TEAM DECISION: trace_id объединяет Backend request, profile loading, retrieval, tools, LLM calls, verification, итоговый response. Предложенный TraceStage DTO: stage_id, parent_stage_id optional, stage_type, start/end, status, sanitized input/output refs, profile/evidence refs, model/config, metrics, error category. Это структурированные наблюдаемые результаты, не hidden chain-of-thought и не полный reasoning transcript.

Один generation/follow-up request имеет собственный trace; связь с предыдущим Insight/trace сохраняется correlation refs. trace_id распространяется через Backend↔ML; exact header/log propagation implementation review ML3. Ingestion может иметь отдельный trace/sync ref, связанный через article/evidence provenance с пользовательским анализом.

ASSUMPTION projections: client trace показывает разрешённые sources/features/этапы проверки; internal trace содержит operational metadata по отдельной capability. Access policy Q01; public API не возвращает всю raw trace. Secrets, full payloads, raw Person data, model private reasoning и arbitrary user prompts не копируются в logs/analytics по умолчанию.

## Метрики и определения

| Metric | Расчёт / ограничение |
|---|---|
| NDCG@K, Recall@K, MRR | На labeled query/context/role corpus, с dataset/config versions; primary и K TBD |
| Expert usefulness / personalization | Рубрика глубины, правильности связи с profile, non-generic interpretation; отдельно от кликов |
| Positive feedback rate | positive / все валидные оценки; рядом количество/период, не доля всех пользователей |
| Grounding coverage | Существенные опубликованные claims с валидными evidence links / все существенные опубликованные claims; наличие link не доказывает support |
| Unsupported assertion rate | Экспертно неподдержанные утверждения / проверенные опубликованные claims; reported verifier status отдельно |
| Bank attribution correctness | Правильно приписанные банку claims / проверенные bank claims; пустая выборка = нет данных |
| Alert quality | Expert precision/recall по high-impact labels; threshold/policy version и sample size |
| Latency | p50/p95 по feed/full generation/follow-up/source processing; успешные/failed requests отдельно |
| Source health / freshness | Последний fetch success, lag publication→index readiness, parse/index failures, coverage |
| Model cost / usage | Provider tokens/cost при наличии, local estimate с маркировкой estimate, общий бюджет и неполнота shared usage |
| Cache | Hits/misses по типу промежуточного результата и версии; не смешивать с качеством ответа |

TEAM DECISION: random percentage confidence не показывается пользователю. Primary metric и target SLO/thresholds TBD Q11, измерения с sample size. CTR/follow-up/source clicks — secondary diagnostics, не ground truth relevance.

## Quality UI и процесс

TEAM DECISION: Source Management и Quality могут быть разделами одного internal control plane. Показывать source/index/model health, usage, feedback/reasons, latency/errors, tokens/cost, alert stats. Offline ranking/verification evaluation показывать с датой, model/config/corpus version; если labels/usage недоступны — «нет данных», не ноль и не выдуманная оценка.

ASSUMPTION monitoring workflow: ML1 reviews retrieval/source coverage; ML2 reviews unsupported/attribution failures и negative feedback; ML3 monitors availability/cost/latency; BE/FE owners проверяют user flow/access. Cadence и alert notification channel TBD. Изменение модели/ranking проходит held-out regression перед новым config version.

## Storage и retention

TEAM DECISION: минимальный telemetry payload, exact retention TBD Q13. Raw evidence, profile snapshots, conversations, trace и aggregate analytics имеют разные цели/сроки. Удаление raw evidence не должно оставлять якобы проверяемый Insight с битой ссылкой; нужны archive/tombstone policy и review. Политика хранения/доступа не считается утверждённой.

FUTURE: ClickHouse/другая analytics sink только если измерены рост объёма/retention, contention OLTP, стоимость аналитических запросов и нарушение согласованных SLO. Сначала benchmark partition/index/aggregation в выбранной архитектуре; затем RFC с migration/backfill/idempotency/consistency plan. Нет автоматического dual-write v0.1, который создаёт неподтверждённые гарантии доставки.
