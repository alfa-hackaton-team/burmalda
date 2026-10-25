import json
from pathlib import Path

ROOT = Path(__file__).parent
PROM = {"type": "prometheus", "uid": "prometheus"}
PAGES = [
    ("ingestion-overview", "Обзор", "Обзор бэкенда"),
    ("ingestion-sources", "Источники", "Источники и очереди"),
    ("ingestion-ml", "ML", "ML · обработка документов"),
    ("ingestion-postgres", "PostgreSQL", "PostgreSQL · соединения и диск"),
    ("ingestion-s3", "S3", "S3 · хранение и операции"),
    ("ingestion-http", "HTTP", "HTTP · API и внешние сервисы"),
]


def variable(name, label, query):
    return {"name": name, "label": label, "type": "query", "datasource": PROM,
            "query": query, "refresh": 1, "multi": True, "includeAll": True,
            "allValue": ".*", "current": {"selected": True, "text": "All", "value": "$__all"}}


class Dashboard:
    def __init__(self, uid, description, variables=None):
        self.uid = uid
        self.panels = []
        self.data = {
            "uid": uid, "title": next(title for key, _, title in PAGES if key == uid),
            "tags": ["ingestion", "backend"], "timezone": "browser", "schemaVersion": 39,
            "version": 1, "refresh": "30s", "time": {"from": "now-6h", "to": "now"},
            "links": [{"title": label, "type": "link", "url": f"/d/{key}", "keepTime": True,
                       "includeVars": True, "targetBlank": False} for key, label, _ in PAGES],
            "templating": {"list": variables or []}, "panels": self.panels,
        }
        self.text(description, 0, 0, 24, 4)

    def add(self, title, x, y, w, h, kind):
        panel = {"id": len(self.panels) + 1, "title": title, "type": kind,
                 "gridPos": {"x": x, "y": y + 1 if y else 0, "w": w, "h": h}}
        self.panels.append(panel)
        return panel

    def text(self, content, x, y, w, h):
        panel = self.add("", x, y, w, h, "text")
        panel["options"] = {"mode": "markdown", "content": content}

    def metric(self, title, expr, x, y, w=12, h=8, kind="timeseries", unit="short", legend="", description="", thresholds=None):
        p = self.add(title, x, y, w, h, kind)
        p.update({"description": description, "datasource": PROM,
                  "targets": [{"expr": expr, "legendFormat": legend, "refId": "A"}],
                  "fieldConfig": {"defaults": {"unit": unit, "color": {"mode": "palette-classic"}}, "overrides": []},
                  "options": {"legend": {"displayMode": "table", "placement": "bottom", "calcs": ["lastNotNull"]}, "tooltip": {"mode": "multi"}}})
        if kind in ("stat", "gauge", "bargauge"):
            p["targets"][0].update({"instant": True, "range": False})
            p["options"] = {"colorMode": "value", "graphMode": "none", "textMode": "auto", "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}}
        if kind == "bargauge":
            p["options"].update({"orientation": "horizontal", "displayMode": "gradient", "showUnfilled": True, "valueMode": "color"})
        if thresholds:
            p["fieldConfig"]["defaults"].update({"color": {"mode": "thresholds"}, "thresholds": {"mode": "absolute", "steps": [{"color": "green", "value": None}] + [{"color": color, "value": value} for value, color in thresholds]}})
        if unit == "percent":
            p["fieldConfig"]["defaults"].update({"min": 0, "max": 100})
        if kind == "gauge":
            p["options"] = {"showThresholdLabels": False, "showThresholdMarkers": True, "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}}
        return p

    def save(self):
        path = ROOT / "dashboards" / f"{self.uid.removeprefix('ingestion-') if self.uid != 'ingestion-overview' else 'ingestion'}.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(self.data, ensure_ascii=False, indent=2) + "\n")


def health(d, title, expr, x, y, w=6):
    p = d.metric(title, expr, x, y, w, 5, "stat", legend="{{storage}}")
    p["fieldConfig"]["defaults"].update({"color": {"mode": "thresholds"}, "thresholds": {"mode": "absolute", "steps": [{"color": "red", "value": None}, {"color": "green", "value": 1}]}})


def quantile(name, metric, grouping="", selector=""):
    return f'histogram_quantile({name}, sum by(le{"," + grouping if grouping else ""})(rate({metric}_bucket{selector}[$__rate_interval])))'


def volumes(d, storage, y):
    scope = f'{{storage="{storage}"}}'
    description = "Файловая система подключённого тома. В локальном Docker PG и S3 могут использовать один диск VM: значения совпадут, их нельзя суммировать."
    d.metric("Свободно для записи", "ingestion_storage_volume_available_bytes" + scope, 0, y, 6, 6, "stat", "bytes", description=description)
    d.metric("Ёмкость диска", "ingestion_storage_volume_size_bytes" + scope, 6, y, 6, 6, "stat", "bytes", description=description)
    fill = f"100 * (1 - ingestion_storage_volume_available_bytes{scope} / ingestion_storage_volume_size_bytes{scope})"
    d.metric("Заполнение диска", fill, 12, y, 6, 6, "gauge", "percent", description="Занятое и зарезервированное пространство. " + description, thresholds=[(80,"orange"),(90,"red")])
    health(d, "Сбор тома", "ingestion_storage_volume_collection_up" + scope, 18, y)
    d.metric("Свободное место", "ingestion_storage_volume_available_bytes" + scope, 0, y+6, unit="bytes", description=description)
    d.metric("Заполнение диска · история", fill, 12, y+6, unit="percent", description=description)


service = variable("service", "Процесс", "label_values(ingestion_postgres_pool_connections, job)")

overview = Dashboard("ingestion-overview", "**Состояние бэкенда.** Разделы сверху открывают детали источников, ML, PostgreSQL, S3 и HTTP.\n\nПустые графики означают отсутствие наблюдений: до подключения ML длительности обработки не появятся.")
overview.metric("Воркеры", 'sum(up{job="worker"})', 0,3,4,5,"stat")
overview.metric("Устарели", "ingestion_sources_stale",4,3,4,5,"stat",thresholds=[(1,"red")])
overview.metric("DEAD", 'ingestion_article_fetch_jobs{status="DEAD"}',8,3,4,5,"stat",thresholds=[(1,"red")])
overview.metric("ML FAIL", "sum(ingestion_ml_failures_last_hour) or on() (0 * (ingestion_pipeline_collection_up == 1))",12,3,4,5,"stat", description="Только завершённые FAILED; не количество попыток POST/GET.",thresholds=[(1,"orange")])
health(overview,"Сбор БД","ingestion_observer_db_up",16,3,4)
health(overview,"Сбор ML","ingestion_pipeline_collection_up",20,3,4)
overview.metric("Очереди · поступило и завершено / мин", "sum by(queue,event)(ingestion_queue_jobs_last_5m) / 5",0,8,legend="{{queue}} · {{event}}",description="Средняя скорость за пять минут по сохранённым заданиям. Повторы HTTP не увеличивают число документов.")
overview.metric("Очереди · ожидают", 'sum(ingestion_article_fetch_jobs{status=~"QUEUED|RETRY"})',12,8,legend="Статьи")['targets'].append({"expr": 'sum(ingestion_processing_jobs{status=~"QUEUED|RETRY"})', "legendFormat": "ML", "refId": "B"})
overview.metric("Документ → поиск · p95", 'ingestion_document_processing_seconds{stage="ingest_to_search",quantile="0.95"}',0,16,unit="s",legend="Загрузка → поиск",description="Успешные операции за последний час: completed_at минус fetched_at. Включает ожидание включения ML и переобработку.")
overview.metric("Отставание очередей", "ingestion_article_job_due_age_max_seconds",12,16,unit="s",legend="Статьи")['targets'].append({"expr":"ingestion_processing_job_due_age_max_seconds","legendFormat":"ML","refId":"B"})
overview.metric("Пул PG · занято", '100 * sum by(job)(ingestion_postgres_pool_connections{state="acquired"}) / sum by(job)(ingestion_postgres_pool_connections{state="max"})',0,24,unit="percent",legend="{{job}}")
overview.metric("Диски PG / S3", "100 * (1 - ingestion_storage_volume_available_bytes / ingestion_storage_volume_size_bytes)",12,24,unit="percent",legend="{{storage}}",description="Общий диск Docker может давать одинаковые значения; это не размер данных конкретного приложения.")
overview.save()

sources = Dashboard("ingestion-sources", "**От источника до сохранённой версии.** Состояния, опросы, повторы отдельных статей и отставание очереди. Размер очереди берётся из БД и сохраняется после перезапуска воркера.")
sources.metric("Устарели", "ingestion_sources_stale",0,3,6,5,"stat",thresholds=[(1,"red")])
sources.metric("Статей DEAD", 'ingestion_article_fetch_jobs{status="DEAD"}',6,3,6,5,"stat",thresholds=[(1,"red")])
sources.metric("Возраст очереди", "ingestion_article_job_due_age_max_seconds",12,3,6,5,"stat","s",thresholds=[(900,"red")])
sources.metric("Просрочено источников", "ingestion_sources_overdue",18,3,6,5,"stat",thresholds=[(1,"orange")])
sources.metric("Опросы по результату · в минуту", "sum by(status)(rate(ingestion_source_polls_total[$__rate_interval])) * 60",0,8,legend="{{status}}")
sources.metric("Длительность опроса · p95",quantile(.95,"ingestion_source_poll_duration_seconds","status"),12,8,unit="s",legend="{{status}}")
sources.metric("Очередь статей · поток / мин",'ingestion_queue_jobs_last_5m{queue="article"} / 5',0,16,legend="{{event}}")
sources.metric("Задания загрузки статей", "ingestion_article_fetch_jobs",12,16,legend="{{status}}")
sources.metric("Новые материалы · в час", "sum by(kind)(rate(ingestion_source_poll_entries_total[$__rate_interval])) * 3600",0,24,legend="{{kind}}")
sources.metric("Состояние источников", "ingestion_sources",12,24,legend="{{health}}")
sources.metric("Источник без успеха · максимум", "ingestion_source_last_success_age_max_seconds",0,32,unit="s")
sources.metric("Загрузка статьи · p95",quantile(.95,"ingestion_article_job_duration_seconds","status"),12,32,unit="s",legend="{{status}}")
sources.metric("Восстановлено lease / час", "sum(rate(ingestion_source_leases_recovered_total[$__rate_interval])) * 3600",0,40)
sources.metric("Удалено опросов / сутки", "sum(increase(ingestion_retention_source_polls_deleted_total[24h]))",12,40)
sources.metric("Просрочка опроса · максимум", "ingestion_source_poll_due_age_max_seconds",0,48,12,5,"stat","s")
sources.metric("Возраст последней очистки", "time() - ingestion_retention_last_success_timestamp_seconds",12,48,12,5,"stat","s",thresholds=[(172800,"red")])
p=sources.add("Источники",0,53,24,9,"table")
p.update({"datasource":{"type":"postgres","uid":"postgres"},"targets":[{"refId":"A","format":"table","rawSql":"SELECT name AS \"Источник\",health AS \"Состояние\",round(extract(epoch from now()-coalesce(last_success_at,created_at)))::bigint AS \"Секунд без успеха\",coalesce(last_error,'') AS \"Ошибка\" FROM sources WHERE enabled ORDER BY CASE WHEN health='HEALTHY' THEN 1 ELSE 0 END,last_success_at NULLS FIRST LIMIT 50"}],"options":{"showHeader":True}})
p=sources.add("Логи воркера",0,62,24,9,"logs")
p.update({"datasource":{"type":"loki","uid":"loki"},"targets":[{"refId":"A","expr":'{service_name="ingestion-worker"}',"queryType":"range"}],"options":{"showTime":True,"wrapLogMessage":True,"enableLogDetails":True}})
sources.save()

ml=Dashboard("ingestion-ml", "**Документы и ML.** Скорость — по уникальным заданиям в БД за 5 минут; p50/p95/p99 — по завершённым операциям за час.\n\n`ingest_to_search`: загрузка → подтверждённый SUCCEEDED; `queue_to_terminal`: постановка задания → SUCCEEDED/FAILED. Без завершённых операций квантилей нет.")
ml.metric("В очереди", 'sum(ingestion_processing_jobs{status=~"QUEUED|RETRY"})',0,3,6,5,"stat")
ml.metric("Отставание", "ingestion_processing_job_due_age_max_seconds",6,3,6,5,"stat","s",thresholds=[(900,"red")])
ml.metric("Отказы за час", "sum(ingestion_ml_failures_last_hour) or on() (0 * (ingestion_pipeline_collection_up == 1))",12,3,6,5,"stat",thresholds=[(1,"orange")])
health(ml,"Сбор метрик","ingestion_pipeline_collection_up",18,3)
ml.metric("Загрузка → поиск", 'ingestion_document_processing_seconds{stage="ingest_to_search"}',0,8,unit="s",legend="q={{quantile}}",description="Успешные операции за час. Время включает ожидание постановки и переобработку старых материалов.")
ml.metric("Задание → завершение", 'ingestion_document_processing_seconds{stage="queue_to_terminal"}',12,8,unit="s",legend="{{status}} · q={{quantile}}")
ml.metric("Поступило и завершено / мин", 'ingestion_queue_jobs_last_5m{queue="ml"} / 5',0,16,legend="{{event}}")
ml.metric("Задания ML", "ingestion_processing_jobs",12,16,legend="{{status}}")
ml.metric("Причины отказов · за час", "ingestion_ml_failures_last_hour",0,24,kind="bargauge",legend="{{code}}",description="Сохранённый last_error_code финальных FAILED. Неизвестные коды объединяются в OTHER, отсутствующие — UNKNOWN. Quality-отказ показывается под кодом, который вернул ML; текст ошибки остаётся в логах.")
ml.metric("Выборка для длительности", "ingestion_document_processing_samples_last_hour",12,24,legend="{{stage}} · {{status}}")
ml.metric("Попытки POST/GET · в минуту", "sum by(status)(rate(ingestion_ml_jobs_total[$__rate_interval])) * 60",0,32,legend="{{status}}",description="Попытки воркера, включая polling; не скорость обработки документов.")
ml.metric("Длительность попытки · p95",quantile(.95,"ingestion_ml_job_duration_seconds","status"),12,32,unit="s",legend="{{status}}")
ml.save()

pg=Dashboard("ingestion-postgres", "**PostgreSQL.** Пулы принадлежат отдельным процессам: выберите процесс в фильтре. Отмена получения соединения включает таймауты.\n\nРазмер БД и ёмкость файловой системы — разные показатели. Диск измеряется через том, подключённый observer только для чтения.", [service])
pg.metric("Размер БД", 'ingestion_storage_used_bytes{storage="postgres"}',0,3,6,5,"stat","bytes")
pg.metric("Пул · занято", '100 * sum(ingestion_postgres_pool_connections{state="acquired",job=~"$service"}) / sum(ingestion_postgres_pool_connections{state="max",job=~"$service"})',6,3,6,5,"gauge","percent",thresholds=[(80,"orange"),(95,"red")])
pg.metric("Пул · лимит", 'sum(ingestion_postgres_pool_connections{state="max",job=~"$service"})',12,3,6,5,"stat")
health(pg,"Сборщик БД","ingestion_observer_db_up",18,3)
pg.metric("Соединения", 'sum by(job,state)(ingestion_postgres_pool_connections{job=~"$service",state=~"acquired|idle|constructing"})',0,8,legend="{{job}} · {{state}}")
pg.metric("Занято от лимита", '100 * sum by(job)(ingestion_postgres_pool_connections{state="acquired",job=~"$service"}) / sum by(job)(ingestion_postgres_pool_connections{state="max",job=~"$service"})',12,8,unit="percent",legend="{{job}}")
pg.metric("Получение соединения · среднее", 'sum by(job)(rate(ingestion_postgres_pool_acquire_seconds_total{job=~"$service"}[$__rate_interval])) / clamp_min(sum by(job)(rate(ingestion_postgres_pool_acquires_total{job=~"$service"}[$__rate_interval])), 0.000000001)',0,16,unit="s",legend="{{job}}",description="Суммарное время Acquire / число успешных Acquire. Не является длительностью SQL-запроса.")
pg.metric("Ожидания и отмены / мин", 'sum by(job)(rate(ingestion_postgres_pool_empty_acquires_total{job=~"$service"}[$__rate_interval])) * 60',12,16,legend="{{job}} · ожидание")['targets'].append({"expr": 'sum by(job)(rate(ingestion_postgres_pool_canceled_acquires_total{job=~"$service"}[$__rate_interval])) * 60',"legendFormat":"{{job}} · отмена","refId":"B"})
volumes(pg,"postgres",24)
pg.metric("Размер БД · история", 'ingestion_storage_used_bytes{storage="postgres"}',0,38,unit="bytes")
pg.metric("Очистка истории · ошибки / сутки", "sum(increase(ingestion_retention_errors_total[24h]))",12,38)
pg.metric("Изменение БД за сутки", 'ingestion_storage_used_bytes{storage="postgres"} - ingestion_storage_used_bytes{storage="postgres"} offset 24h',0,46,12,5,"stat","bytes",description="Появится после накопления суток истории.")
pg.metric("Возраст последней очистки", "time() - ingestion_retention_last_success_timestamp_seconds",12,46,12,5,"stat","s",thresholds=[(172800,"red")])
pg.save()

s3=Dashboard("ingestion-s3", "**S3.** Размер и количество текущих объектов всего bucket; операции SDK измеряются отдельно по процессам.\n\nGET включает чтение тела до Close; PUT-байты учитываются при успешной загрузке. Ошибки и повторы SDK входят в длительность одной операции. Нулевая активность не означает отсутствие файлов.", [service, variable("operation","Операция","label_values(ingestion_s3_operations_total, operation)")])
s3.metric("Объём объектов", 'ingestion_storage_used_bytes{storage="s3"}',0,3,6,5,"stat","bytes")
s3.metric("Объекты", 'ingestion_storage_objects{storage="s3"}',6,3,6,5,"stat")
s3.metric("Нет оригинала", "ingestion_raw_missing_objects",12,3,6,5,"stat",thresholds=[(1,"red")])
health(s3,"Сбор bucket",'ingestion_storage_collection_up{storage="s3"}',18,3)
scope='{job=~"$service",operation=~"$operation"}'
s3.metric("Операции / секунду", f"sum by(operation,outcome)(rate(ingestion_s3_operations_total{scope}[$__rate_interval]))",0,8,legend="{{operation}} · {{outcome}}")
s3.metric("Длительность операции · p95",quantile(.95,"ingestion_s3_operation_duration_seconds","operation",scope),12,8,unit="s",legend="{{operation}}")
s3.metric("Передача данных", 'sum by(direction)(rate(ingestion_s3_transferred_bytes_total{job=~"$service"}[$__rate_interval]))',0,16,unit="Bps",legend="{{direction}}",description="Байты полезной нагрузки; частичные неудачные PUT не известны и не учитываются, GET считает фактически прочитанные байты.")
s3.metric("Ошибки операций / мин", 'sum by(operation)(rate(ingestion_s3_operations_total{outcome="error",job=~"$service",operation=~"$operation"}[$__rate_interval])) * 60 or on(operation) (0 * sum by(operation)(rate(ingestion_s3_operations_total{job=~"$service",operation=~"$operation"}[$__rate_interval])))',12,16,legend="{{operation}}")
volumes(s3,"s3",24)
s3.metric("Объём и количество объектов", 'ingestion_storage_used_bytes{storage="s3"}',0,38,unit="bytes")
s3.metric("Объекты без ссылок", "ingestion_raw_orphan_objects",12,38,description="После grace-периода; автоматического удаления нет.")
s3.metric("Возраст инвентаризации", 'time() - ingestion_storage_last_success_timestamp_seconds{storage="s3"}',0,46,8,5,"stat","s")
s3.metric("Изменение за сутки", 'ingestion_storage_used_bytes{storage="s3"} - ingestion_storage_used_bytes{storage="s3"} offset 24h',8,46,8,5,"stat","bytes",description="Появится после накопления суток истории.")
s3.metric("Ошибки сверки / сутки", "sum(increase(ingestion_raw_reconciliation_errors_total[24h]))",16,46,8,5,"stat",thresholds=[(1,"red")])
s3.save()

http=Dashboard("ingestion-http", "**HTTP.** API измеряется на входе, включая авторизацию и валидацию. Внешние вызовы — отдельно: feed, article и ml.\n\nМетки содержат только шаблоны реализованных маршрутов; неизвестные пути объединяются в unmatched. UUID, URL, токены и тексты ошибок в метрики не попадают.")
http.metric("API · запросов / сек", 'sum(rate(ingestion_http_requests_total[$__rate_interval]))',0,3,6,5,"stat")
http.metric("API · 5xx / мин", 'sum(rate(ingestion_http_requests_total{code=~"5.."}[$__rate_interval])) * 60 or on() (0 * sum(rate(ingestion_http_requests_total[$__rate_interval])))',6,3,6,5,"stat",thresholds=[(.01,"red")])
http.metric("API · p95",quantile(.95,"ingestion_http_request_duration_seconds"),12,3,6,5,"stat","s")
health(http,"API доступен",'min(up{job="api"})',18,3)
http.metric("API · маршруты", 'sum by(route,method)(rate(ingestion_http_requests_total[$__rate_interval]))',0,8,legend="{{method}} {{route}}")
http.metric("API · ответы", 'sum by(code)(rate(ingestion_http_requests_total[$__rate_interval]))',12,8,legend="{{code}}")
http.metric("API · p95 по маршрутам",quantile(.95,"ingestion_http_request_duration_seconds","route"),0,16,unit="s",legend="{{route}}")
http.metric("Внешние вызовы · p95",quantile(.95,"ingestion_upstream_http_duration_seconds","upstream"),12,16,unit="s",legend="{{upstream}}")
http.metric("Внешние HTTP-ответы / мин", "sum by(upstream,status)(rate(ingestion_upstream_http_responses_total[$__rate_interval])) * 60",0,24,legend="{{upstream}} · {{status}}")
http.metric("Сеть / чтение · ошибки / мин", "sum by(upstream)(rate(ingestion_upstream_http_errors_total[$__rate_interval])) * 60",12,24,legend="{{upstream}}")
http.metric("429 и 503 · за 30 минут", 'sum by(upstream,status)(increase(ingestion_upstream_http_responses_total{status=~"429|503"}[30m]))',0,32,legend="{{upstream}} · {{status}}")
http.metric("Внешние вызовы · результат", "sum by(upstream,outcome)(rate(ingestion_upstream_http_duration_seconds_count[$__rate_interval]))",12,32,legend="{{upstream}} · {{outcome}}")
http.save()
