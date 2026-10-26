# ML: текущее состояние и контракты

В этой ветке объединены ML1 и ML2. ML1 принимает сохранённые Go-версии документов, проверяет качество, создаёт чанки и embeddings, хранит индекс в PostgreSQL/pgvector и выполняет retrieval. ML2 получает документы и профиль, извлекает типизированные события, проверяет основания и сохраняет результат. Обмен с Go идёт через HTTP; ML владеет своей схемой `ml` и миграциями.

## Основные контракты и запуск

| Документ | Что в нём |
|---|---|
| [ML HTTP OpenAPI](../api/ml-openapi.yaml), [общие схемы](../api/schemas.yaml) | Payload Go → ML, polling и состояния операции |
| [Приём документов](article-processing.md), [R02 quality gate](document-quality.md) | Валидация сохранённых документов и решение PASS/FAIL |
| [Контракт извлечения событий](event-extraction-contract.md) | Результат ML2, evidence и конфигурация извлечения |
| [Архитектура](architecture.md), [совместимость API](api-compatibility.md) | Границы модулей и соответствие общему API |
| [ML1 pipeline](../../ml/PIPELINE.md), [storage](../../ml/PERSISTENCE.md), [retrieval](../../ml/RETRIEVAL.md) | Индексация, миграции и поиск |
| [Docker Compose](docker.md) | Локальная БД, миграции, API и worker |
| [Python ML runtime](../../ml/README.md) | Зависимости, библиотеки и команды разработки |

## Согласованные поля

- Go передаёт `article.version_no`, `source.config_version`, `pipeline_version=ml-pipeline-v1` и `metadata.extraction_method` с `metadata.extraction_errors`.
- `ML_MAX_TEXT_BYTES=131072` относится к `article.body` в UTF-8 байтах, без заголовка; слишком большой текст отклоняется без обрезания.
- ML сохраняет `document_version_id`, равный входному `article_version_id`. Идемпотентность операции закреплена ключом Go и отпечатком значимых полей.
- `raw_ref` остаётся ссылкой на сохранённый оригинал; ML не скачивает произвольные URL.
- `SUCCEEDED` выставляется после сохранения результатов обработки. Polling через GET не удаляет результат.

## Проверки

Из корня checkout:

```bash
.venv/bin/python -m unittest discover -s ml/tests -v
.venv/bin/python -m unittest discover -s docs/ml/scripts -p 'test_*.py' -v
.venv/bin/python docs/ml/scripts/check_ml_contracts.py --full
.venv/bin/python docs/ml/scripts/check_api_contracts.py
docker-compose --env-file .env.example config -q
docker-compose --env-file .env.example build
```

Для PostgreSQL-тестов задайте `ML1_TEST_POSTGRES_DSN` на отдельную одноразовую базу с pgvector. Вес embedding-модели нужен для настоящей индексации. Настоящая обработка Go-материала также требует локальный `.env` с ключом gateway; секрет нельзя класть в Git или передавать в чаты.
