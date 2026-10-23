# Go-бэкенд

Один Go-модуль содержит два запускаемых процесса: `api` и `worker`. Пока это основа сервиса: оба процесса проверяют соединение с PostgreSQL, API предоставляет проверки состояния, а обработка источников будет добавлена следующим этапом. Продуктовые маршруты и миграции ещё не реализованы.

## Локальный запуск

Требуется Go 1.25+ и Docker Compose. Из каталога `backend`:

```sh
docker compose up -d db
export DATABASE_URL='postgres://burmalda:burmalda@127.0.0.1:5432/burmalda?sslmode=disable'
export HTTP_ADDR=127.0.0.1:18080
go run ./cmd/api
```

В другом терминале из того же каталога:

```sh
export DATABASE_URL='postgres://burmalda:burmalda@127.0.0.1:5432/burmalda?sslmode=disable'
go run ./cmd/worker
```

Проверки API: `curl http://127.0.0.1:18080/health/live` отвечает без обращения к БД, а `/health/ready` проверяет PostgreSQL и возвращает `503`, когда соединение недоступно. Оба процесса завершаются по `SIGINT`/`SIGTERM`. Остановить локальную БД: `docker compose down` (данные останутся в volume).

Параметры окружения перечислены в [.env.example](.env.example). `DATABASE_URL` обязателен; остальные имеют значения по умолчанию. Примерные учётные данные предназначены только для локальной разработки. Не сохраняйте рабочие секреты в репозитории.
