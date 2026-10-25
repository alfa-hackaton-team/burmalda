# Метрики и дашборды

В корне репозитория `docker compose up -d --build` запускает сборщики и Grafana. Открыть [Обзор](http://127.0.0.1:13000/d/ingestion-overview). Верхняя навигация переключает разделы и сохраняет выбранный диапазон времени.

| Раздел | Содержимое |
|---|---|
| Обзор | Состояние процессов, очереди, отказы, полное время документа, занятость пулов и дисков |
| Источники | Опросы, новые версии, повторы статей, DEAD, отставание, таблица источников и логи |
| ML | Длительности документов, поток уникальных заданий, статусы и причины финальных отказов |
| PostgreSQL | Пул каждого процесса, ожидания, отмены, объём БД и диск |
| S3 | Размер bucket, объекты, длительности и ошибки SDK, переданные байты, сверка и диск |
| HTTP | Входящие API-запросы, коды и задержки по маршрутам; исходящие feed/article/ml |

## Время документа и очереди

Observer вычисляет показатели из постоянной БД. Миграция `000022_observability_queue_indexes.sql` добавляет индексы для выборок по времени.

- `ingestion_queue_jobs_last_5m{queue,event}` — число новых строк заданий и финальных завершений за последние пять минут. `queue`: `article` или `ml`; `event`: `arrived`, `succeeded`, `failed`. На графиках делится на 5 для скорости в минуту. Это **gauge**, к нему не применяется `rate`/`increase`.
- Повтор POST/GET не создаёт новое задание и не увеличивает входной поток. Повторное использование существующей строки задания статьи также не считается новой строкой очереди. Новая версия пайплайна создаёт отдельное ML-задание.
- `ingestion_document_processing_seconds{stage,status,quantile}` — p50/p95/p99 по операциям, завершённым **за последний час**. `queue_to_terminal` измеряет `completed_at - processing_jobs.created_at`, включая ожидания и повторы, отдельно для SUCCEEDED/FAILED. `ingest_to_search` измеряет `completed_at - article_versions.fetched_at` только для SUCCEEDED. Здесь также учитывается время до включения ML и отправка старого корпуса. Измерение заканчивается, когда Go сохранил подтверждённый результат ML; включает задержку polling.
- `ingestion_document_processing_samples_last_hour` — размер выборки каждого stage/status. Без завершений нет квантилей; нулевые длительности не подставляются.
- `ingestion_ml_failures_last_hour{code}` — финальные FAILED за час, по сохранённому `last_error_code`. Это gauge; временные RETRY не являются финальными отказами. Известные коды перечислены в Go, неизвестные сводятся к OTHER, отсутствующие — UNKNOWN. Quality-отказ не определяется по тексту: если ML возвращает общий VALIDATION_ERROR, он остаётся VALIDATION_ERROR.
- `ingestion_pipeline_collection_up` — успех выборок. При ошибке данные не публикуются как нули.

Эти метрики не заменяют счётчики попыток `ingestion_article_jobs_total` / `ingestion_ml_jobs_total`: polling может давать много попыток на один документ. Переобработка каждого пайплайна считается отдельной операцией.

## Пулы PostgreSQL

Каждый процесс экспортирует собственный `pgxpool.Stat`. Prometheus добавляет `job` и `instance`; PostgreSQL/S3-дашборды позволяют выбрать процесс.

- `ingestion_postgres_pool_connections{state}`: acquired, idle, constructing, total, max. total/max нельзя складывать с остальными состояниями.
- `ingestion_postgres_pool_acquires_total` — успешные Acquire.
- `ingestion_postgres_pool_acquire_seconds_total` — время успешных Acquire; отношение rate времени к rate количества даёт среднее время получения соединения.
- `ingestion_postgres_pool_empty_acquires_total` и `ingestion_postgres_pool_empty_wait_seconds_total` — успешные получения с ожиданием свободного соединения и затраченное время.
- `ingestion_postgres_pool_canceled_acquires_total` — отменённые/истёкшие по таймауту Acquire; не только таймауты.

Это метрики пула приложения, не длительности SQL и не общее число всех подключений PostgreSQL.

## S3 и HTTP

- `ingestion_s3_operations_total{operation,outcome}` и `ingestion_s3_operation_duration_seconds` измеряют put/get/head/list/head_bucket/create_bucket. outcome: success/error. Попытки внутри SDK объединяются в одну операцию; GET длится до закрытия тела ответа, ошибки чтения учитываются как error.
- `ingestion_s3_transferred_bytes_total{direction}`: upload — размер успешного PUT, download — фактически прочитанные байты GET, включая данные перед ошибкой чтения. Неизвестные частичные неуспешные PUT и протокольные накладные расходы не учитываются.
- `ingestion_http_requests_total{route,method,code}` и `ingestion_http_request_duration_seconds{route,method}` измеряют API вместе с авторизацией и валидацией. Маршруты ограничены реализованными шаблонами в `observability/http.go`; при добавлении API-маршрутов нужно обновить этот список. Неизвестные URL объединяются в unmatched. UUID, токены, параметры запроса и адреса издателей в метки не попадают.
- `ingestion_upstream_http_duration_seconds{upstream,outcome}` — исходящие feed/article/ml, включая чтение ответа. `ingestion_upstream_http_errors_total` — сетевые ошибки, ошибки чтения и превышения размера; HTTP 4xx/5xx видны отдельно в `ingestion_upstream_http_responses_total`.

API отдаёт метрики на отдельном внутреннем `METRICS_ADDR=:19095`; этот порт не публикуется наружу корневым Compose. У других процессов: worker 19090, observer 19091, retention 19093, rawstorage 19094. Для API, запущенного вручную, `METRICS_ADDR` включает сервер метрик; пустое значение отключает его.

Подробности размеров хранилищ и дисков — в [README бэкенда](../README.md#объём-postgresql-и-s3). Пустые графики до первого запроса/задания являются отсутствием наблюдений, не доказательством здоровья. Для rate и p95 гистограмм нужны как минимум два scrape; для SQL-квантилей достаточно одной завершённой операции.

## Обновление

Из корня репозитория:

```sh
python3 backend/observability/grafana/generate_dashboard.py
python3 backend/observability/grafana/generate_alerts.py
docker compose up -d --build
docker compose restart prometheus grafana
```

Дашборды загружаются из JSON в папку Ingestion. Ссылки существующих alert rules пересоздаются по UID дашборда и названию панели. Перезапуск Prometheus нужен после изменения scrape-конфига, Grafana — после изменения provisioning правил. Доставка уведомлений настраивается отдельно. История метрик в локальном Compose хранится 15 дней.
