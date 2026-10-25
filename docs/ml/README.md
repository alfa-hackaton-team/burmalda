# ML: состояние и контракты

Актуализировано 25 октября 2026 года. HTTP-контракты взяты из `main` (`52a20bb`, после объединения #27).

ML1 отвечает за качество документов, чанки и поиск. ML2 — за профиль, события, агента и проверку выводов. Go сохраняет источники, оригиналы и версии, доставляет задания и публикует результаты.

## Реализовано в этой ветке

- R01: синхронные `Retriever.search/get_chunks` на fixtures/in-memory; 27 тестов. Это точные настроенные запросы, не семантический поиск.
- R02: строгий вход → immutable документ → deterministic quality gate; 28 тестов и 8 synthetic corpus fixtures.
- Пять payload из #27 проверены через R02 без изменений кода. Все дали PASS с ожидаемыми предупреждениями. HTTP-прогон и полный банковский корпус не проверены.

Реальный индекс, embeddings, production retrieval и HTTP ML в этой ветке ещё не реализованы. PASS качества не означает готовность поиска или завершение полной операции.

## Основные документы

| Документ | Назначение |
|---|---|
| [HTTP OpenAPI](../api/ml-openapi.yaml), [общие схемы](../api/schemas.yaml) | Контракты Go ↔ ML из main |
| [Логические DTO v1](contracts/v1/contracts.schema.json), [семантика](contracts.md) | Retrieval, analysis и follow_up |
| [Приём документов](article-processing.md) | Отображение Go → ML1 и lifecycle |
| [Правила R02](document-quality.md), [quality schema](contracts/quality/v1/quality.schema.json) | Внутренний библиотечный контракт R02 |
| [Архитектура](architecture.md), [план](development.md) | Ответственность и следующие этапы |
| [Совместимость API](api-compatibility.md) | Преобразование результата ML в публичный ответ |
| [Запуск Python ML](../../ml/README.md) | Зависимости, примеры и команды |

`reference/` — исторические снимки. `ml1/` — локальный контекст разработчика, не дополнительный внешний контракт. Фактическую БД определяют миграции Go; SQL RFC не доказывает наличие ML-таблиц.

## Проверки

Из корня checkout, в окружении с `ml/requirements.txt` и `docs/ml/requirements-validation.txt`:

```sh
python -m unittest discover -s ml/tests -v
python -m unittest discover -s docs/ml/scripts -p 'test_*.py'
python docs/ml/scripts/check_ml_contracts.py --full
python docs/ml/scripts/check_api_contracts.py
```

Схемы читаются runtime из `docs/`: поставлять `ml/` и `docs/` вместе. Fixtures и локальные тесты не подтверждают работу production HTTP, БД или смысловую достоверность выводов.
