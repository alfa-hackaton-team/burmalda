# Go-бэкенд

Один Go-модуль содержит процессы `api`, `worker`, мигратор и локальную команду `sourcectl`. `api` и `worker` проверяют соединение с PostgreSQL, API предоставляет проверки состояния. Схема и реестр источников готовы; опрос источников и внешний административный API будут добавлены следующими этапами.

## Локальный запуск

Требуется Go 1.25+ и Docker Compose. Из каталога `backend`:

```sh
docker compose up -d db
export DATABASE_URL='postgres://burmalda:burmalda@127.0.0.1:5432/burmalda?sslmode=disable'
go run ./cmd/migrate
export HTTP_ADDR=127.0.0.1:18080
go run ./cmd/api
```

В другом терминале из того же каталога:

```sh
export DATABASE_URL='postgres://burmalda:burmalda@127.0.0.1:5432/burmalda?sslmode=disable'
go run ./cmd/worker
```

Проверки API: `curl http://127.0.0.1:18080/health/live` отвечает без обращения к БД, а `/health/ready` проверяет PostgreSQL и возвращает `503`, когда соединение недоступно. Оба процесса завершаются по `SIGINT`/`SIGTERM`. Остановить локальную БД: `docker compose down` (данные останутся в volume).

## Миграции

SQL-файлы лежат в [migrations](migrations). Команда `go run ./cmd/migrate` применяет новые версии по порядку в одной транзакции, берёт advisory lock и записывает контрольные суммы в `schema_migrations`. Повторный запуск безопасен; изменение уже применённого SQL останавливает команду с ошибкой. Миграции запускаются отдельно перед запуском новой версии сервисов. Автоматического отката нет: для изменения схемы добавляется следующий SQL-файл.

Первая миграция создаёт `sources`, `source_polls`, `raw_artifacts`, `articles`, `article_versions` и `processing_jobs`. Оригинал HTML/PDF хранится в `raw_artifacts.content` (`bytea`) либо по `object_uri`; ровно одно место хранения обязательно. Для одинаковых байтов одного источника используется один артефакт по SHA-256. `article_versions` остаются отдельными версиями нормализованного текста и ссылаются на артефакт. Сроки хранения и партиционирование пока не заданы: сначала нужно утвердить retention и зависимости доказательств от версий.

Миграция `000003` добавляет к версии статьи необязательные язык и автора, объект метаданных и проверку непустого текста. Задание на ML хранит безопасные поля источника отдельными колонками, чтобы повторить исходный запрос после изменения реестра. `source_id` берётся из версии статьи. Для пары `(article_version_id, pipeline_version)` уже действует уникальность; успешное задание обязано содержать `document_version_id`. Численный предел текста и состав `metadata` ещё согласуются с ML1.

## Реестр источников

`sourcectl` — локальная команда оператора, работающая напрямую с PostgreSQL. Её можно использовать до подключения сессионной авторизации административного API. Команды из каталога `backend` при установленном `DATABASE_URL`:

```sh
go run ./cmd/sourcectl create source.json
go run ./cmd/sourcectl list 25
go run ./cmd/sourcectl get SOURCE_ID
go run ./cmd/sourcectl patch SOURCE_ID CONFIG_VERSION patch.json
```

`source.json` соответствует `SourceConfigInput` в [схеме API](../docs/api/schemas.yaml). Например:

```json
{
  "name": "Example feed",
  "source_type": "news",
  "source_role": "external",
  "trust_level": "reviewed",
  "adapter_type": "rss",
  "endpoint": "https://example.com/feed.xml",
  "enabled": true,
  "polling_interval_seconds": 900
}
```

Пока принимаются конфигурации `rss` и `atom` с пустым `adapter_config` и без `credential_ref`; сами адаптеры ещё не опрашивают источники. Интервал — от 60 секунд до суток. При сохранении проверяются URL и его текущие DNS-адреса; воркер повторит проверку при каждом сетевом соединении и перенаправлении. `patch` принимает частичный JSON и требует актуальный `config_version`; изменение конфигурации сбрасывает lease воркера, а смена адреса или адаптера также сбрасывает HTTP-валидаторы и курсор. Команда имеет доступ к БД и предназначена для доверенного оператора, не для вызова из интерфейса.

Маршруты `GET/POST /v0.1/admin/sources` и `GET/PATCH /v0.1/admin/sources/{source_id}` реализованы в `internal/sources.AdminHandler`. Они не подключены к `cmd/api`, пока нет серверной сессии, проверки административных прав, CSRF и Origin. Конструктор требует авторизатор; без него маршруты не создаются. При подключении PATCH использует `If-Match: "<config_version>"`, а ответы возвращают `ETag`.

Параметры окружения перечислены в [.env.example](.env.example). `DATABASE_URL` обязателен; остальные имеют значения по умолчанию. Примерные учётные данные предназначены только для локальной разработки. Не сохраняйте рабочие секреты в репозитории.
