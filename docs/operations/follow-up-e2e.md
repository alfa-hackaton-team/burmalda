# Backend follow-up E2E

Запуск из checkout с работающим локальным PostgreSQL в Docker:

```sh
python3 scripts/test_follow_up_e2e.py
```

Нужны существующие образы `burmalda-ml-worker`, `golang:1.27.3-alpine` и кеши
`burmalda-go-mod` / `burmalda-go-build`. Runner принимает `--db-container`,
`--ml-image`, `--go-image`; по умолчанию БД — контейнер `burmalda-db-1`.
Go dependencies читаются offline (`GOPROXY=off`). Gateway-ключ не требуется.

Тест создаёт отдельную временную БД и сохраняет в ней verified fixture Insight.
Production demo insights и конфигурация работающих сервисов не меняются.
Corpus-таблиц в тестовой БД нет. Runner поднимает отдельный Python HTTP bridge,
запускает Go test и удаляет bridge; Go test удаляет свою БД.

Реальные компоненты цепочки:

- HTTP-клиент и Go API на настоящем TCP-сокете;
- session cookie, PostgreSQL session store, CSRF и context grant;
- создание conversation, очереди request_operations и worker iteration;
- межпроцессный HTTP-запрос в Python;
- AnalysisService, AnalysisAgent, citation support guard, LLMSemanticVerifier adapter;
- публикация ответа в PostgreSQL и чтение messages/generation через API.

Generation и verdict модели заменены deterministic fixtures. Python HTTP bridge
находится только в `ml_runtime/tests/`: это тестовый adapter, не production
реализация `/internal/v0.1/follow-ups`. Тест не подтверждает deployment этого
endpoint, качество реальной модели, model latency или browser integration.
Frontend, реальные модели, ML1 weights, поиск/reranker и precompute не запускаются.

Сценарии: grounded answer; insufficient_evidence; verifier reject; неточная
цитата; ссылка на chunk вне карточки. Проверяются сохранённый initial snapshot,
exact evidence в ответе, отсутствие assistant message при отказе, повтор submit
с тем же idempotency key, отсутствие сессии, отозванный grant и неизменность карточки.
Счётчики между процессами должны показать 5 generation, 5 guard и 2 verifier;
search, get_chunks и вход в search runner — 0. Если search runner не вызывается,
ветка ML1 retrieval/reranker не выполняется; её выполнение также запрещено tripwire.

Для отдельной проверки ML2 без HTTP:

```sh
python -m unittest ml_runtime.tests.test_follow_up_snapshot
```
