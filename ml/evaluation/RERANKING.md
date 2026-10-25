# Эксперимент: vector Top-20 → reranker → Top-5/10

Только evaluation: рабочий `PostgresRetriever`, DTO, embedding/chunking profiles и DDL не менялись. Корпус прежний: **40 synthetic документов/чанков, 24 запроса, 29 positives**. Dataset SHA-256: `7d776a8d28b3fdb66e7b30c4eab87957c0c089be40ef48c6aa361e08fd261cde`. Qrels, тексты и instruction после результатов не менялись.

## Конфигурация и запуск

- Embedding: прежний Qwen3-Embedding-0.6B, pinned profile, настоящий PostgreSQL/pgvector search с K=20.
- Reranker: `Qwen/Qwen3-Reranker-0.6B`, revision `e61197ed45024b0ed8a2d74b80b4d909f1255473`, experimental profile `ml1-experiment-qwen3-reranker-06b-v1`.
- CPU float32, eager, batch=4, 4 torch threads в измеренном прогоне; max input=8192, без truncation. Официальный фиксированный query/document prompt; ranking score=`logit(yes)-logit(no)`. [Источник формата модели](https://huggingface.co/Qwen/Qwen3-Reranker-0.6B).
- Модель видит только query + точный chunk_text. Relevance labels, title и vector score в prompt не передаются. Сортировка по reranker score, при равенстве — исходный vector rank и UUID. Scores не смешиваются с cosine и не являются калиброванной уверенностью.

```sh
# Однократная явная загрузка вне Git, в стандартный HF cache:
python -c 'from huggingface_hub import snapshot_download; snapshot_download("Qwen/Qwen3-Reranker-0.6B", revision="e61197ed45024b0ed8a2d74b80b4d909f1255473", allow_patterns=["config.json", "generation_config.json", "tokenizer.json", "model.safetensors"])'

ML1_EVAL_POSTGRES_DSN='<disposable test DSN>' HF_HUB_OFFLINE=1 \
  python -m ml.evaluation.reranking --output .local/retrieval-evaluation/reranker.json
```

Зависимости прежние. Embedding и reranker загружаются последовательно; веса offline, вне репозитория. Fixture-схема создаётся/удаляется прежним evaluation runner. Перед reranking проверяются dataset/profile/source fingerprints и совпадение всех vector Top-20 с сохранённым diagnostic baseline. В выполненном прогоне UUID, порядок и текст совпали, max cosine difference=0. Ошибка inference или несовпадение ID/NaN/Inf останавливает эксперимент без silent fallback.

## Результаты

[Все запросы, позиции и scores](reports/reranker_v1.md), [полный JSON, configuration/timing/fingerprints](reports/reranker_v1.json).

| Метрика | Vector-only | После reranker | Изменение |
|---|---:|---:|---:|
| Macro Recall@5 | 0.916667 | **1.000000** | +0.083333 |
| Macro Recall@10 | 0.916667 | **1.000000** | +0.083333 |
| MRR@10 | 0.784722 | **1.000000** | +0.215278 |
| Binary nDCG@10 | 0.814661 | **0.996655** | +0.181994 |

Все 29 positives попали в финальный Top-5. Первый результат релевантен у 24/24 запросов. Recall@5 вырос у 3 запросов, MRR/nDCG@10 — у 8; у остальных соответствующие метрики прежние, регрессий по ним нет. Перестановка двух одинаково релевантных чанков не считается улучшением/ухудшением бинарной разметки.

| Query | Positive | Vector rank → reranker rank |
|---|---|---:|
| q13, два способа экономии | energy_solar / equipment_duty | 15→3 / 4→1 |
| q21, English: прибыль вверх, выручка вниз | retail_profit | 11→1 |
| q24, сокращение из-за спроса | factory_cancel / labor_layoffs | 15→2 / 4→1 |
| q10, НДС при УСН | vat | 2→1 |
| q16, атака остановила заказы | it_cyber | 2→1 |
| q17, нехватка сварщиков | labor_shortage | 2→1 |
| q19, ожидание оплаты увеличилось | procurement | 2→1 |
| q20, продажи вверх, прибыль вниз | retail_revenue | 3→1 |

Список размеченных hard negatives выше **первого** positive сократился с 6 запросов до 0. Это не означает, что все нерелевантные документы ушли из Top-5 или встали ниже каждого positive.

Отдельная описательная абляция: отфильтровали сохранённые reranker scores до кандидатов с `vector_rank<=10` и пересчитали метрики (без нового inference/подбора конфигурации). Получено Recall@5/10=0.916667, MRR@10=0.958333, nDCG@10=0.926096. Значит, улучшение состоит из **двух частей**: 20 кандидатов возвращают пропущенные positives, reranker улучшает порядок. Один reranker поверх прежнего Top-10 полноту не восстановит. Это использование pointwise scores из batch=4; отдельного latency benchmark режима Top-10 не проводилось.

## Остаточная ошибка и риски интерпретации

В q13 между двумя positives остаётся противоположная по смыслу новость:

1. `equipment_duty`, снижение пошлины на комплектующие: score **9.229431**, positive.
2. `fx_ruble`, **рост** затрат на импорт из-за курса: score **2.243979**, negative.
3. `energy_solar`, собственная генерация: score **−0.296418**, positive.

Запрос спрашивает о снижении расходов через собственную генерацию и комплектующие. Ранжирование всё ещё предпочитает общее сходство по импортным затратам точному направлению изменения и второму аспекту. Гипотеза для следующего эксперимента — отдельно оценивать аспекты/требования запроса; этот прогон не доказывает причину внутри модели.

Другие hard negatives остаются высоко: q19 обратное изменение 60→30 дней на втором месте со score 8.768806; q16 плановое обновление без сбоя — на втором со score 6.718558. Поэтому не вводим отсечение `score>0`/`sigmoid>0.5`: оно оставило бы некоторые ложные совпадения и удалило релевантную солнечную генерацию. Reranking не заменяет проверку утверждений и evidence в ML2. MRR=1 тоже не гарантирует полноту многосоставного ответа.

## Цена по времени

В одном последовательном локальном CPU-прогоне:

- vector query embedding + pgvector search: median **0.110 с**, max **0.124 с**;
- дополнительный rerank 20 пар: median **4.200 с**, max **4.610 с**;
- загрузка reranker: отдельно **1.748 с**, не включена в query latency;
- суммарное reranking-время 24 запросов: **100.549 с**.

Это не SLA и не нагрузочный benchmark; первый запрос может включать warmup. Качество заметно выросло, но стоимость CPU велика относительно vector-only. Не измерялись concurrency, p95 на серии независимых запусков, GPU/квантизация, длинные документы. Результат не разрешает автоматически включать reranker для всех production-запросов.

## Что исправлять дальше

1. **Надёжность оценки:** независимая проверка разметки и новый holdout с реальными длинными текстами, отрицаниями, числами, multi-aspect и no-answer запросами. Набор v1 остаётся неизменным regression set; 100% на нём не означает 100% в работе.
2. **q13:** отдельный контролируемый эксперимент с multi-aspect scoring/coverage, без ручных правил для ID q13 и без изменения gold labels. Сравнить прирост nDCG и регрессии на holdout; не менять prompt по одному удачному примеру.
3. **Latency:** измерить batch sizes, оптимизированный inference/квантизацию или ускоритель как отдельные профили с повторной оценкой качества и score stability. Candidate K уменьшать только после проверки coverage на независимых данных; K=15, достаточный здесь, не универсальная настройка.
4. **Интеграция:** если качество/latency подтвердятся, отдельно согласовать experimental profile → production adapter. Не менять vector-only baseline и не смешивать cosine с reranker score.

Сейчас нет оснований менять embedding/chunking, вводить перевод English-запросов или добавлять hybrid. Кандидат q21 уже присутствует на месте 11 и корректно поднят reranker. В этом этапе не реализованы новые исправления, production reranking, HTTP, Docker, миграции или thresholds.
