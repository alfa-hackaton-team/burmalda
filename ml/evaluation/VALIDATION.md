# Проверка на новых текстах, аспекты и inference

Работа ограничена evaluation. Production Retriever/DTO/embedding profile, SQL и миграции не меняются. Исходный synthetic v1 и его результаты не перезаписываются.

**Статус: эксперименты остановлены по запросу пользователя.** Завершённые vector/CPU результаты и ограничения сохранены в [STATUS.md](reports/validation_v1/STATUS.md). MPS прерван, multi-aspect не запускался. Команды ниже — справка для воспроизведения, не указание продолжать tuning.

## Протокол до inference

Новый [corpus](data/official_holdout_v1.json): 12 официальных публикаций 2024–2025 годов, 51 чанк, 13 187 слов; документы дают от 1 до 18 чанков. 14 запросов с положительной разметкой и 4 corpus-relative no-answer запроса. Русские/английские тексты и запросы, соседние даты/ставки, отрицания, разные числовые прогнозы, 5 составных запросов. Text/qrels/aspects зафиксированы до первого модельного прогона: SHA-256 `a2ca1f979198f8d5c17b5511b5513720416b86305b73963cd0ae834fe78f58ee`.

Это отдельный корпус по отношению к synthetic v1, но **не независимая разметка другим человеком** и не промышленный gold dataset. Тематика преимущественно макроэкономическая; выводы нельзя переносить на все корпоративные новости. Разметка включает точные source spans и UUID; публикационно-специфические запросы явно называют источник/дату. Несколько чанков могут поддерживать разные аспекты или содержать повторяющуюся из-за overlap цитату.

Источники: [Пресс-служба Банка России](https://www.cbr.ru/press/pr/), [заявления FOMC](https://www.federalreserve.gov/newsevents/pressreleases/monetary20240918a.htm), [протокол FOMC](https://www.federalreserve.gov/monetarypolicy/fomcminutes20240918.htm). Каждый документ содержит исходный URL, дату, SHA-256 HTML/нормализованного текста и ссылку на reuse policy. [Банк России разрешает перепечатку со ссылкой на первоисточник](https://www.cbr.ru/about/?PrtId=use); [публикации Board находятся в public domain, если не указано иное](https://www.federalreserve.gov/disclaimer.htm). Сохранены полные текстовые блоки публикаций, без навигации и скриптов; приложения не включаются. Загрузка нужна только при подготовке snapshot, не при повторе evaluation.

## Воспроизведение

```sh
export HF_HUB_OFFLINE=1
# Только для vector stage: disposable PostgreSQL с заранее установленным vector.
export ML1_EVAL_POSTGRES_DSN='<test DSN>'
python -m ml.evaluation.validation_study vector --output-dir .local/validation
python -m ml.evaluation.validation_study profile --profile cpu-eager-b4 --output-dir .local/validation
python -m ml.evaluation.validation_study profile --profile cpu-eager-b8 --output-dir .local/validation
python -m ml.evaluation.validation_study profile --profile cpu-sdpa-b4 --output-dir .local/validation
python -m ml.evaluation.validation_study profile --profile mps-sdpa-b4 --output-dir .local/validation
python -m ml.evaluation.validation_study aspects --output-dir .local/validation
```

Vector stage проходит настоящий chunker → Qwen embeddings → persistence → pgvector Top-20 в изолированной временной схеме. Далее **один и тот же** сохранённый набор кандидатов используется во всех профилях/аспектах: различия не маскируются повторным поиском. Профиль, dataset/source fingerprints, environment, пары query/chunk, scores и timing записываются в JSON. При несовпадении dataset execution прекращается. Веса обеих моделей — стандартный HF cache, offline.

## Что именно сравнивается

- **No-answer:** Recall/MRR/nDCG математически не определены при пустом gold — в JSON `null`, из среднего по 14 answerable исключены. Отдельно изучаются принудительная выдача и scores на 4 no-answer. Ни threshold, ни автоматический abstention здесь не вводятся.
- **Multi-aspect:** заранее заданные вручную смысловые вопросы из исходного запроса, без документов/UUID/qrels в модельном input. Каждый аспект ранжирует те же 20 кандидатов. Объединение — детерминированный равновесный round-robin с удалением повторных UUID; logits разных запросов не усредняются. Измеряются Recall/MRR/nDCG и доля покрытых аспектов. Это проверка scoring/coverage при готовых аспектах, **не автоматического decomposer**.
- **Development отдельно:** 5 составных запросов synthetic v1, включая q13, получают [отдельные query-only aspects](data/synthetic_aspects_v1.json). Старый dataset не меняется, результаты не смешиваются с новым корпусом.
- **Inference:** одинаковые weights/revision/prompt/max length/float32; CPU eager batch 4/8, CPU SDPA batch 4, MPS SDPA batch 4. Два warmup batch до измерений, повтор фиксированного запроса для stability. Профили идут последовательно, не параллельно. Сравниваются качества, полный Top-5 порядок, score differences и median/max latency. Неподдерживаемый ускоритель — явная ошибка, не silent CPU fallback. Это один локальный прогон, не SLA; возможны тепловые/порядковые эффекты.

## Граница с ML2

[Восемь evidence handoff cases](data/ml2_evidence_cases_v1.json) сохраняют требования существующего `docs/ml/contracts.md`: точные авторизованные исторические чанки, буквальная цитата и provenance, затем проверка субъекта, даты, чисел/единиц, отрицаний и направления изменения. Несколько заведомо ложных claims имеют **точную существующую цитату**: механическое совпадение не является entailment. При отсутствии основания нельзя публиковать verified claim; используется существующий `insufficient_evidence`.

Это gold-кейсы и контрактные требования для ML2, **не новый semantic verifier**. Реализации ML2 verifier в этом checkout нет; его смысловая проверка этими экспериментами не подтверждается. Высокий reranker score не устанавливает `SUPPORTED` и не заменяет ML2 verification.
