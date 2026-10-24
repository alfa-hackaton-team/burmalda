import json
from pathlib import Path


def rule(uid, title, expression, threshold, pending, severity, panel, no_data="Alerting"):
    return {
        "uid": uid,
        "title": title,
        "condition": "B",
        "data": [
            {
                "refId": "A",
                "datasourceUid": "prometheus",
                "relativeTimeRange": {"from": 3600, "to": 0},
                "model": {"datasource": {"type": "prometheus", "uid": "prometheus"}, "expr": expression, "instant": True, "range": False, "intervalMs": 1000, "maxDataPoints": 43200, "refId": "A"},
            },
            {
                "refId": "B",
                "datasourceUid": "__expr__",
                "relativeTimeRange": {"from": 0, "to": 0},
                "model": {"datasource": {"type": "__expr__", "uid": "__expr__"}, "expression": f"$A > {threshold}", "type": "math", "refId": "B"},
            },
        ],
        "noDataState": no_data,
        "execErrState": "Error",
        "for": pending,
        "labels": {"severity": severity, "component": "ingestion"},
        "annotations": {"summary": title},
        "dashboardUid": "ingestion-overview",
        "panelId": panel,
    }


rules = [
    rule("ingestion-worker-down", "Воркер недоступен", "1 - min(up{job=\"worker\"})", 0, "2m", "critical", 1),
    rule("ingestion-observer-down", "Не собирается состояние БД", "1 - ingestion_observer_db_up", 0, "2m", "critical", 6),
    rule("ingestion-sources-stale", "Источники долго не опрашивались", "ingestion_sources_stale", 0, "5m", "warning", 2),
    rule("ingestion-articles-dead", "Есть статьи DEAD", "ingestion_article_fetch_jobs{status=\"DEAD\"}", 0, "1m", "warning", 4),
    rule("ingestion-article-backlog", "Очередь статей отстаёт", "ingestion_article_job_due_age_max_seconds", 900, "5m", "warning", 5),
    rule("ingestion-ml-backlog", "Очередь ML отстаёт", "ingestion_processing_job_due_age_max_seconds", 900, "5m", "warning", 14),
    rule("ingestion-poll-errors", "Повторяются неудачные опросы", "sum(increase(ingestion_source_polls_total{status=~\"FAILED|PARTIAL\"}[3h])) or vector(0)", 2, "5m", "warning", 7, "OK"),
    rule("ingestion-rate-limits", "Повторяются ответы 429/503", "sum(increase(ingestion_upstream_http_responses_total{status=~\"429|503\"}[30m])) or vector(0)", 5, "5m", "warning", 11, "OK"),
]

output = Path(__file__).parent / "provisioning" / "alerting" / "alerts.json"
output.parent.mkdir(parents=True, exist_ok=True)
output.write_text(json.dumps({"apiVersion": 1, "groups": [{"orgId": 1, "name": "Source ingestion", "folder": "Ingestion", "interval": "1m", "rules": rules}]}, ensure_ascii=False, indent=2) + "\n")
