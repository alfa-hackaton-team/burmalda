# Контракты ML v1

Редакция 1.1 до первого выпуска. `contracts.schema.json` содержит определения Draft 2020-12 в `$defs`. Для SearchRequest валидируйте через `$ref: #/$defs/SearchRequest` с корнем этого файла. Остальные корневые сообщения: SearchResponse, GetChunksRequest, GetChunksResponse, ReadyVersionsRequest, ReadyVersionsResponse, AnalysisRequest, AnalysisResponse. InsightSnapshot используется внутри FollowUp.

Включайте проверку format в выбранном валидаторе. Верхний объект — контейнер определений: валидация против него без выбора `$ref` не проверяет форму сообщения. Правила доступа, пары запрос/ответ, снимка истории и смысла evidence описаны в [contracts.md](../../contracts.md).

## Проверка без зависимостей

Из корня репозитория:

```bash
python3 docs/ml/scripts/check_ml_contracts.py
python3 -m unittest discover -s docs/ml/scripts -p 'test_*.py'
```

Или из автономной папки `docs/ml`: `python3 scripts/check_ml_contracts.py` и `python3 -m unittest discover -s scripts -p 'test_*.py'`.

Первый скрипт проверяет используемое подмножество схемы, 16 fixtures, пары запрос/ответ и сквозную последовательность analysis → follow_up. `analysis.ok/insufficient/failed` — альтернативные исходы одной операции; initial и follow_up — два разных запуска с разными trace/run/operation ID. Отрицательные тесты воспроизводят ошибки из ревью: отсутствующий/подменённый снимок, чужие ссылки, неверные цитаты, нарушенную границу истории и повторное использование trace/claims.

Это не общий production-валидатор. Проверки сравнивают переданные синтетические данные, а не выполняют авторизацию по БД и не доказывают смысловую поддержку утверждений.

## Полная JSON Schema и граница API

Из корня репозитория, в отдельном окружении:

```bash
python3 -m venv .venv-contracts
.venv-contracts/bin/python -m pip install -r docs/ml/requirements-validation.txt
.venv-contracts/bin/python docs/ml/scripts/check_ml_contracts.py --full
.venv-contracts/bin/python docs/ml/scripts/check_api_contracts.py
```

`--full` включает Draft 2020-12 и format через jsonschema. Проверка API разрешает локальные `$ref`, проверяет входы/выходы HTTP анализа и уточнения, соответствие terminal state результату, публичные fixtures и пример преобразования action → AI_INFERENCE + SCENARIOS_ACTIONS. Полный OpenAPI conformance, runtime-адаптер, миграции и сервисные тесты этой командой не реализуются.

После установки зависимостей все проверки работают без сети и платных вызовов моделей. Окружение и кэш не коммитятся. Изменение полей сопровождается обновлением схемы, fixtures, отрицательных тестов, документации и адаптеров; после выпуска несовместимые изменения получают новую версию.
