# Retrieval evaluation v1

Фиксированный **синтетический**, вручную размеченный набор бизнес-новостей: 40 документов / 40 чанков, 24 запроса, 29 положительных query–chunk пар. Два запроса на английском, документы на русском. Все версии fixture `1`, ready=true, trusted allowlist включает весь корпус; metadata filters выключены.

[Dataset](data/business_news_v1.json) содержит точные положительные `chunk_id`, hard negatives и обоснования. Релевантность — прямое подтверждение факта с нужными сущностью, направлением, причиной и статусом; общая тема недостаточна. Разметка и тексты зафиксированы **до первого запуска модели**, после просмотра результатов не менялись. Все события вымышлены, не актуальные новости. Ручная разметка пока без независимой проверки.

## Перезапуск

Зависимости: существующие `ml/requirements-embeddings.txt` и `ml/requirements-persistence.txt`. Нужны закреплённые Qwen weights/tokenizer в HF cache и disposable PostgreSQL с установленным `vector`. Docker не требуется.

```sh
ML1_EVAL_POSTGRES_DSN='<disposable test DSN>' HF_HUB_OFFLINE=1 \
  python -m ml.evaluation.retrieval --output .local/retrieval-evaluation/run.json
```

Runner создаёт уникальную временную схему, применяет **существующий** DDL, строит чанки настоящим chunker v1, сверяет UUID/hash с manifest, кодирует Qwen, сохраняет через действующий persistence и вызывает `PostgresRetriever.search` для каждого query. Собственная схема удаляется также при ошибке. Production `ml`, Go-таблицы и миграционные файлы не меняются. DSN не записывается в отчёт.

JSON и соседний Markdown содержат expected UUID, Top-10 со scores, пропущенные positives и hard negatives. Запрос выполняется один раз с K=10, первые пять результатов дают @5. **Macro Recall@K** — среднее по запросам доли найденных релевантных чанков; знаменатель — все positives запроса. Ошибка/partial останавливает evaluation, а не превращается в нулевое качество. Порогов качества или similarity нет.

В JSON фиксируются dataset SHA-256, immutable profiles/revision, batch size, окружение и SHA-256 реализации. При изменении chunking несовпадение manifest — явная ошибка: опубликованный dataset v1 нельзя молча переразмечать. Новый корпус/разметка требуют новой версии и явной фиксации UUID. Нельзя выбирать только удачные запросы после просмотра scores.

## Первый baseline

[По каждому запросу](reports/vector_v1.md), [полный машинный отчёт](reports/vector_v1.json).

- Macro Recall@5 = **0.916667**, Macro Recall@10 = **0.916667**; все positives найдены у 21 из 24 запросов.
- Два независимых прогона с повторным построением/сохранением векторов: одинаковые Top-10 и метрики, max score difference = 0.
- Реальные Qwen CPU float32, PostgreSQL 18.4, pgvector 0.8.5. Остальные версии и fingerprints — в JSON.

| Query | Проблема |
|---|---|
| q13 | Найдена льгота на комплектующие (rank 4), пропущена собственная солнечная генерация. Составной запрос теряет один смысловой аспект; выше стоят новости о росте расходов. Recall@5/10=0.5. |
| q21 | Английский запрос о росте прибыли при падении выручки не нашёл `retail_profit` в Top-10. Это один наблюдаемый cross-language failure, а не доказательство общей непригодности multilingual retrieval. Recall@5/10=0. |
| q24 | Сокращение штата из-за спроса найдено (rank 4), отмена расширения пропущена. Выше новости с другой причиной: дорогие кредиты и дефицит кадров. Recall@5/10=0.5. |

У q10/q16/q17/q19/q20/q24 размеченный hard negative выше первого positive. Примеры: другой налог вместо НДС при УСН; отсутствие сбоя вместо атаки; оплата 60→30 дней вместо 30→60. Recall@5 этого не показывает: метрика не измеряет качество первых мест. Увеличение K с 5 до 10 в этом прогоне не помогло.

## Диагностика полного ранжирования

Добавлены MRR@K (первый positive) и binary nDCG@K (все positives с discount `1/log2(rank+1)`), macro averaging. [Полная выдача и позиции](reports/vector_v1_diagnostics.md), [JSON](reports/vector_v1_diagnostics.json).

```sh
ML1_EVAL_POSTGRES_DSN='<disposable test DSN>' HF_HUB_OFFLINE=1 \
  python -m ml.evaluation.retrieval --full-ranking --output .local/retrieval-evaluation/diagnostics.json
```

Диагностический запрос получает все 40 fixture-чанков через тот же Retriever; default baseline остаётся K=10. Режим ограничен 50 чанками по существующему контракту, полнота корпуса проверяется явно. Dataset, профили и реализация retrieval совпадают с исходным baseline; проверка всех Top-10 префиксов дала те же UUID и max score difference=0. Исходные baseline-файлы не перезаписывались.

| K | Macro Recall | MRR | Binary nDCG |
|---|---:|---:|---:|
| 5 / 10 | 0.916667 | 0.784722 | 0.814661 |
| 20 / 40 | 1.000000 | 0.788510 | 0.839057 |

| Query | Пропущенный в Top-10 чанк | Rank | Score | Отставание от score на 10-м месте |
|---|---|---:|---:|---:|
| q13 | energy_solar | 15 | 0.475268 | 0.037473 |
| q21 | retail_profit | 11 | 0.457985 | 0.005434 |
| q24 | factory_cancel | 15 | 0.512502 | 0.015227 |

Все positives присутствуют в первых 15 позициях. Recall@40=1 при возврате всего корпуса закономерен, а Recall@20 показывает, что текущие misses можно включить в кандидаты без изменения модели/запросов. Повышение candidate K само по себе **не улучшает финальный Top-5/10**. Диагностика обосновывает отдельный reranker-эксперимент, а не автоматическую production-настройку на основании 40 документов. Рабочий vector-only Retriever reranker не включает.

Отдельный evaluation-only [эксперимент Qwen reranker 20→5/10](RERANKING.md) выполнен: Recall@5/10=1, MRR@10=1, nDCG@10=0.996655; CPU rerank median≈4.2с. Остаточная ошибка q13, абляция, latency и дальнейшие решения разобраны отдельно; рабочий vector-only retrieval остаётся прежним.

Ограничения: маленький искусственный корпус, один короткий чанк на документ, нет проверки длинных документов/границ чанков, production ACL/latest/readiness, свежести корпуса и no-answer/abstention. Это диагностическая точка отсчёта, не production SLA и не gold dataset реальных новостей.
