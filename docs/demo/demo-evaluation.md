# Demo и evaluation

Status: DRAFT. CASE REQUIREMENT: работающий интерактивный прототип, новые события и полный сценарий; TEAM DECISION: одинаковый corpus для различных contexts и roles. Все personas ниже ASSUMPTION, синтетические, не реальные клиенты/данные Альфа-Банка.

## Предлагаемые fixtures

| Fixture | Синтетический контекст | Контраст, который проверяем |
|---|---|---|
| Company A | Производитель, заёмные продукты, подтверждённые fixture payment relations | Финансирование и операционные зависимости |
| Company B | Экспортно-ориентированный бизнес с явно заданными fixture FTS countries | Географические/валютные сценарии только при evidence |
| Company C | Внутренний сервисный бизнес, deposit product, другой payment graph | Альтернативная релевантность одного corpus |
| Group G | A + дополнительная синтетическая компания с иным профилем | Company vs Group, contribution refs и aggregation gaps |
| Personas | CEO/CFO для одной компании; отдельный Bank Analyst | Role-aware interpretation и client/admin boundary |

Суммы/периоды/страны определяются fixture автором после mapping данных, не выдаются за исследованные CSV. Synthetic User→Person mappings и роли задаются явно; group grants остаются Q01. Не показывать Group demo как защищённый доступ, пока права не согласованы.

Один фиксированный corpus включает: событие о ставке, отраслевое/сырьевое событие, внешнеторговое изменение, нерелевантное событие, несколько статей об одном Event, аналитику Альфы с датой и конфликтующий/устаревший материал. Конкретные факты из реального мира проверяются при наполнении corpus; здесь это категории fixtures, не актуальные новости.

## Live demo script

1. Запуск по README/Docker; показать версии corpus/profile/config, не презентацию заранее нарисованных результатов.
2. Войти synthetic persona, открыть заметную точку входа, выбрать Company A; показать lightweight feed и «почему вам».
3. На том же corpus сменить контекст на B/C: показать различия ranking, features и explanations; обсудить случаи, где совпадение ranking обоснованно, не форсировать искусственное различие всех карточек.
4. Открыть один Event у CEO и CFO A: full analysis on demand, разные business angles при одинаковых исходных фактах.
5. Выбрать Group G при согласованных правах: показать вклад конкретных компаний и ограничение aggregation, а не просто сумму показателей.
6. Раскрыть Claim → material version/chunk и ProfileFeature; показать отличие bank position от AI inference/forecast.
7. Задать связанный уточняющий вопрос, требующий нового evidence; показать tool/retrieval stage, verification и grounded response. Перезагрузить сессию и проверить историю.
8. Показать insufficient/conflicting evidence case: корректная оговорка/ограничение, без выдуманной позиции банка.
9. Ввести ранее не показанный материал через поддержанный adapter: technical dedup, semantic Event processing и изменение feed без hardcoded response.
10. В Source Management показать manual sync, ошибку одного source и сохранение остальных; в Quality UI trace/latency/cost/feedback с версионностью.
11. Оставить отрицательный feedback с reason, показать сохранённую запись; behavioural retraining не обещать.

## Evaluation artifact contract

ASSUMPTION: labeled rows содержат sample_id, corpus snapshot, business_context/profile ref, role, event, graded relevance, expected company features, allowed bank evidence, expected contradictions/limitations и annotator notes. Grounding rubric оценивает каждый существенный claim; эксперт не требует буквально одинакового текста.

Рубрика: factual correctness; конкретная связь с business context; корректность role angle; многофакторность; bank attribution; entailment evidence; полнота risks/opportunities/scenarios без выдумок; honest uncertainty; helpfulness. Scale/primary/K/sample size TBD, согласовать до измерения кандидатов. Разметить спорные примеры, провести adjudication, сохранить disagreement. Тuning и held-out Event families не пересекаются; новые news не заменять только перефразированными duplicates.

## Measurable success и negative scenarios

| Проверка | Evidence of pass |
|---|---|
| Personalization | Labels + ranking metrics по context/role и экспертная оценка объяснений; baseline comparison |
| Claim grounding | Ссылки разрешаются; sampled entailment audit; unsupported assertions измерены |
| Agent behavior | Trace показывает planning result, calls, verification и adaptation на gap case |
| Data isolation | Запрещённый context/conversation/trace/admin operation не возвращает данные; Q01 gate |
| Robustness | Missing fields, malformed document, contradictory bank/news, timeout, budget exhausted и retry дают описанные состояния |
| New material | Fresh ingestion проходит до feed/Insight без заготовленного результата |
| Version stability | Новый Profile/Article не меняет основания сохранённого Insight silently |
| Operations | Duplicate sync/message retry не создаёт дубли/бесконтрольные платные calls; measured cost/latency |

Численные target values не объявлены до baseline (Q11). Дизайн готов к review, а продукт не считается validated до реальных измерений. Не заявлять победу над baseline без результатов.

## Финальная сдача (CASE REQUIREMENT, с. 8)

- GitHub repo с историей и pinned deadline commit/tag; доступ `alfa-hack-2026` для private repo.
- README: purpose, architecture, env/config, запуск, компоненты и пошаговый сценарий.
- Docker и единый воспроизводимый startup, language/dependency locks, `.env.example` без secrets.
- THIRD_PARTY_NOTICES: components/models/datasets/services, licenses и simulated integrations.
- Synthetic E2E fixtures и test accounts с документированными scopes/roles.
- Интерактивный прототип, не только prepared screens; external server рекомендуется.

Runtime deliverables входят в критерии готовности итогового прототипа; их наличие проверяется перед сдачей.
