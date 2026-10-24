import json
from pathlib import Path


PROM = {"type": "prometheus", "uid": "prometheus"}
LOKI = {"type": "loki", "uid": "loki"}
POSTGRES = {"type": "postgres", "uid": "postgres"}
panels = []


def metric(title, expression, x, y, w, h, kind="timeseries", unit="short", description="", legend="{{status}}", thresholds=None):
    panel = {
        "id": len(panels) + 1,
        "title": title,
        "description": description,
        "type": kind,
        "datasource": PROM,
        "gridPos": {"x": x, "y": y, "w": w, "h": h},
        "targets": [{"expr": expression, "legendFormat": legend, "refId": "A"}],
        "fieldConfig": {
            "defaults": {"unit": unit, "color": {"mode": "palette-classic"}},
            "overrides": [],
        },
        "options": {"legend": {"displayMode": "list", "placement": "bottom"}, "tooltip": {"mode": "multi"}},
    }
    if kind == "stat":
        panel["options"] = {"colorMode": "value", "graphMode": "area", "justifyMode": "auto", "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}}
    if thresholds:
        panel["fieldConfig"]["defaults"]["thresholds"] = {"mode": "absolute", "steps": [{"color": "green", "value": None}, {"color": "red", "value": thresholds}]}
        panel["fieldConfig"]["defaults"]["color"] = {"mode": "thresholds"}
    panels.append(panel)


metric("Воркеры", "sum(up{job=\"worker\"})", 0, 0, 4, 4, "stat", legend="")
metric("Устарели", "ingestion_sources_stale", 4, 0, 4, 4, "stat", description="Нет успешного опроса за три интервала источника.", legend="", thresholds=1)
metric("Очередь статей", "sum(ingestion_article_fetch_jobs{status=~\"QUEUED|RETRY\"})", 8, 0, 4, 4, "stat", legend="")
metric("Статей DEAD", "ingestion_article_fetch_jobs{status=\"DEAD\"}", 12, 0, 4, 4, "stat", legend="", thresholds=1)
metric("Возраст очереди", "ingestion_article_job_due_age_max_seconds", 16, 0, 4, 4, "stat", "s", "Возраст самого старого задания, срок выполнения которого уже наступил.", "", 900)
metric("Сборщик БД", "ingestion_observer_db_up", 20, 0, 4, 4, "stat", "short", "1 означает успешный опрос БД. Пустое значение означает, что сам сборщик недоступен.", "")
panels[-1]["fieldConfig"]["defaults"]["thresholds"] = {"mode": "absolute", "steps": [{"color": "red", "value": None}, {"color": "green", "value": 1}]}
panels[-1]["fieldConfig"]["defaults"]["color"] = {"mode": "thresholds"}

metric("Опросы по результату · в минуту", "sum by(status)(rate(ingestion_source_polls_total[$__rate_interval])) * 60", 0, 4, 12, 8, legend="{{status}}")
metric("Длительность опроса · p95", "histogram_quantile(0.95, sum by(le,status)(rate(ingestion_source_poll_duration_seconds_bucket[$__rate_interval])))", 12, 4, 12, 8, unit="s", legend="{{status}}")
metric("Состояние источников", "sum by(health)(ingestion_sources)", 0, 12, 8, 7, legend="{{health}}")
metric("Новые материалы · в час", "sum by(kind)(rate(ingestion_source_poll_entries_total{kind=~\"new_articles|new_versions|failed\"}[$__rate_interval])) * 3600", 8, 12, 8, 7, legend="{{kind}}")
metric("Ответы 429 и 503 · за интервал", "sum by(upstream,status)(increase(ingestion_upstream_http_responses_total{status=~\"429|503\"}[$__rate_interval])) or vector(0)", 16, 12, 8, 7, legend="{{upstream}} {{status}}")
metric("Задания загрузки статей", "sum by(status)(ingestion_article_fetch_jobs)", 0, 19, 8, 7, legend="{{status}}")
metric("Задания ML", "sum by(status)(ingestion_processing_jobs)", 8, 19, 8, 7, legend="{{status}}")
metric("Просрочка заданий", "ingestion_article_job_due_age_max_seconds or ingestion_processing_job_due_age_max_seconds", 16, 19, 8, 7, unit="s", legend="{{__name__}}")
panels[-1]["targets"] = [
    {"expr": "ingestion_article_job_due_age_max_seconds", "legendFormat": "Статьи", "refId": "A"},
    {"expr": "ingestion_processing_job_due_age_max_seconds", "legendFormat": "ML", "refId": "B"},
]

metric("Самый долгий источник без успеха", "ingestion_source_last_success_age_max_seconds", 0, 26, 6, 6, unit="s", legend="")
metric("Самый просроченный опрос", "ingestion_source_poll_due_age_max_seconds", 6, 26, 6, 6, unit="s", legend="")
metric("Просрочено источников", "ingestion_sources_overdue", 12, 26, 6, 6, "stat", legend="", thresholds=1)
metric("Восстановлено lease · в час", "sum(rate(ingestion_source_leases_recovered_total[$__rate_interval])) * 3600", 18, 26, 6, 6, legend="")
metric("Длительность загрузки статьи · p95", "histogram_quantile(0.95, sum by(le,status)(rate(ingestion_article_job_duration_seconds_bucket[$__rate_interval])))", 0, 32, 12, 8, unit="s", legend="{{status}}")
metric("Длительность задания ML · p95", "histogram_quantile(0.95, sum by(le,status)(rate(ingestion_ml_job_duration_seconds_bucket[$__rate_interval])))", 12, 32, 12, 8, unit="s", legend="{{status}}")
metric("Все ответы внешних сервисов · в минуту", "sum by(upstream,status)(rate(ingestion_upstream_http_responses_total[$__rate_interval])) * 60", 0, 40, 8, 7, legend="{{upstream}} {{status}}")
metric("Попытки загрузки статей · в минуту", "sum by(status)(rate(ingestion_article_jobs_total[$__rate_interval])) * 60", 8, 40, 8, 7, legend="{{status}}")
metric("Попытки ML · в минуту", "sum by(status)(rate(ingestion_ml_jobs_total[$__rate_interval])) * 60", 16, 40, 8, 7, legend="{{status}}")

panels.append({
    "id": len(panels) + 1,
    "title": "Источники",
    "description": "Первые 50 включённых источников, начиная с проблемных. Нулевая просрочка означает, что время следующего опроса ещё не наступило.",
    "type": "table",
    "datasource": POSTGRES,
    "gridPos": {"x": 0, "y": 47, "w": 24, "h": 9},
    "targets": [{"refId": "A", "format": "table", "rawSql": "SELECT name AS \"Источник\", health AS \"Состояние\", round(extract(epoch from now()-coalesce(last_success_at,created_at)))::bigint AS \"Секунд без успеха\", greatest(round(extract(epoch from now()-next_poll_at))::bigint,0) AS \"Просрочено, с\", coalesce(last_error,'') AS \"Последняя ошибка\", id::text AS \"ID\" FROM sources WHERE enabled ORDER BY CASE WHEN health='HEALTHY' THEN 1 ELSE 0 END, last_success_at NULLS FIRST LIMIT 50"}],
    "fieldConfig": {"defaults": {}, "overrides": []},
    "options": {"showHeader": True},
})

panels.append({
    "id": len(panels) + 1,
    "title": "Логи воркера",
    "description": "JSON-логи с идентификаторами poll_id, article_job_id, job_id и trace_id.",
    "type": "logs",
    "datasource": LOKI,
    "gridPos": {"x": 0, "y": 56, "w": 18, "h": 9},
    "targets": [{"refId": "A", "expr": "{service_name=\"ingestion-worker\"}", "queryType": "range"}],
    "options": {"showTime": True, "showLabels": False, "showCommonLabels": False, "wrapLogMessage": True, "prettifyLogMessage": True, "enableLogDetails": True},
})

panels.append({
    "id": len(panels) + 1,
    "title": "Трейсы",
    "type": "text",
    "gridPos": {"x": 18, "y": 56, "w": 6, "h": 9},
    "options": {"mode": "markdown", "content": "### Где задержка?\n\nОткройте **Explore → Tempo** и найдите трассу по `trace_id` из лога или записи опроса.\n\nОпрос: `source.poll` → `http.fetch.feed` / `http.fetch.article`. Повтор статьи: `article.fetch_job`. ML: `ml.process_job` → `http.ml`. Это отдельные операции.\n\n[Открыть Explore](/explore)"},
})

dashboard = {
    "uid": "ingestion-overview", "title": "Источники и обработка", "tags": ["ingestion", "backend"],
    "timezone": "browser", "schemaVersion": 39, "version": 1, "refresh": "30s",
    "time": {"from": "now-6h", "to": "now"}, "templating": {"list": []},
    "panels": panels,
}

output = Path(__file__).parent / "dashboards" / "ingestion.json"
output.parent.mkdir(parents=True, exist_ok=True)
output.write_text(json.dumps(dashboard, ensure_ascii=False, indent=2) + "\n")
