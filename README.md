# Burmalda · Бизнес-пульс

**Событие → контекст компании → разбор с проверяемыми основаниями.**

Burmalda помогает разобраться, как новости и банковская аналитика могут повлиять на бизнес. Вы выбираете компанию и роль, открываете событие, изучаете выводы и источники, затем задаёте уточняющий вопрос по сохранённому разбору.

**React · Go · Python · PostgreSQL / pgvector · S3 · Qwen · LLM gateway**

[Полный запуск](#полный-запуск-с-нуля) · [Вход в аккаунт](#как-войти-в-тестовый-аккаунт) · [Источники](#как-добавить-новостной-источник) · [Решение проблем](#если-что-то-не-запустилось) · [Документация](#документация)

## Что можно сделать

| Возможность | Как это работает |
|---|---|
| Смотреть события | Новости и банковские документы превращаются в сохранённые события |
| Получить разбор | Go загружает доверенный профиль компании, ML формирует claims и проверяет их через обязательный verifier |
| Проверить основания | В карточке доступны цитаты, версии документов, ограничения и ссылки на первоисточники |
| Уточнить ответ | Чат использует сохранённые evidence карточки, без повторного поиска и reranker |
| Вернуться к результату | Разбор и диалог хранятся в PostgreSQL; обычное повторное открытие восстанавливает существующую операцию |
| Наблюдать за системой | Метрики, логи и трассировка доступны через Prometheus, Grafana, Loki и Tempo |

Если доказательств недостаточно, система должна вернуть `insufficient_evidence`. Если вывод не прошёл проверку, он не публикуется как готовый Insight.

## Выберите режим

| Режим | Что понадобится | Что получите |
|---|---|---|
| **Полный продукт — основной путь ниже** | Docker, исходный набор, кэш Qwen, ключ gateway | Реальный импорт, профиль, анализ, verifier, сохранение и чат |
| **Go + интерфейс без ML** | Docker и исходный набор | Регистрация, вход, сохранённые новости; новые ML-разборы недоступны |
| **Автономный макет** | Docker | Учебные карточки и публичный demo-логин, без Go и настоящих моделей |

Обычная команда `docker compose up` **не включает ML**. Для полного продукта нужны оба Compose-файла и профиль `ml`.

## Полный запуск с нуля

Команды рассчитаны на Bash/Zsh в macOS, Linux или WSL2. Выполняйте их из корня репозитория, если явно не указано другое.

### 1. Подготовьте инструменты и получите проект

Нужны:

- Git;
- работающий Docker Engine / Docker Desktop с **Compose v2 или новее**;
- Python **3.12** с `venv` для подготовки кэша моделей;
- интернет для первого скачивания зависимостей, весов и обращений к gateway;
- для полного локального ML рекомендуется **12 ГБ RAM, выделенных Docker**, и около **20 ГБ свободного диска** под образы, модели и данные.

Go и Node.js на хосте для Docker-запуска не требуются: они входят в сборочные образы.

```bash
git clone --branch fix/pr36-integration https://github.com/alfa-hackaton-team/burmalda.git
cd burmalda

docker version
python3.12 --version
```

Должна работать одна из команд: `docker compose version` или `docker-compose version`. Отдельный бинарник Compose v2+ тоже подходит; Python-пакет `docker-compose` v1 не подходит.

Эта инструкция соответствует ветке `fix/pr36-integration`. Для существующего checkout сначала проверьте `git status` и `git branch --show-current`: другой ветке может требоваться другая инструкция.

### 2. Подготовьте Python и проверьте исходные данные

```bash
python3.12 -m venv .venv
.venv/bin/python -m pip install -r ml/requirements-chunking.txt
```

В текущей ветке набор включён в `data/hackathon/`: **3 CSV, 9 PDF, 9 предоставленных TXT и `manifest.json`**. CSV нужны для профилей компаний, PDF/TXT — для банковского корпуса. Файлы не нужно загружать в интерфейс вручную.

Проверьте комплект и контрольные суммы до тяжёлой сборки:

```bash
.venv/bin/python - <<'PY'
import hashlib
import json
from pathlib import Path

root = Path('data/hackathon')
manifest = json.loads((root / 'manifest.json').read_text(encoding='utf-8'))

def verify(path, expected):
    if not path.is_file():
        raise SystemExit(f'Отсутствует файл: {path}')
    with path.open('rb') as stream:
        actual = hashlib.file_digest(stream, 'sha256').hexdigest()
    if actual != expected:
        raise SystemExit(f'Не совпадает SHA-256: {path}')

for entry in manifest['files']:
    path = root / entry['name']
    verify(path, entry['sha256'])
    if 'text_sha256' in entry:
        verify(path.with_suffix('.txt'), entry['text_sha256'])
print(f"Набор проверен: {manifest['version']}")
PY
```

Если файлов нет или хеш не совпал, восстановите оригинальный комплект. Не меняйте манифест ради обхода проверки и не подставляйте учебный профиль вместо импортированных данных. Подробности — [в описании набора](data/hackathon/README.md).

### 3. Создайте `.env` и задайте ключ gateway

В новом checkout:

```bash
cp .env.example .env
```

Если `.env` уже существует, сохраните его и обновите нужные значения вручную.

Откройте `.env`. Замените `ML_LLM_API_KEY=replace_with_your_team_key` на действующий ключ команды и установите:

```dotenv
ML_LLM_BASE_URL=https://changellenge.up.railway.app/v1
ML_ANALYSIS_CONFIG_VERSION=ml-analysis-v2
ML_ANALYSIS_RETRIEVAL_MODE=event-evidence
ML_PIPELINE_VERSION=ml-pipeline-v1
ML_MAX_TEXT_BYTES=131072
ML_BASE_URL=http://ml-api:8000
ML_EMBEDDING_BATCH_SIZE=1
FRONTEND_DATA_MODE=api
AUTH_PASSWORD_LOGIN_ENABLED=true
AUTH_SELF_REGISTRATION_ENABLED=true
FRONTEND_SELF_REGISTRATION_ENABLED=true
```

- **Gateway:** используйте действующий адрес команды. Если основной недоступен, согласованный резерв — `https://api.alfahack-gateway26.ru/v1`. Доступность адреса и лимиты нужно проверять в своём окружении.
- **Модели:** оставьте ID из `.env.example`; generation и verifier используют разные существующие настройки.
- **Версия анализа:** `ml-analysis-v2` отделяет результаты текущей конфигурации от предыдущих. Compose передаёт одно значение в Go и Python.
- **Пароли БД и служебный токен:** `.env.example` содержит согласованные значения для локального запуска. Если заменяете их, сделайте это **до первого создания БД**. Для паролей в DSN удобно использовать случайные hex-строки.

`.env` исключён из Git. Не публикуйте ключи, session cookies или полный вывод `docker compose config`. Локальные настройки из примера не предназначены для публичного deployment.

### 4. Один раз скачайте закреплённые веса

```bash
mkdir -p docker-volume/models docker-volume/postgres
HF_HUB_OFFLINE=0 ML_MODEL_CACHE="$PWD/docker-volume/models" \
  .venv/bin/python -m ml_runtime.warm_ml1_models
```

Загрузчик скачивает **две закреплённые ревизии Qwen**, суммарно около **2,3 ГБ**:

- `Qwen/Qwen3-Embedding-0.6B`;
- `Qwen/Qwen3-Reranker-0.6B`.

**Веса не лежат в GitHub и не входят в `pip install`.** Они сохраняются на диске в `docker-volume/models`. Контейнеры читают эту папку как `/models/hf` в offline-режиме. Обычный restart или пересборка образа не требуют повторного скачивания.

Если готовый кэш уже есть, можно скопировать его содержимое, включая `blobs` и `snapshots`, в `docker-volume/models` **этого checkout**. Compose не увидит автоматически кэш из другой копии репозитория.

После старта модели всё равно загружаются **с диска в RAM**. Это отдельный этап, даже когда скачивание уже не требуется. GLM/generation и production verifier вызываются через gateway; их веса локально скачивать не нужно.

### 5. Соберите продукт и проверьте gateway DNS

Чтобы во всех командах использовать одинаковый набор Compose-файлов, определите функцию в текущем терминале:

```bash
dc() {
  if docker compose version >/dev/null 2>&1; then
    docker compose -f compose.yaml -f compose.ml.yaml --profile ml "$@"
  else
    docker-compose -f compose.yaml -f compose.ml.yaml --profile ml "$@"
  fi
}

dc config --quiet
dc build
```

Первая сборка устанавливает в образы Go/Node/Python-зависимости, включая PyTorch. Это может занять заметное время. При повторной сборке используются слои Docker; кэш весов хранится отдельно.

Проверьте DNS gateway из того же сервисного окружения, не вызывая модель и не раскрывая ключ:

```bash
dc run --rm --no-deps ml-analysis-worker python -c \
  'import os, socket; from urllib.parse import urlparse; host = urlparse(os.environ["ML_LLM_BASE_URL"]).hostname; socket.getaddrinfo(host, 443); print("Gateway DNS: OK")'
```

Эта проверка подтверждает только DNS. Рабочий ключ, доступ к моделям и оставшийся лимит проверяются при настоящем запросе.

### 6. Запустите все сервисы

```bash
dc up -d
dc ps -a
```

При первом полном запуске автоматически выполняются:

1. запуск PostgreSQL 18 / pgvector и локального S3;
2. Go-миграции, включая `000027_insight_operation_aliases.sql`, и ML-миграции;
3. создание S3 bucket, проверка и импорт исходного набора;
4. запуск embedding/reranker, ML API и воркеров;
5. запуск Go API, frontend и наблюдаемости.

**Полный запуск начинает обработку импортированных банковских материалов через реальные модели и расходует лимит gateway.** Новые источники тоже добавляют работу в очередь. Если сейчас нужны только сборка, импорт и регистрация без автоматической платной обработки, вместо обычного запуска используйте:

```bash
dc up -d --scale worker=0 --scale ml-worker=0
```

В таком режиме новые события не обрабатываются. Чтобы явно включить обработку позже:

```bash
dc up -d --scale worker=1 --scale ml-worker=1
```

### 7. Дождитесь готовности

```bash
dc logs --tail=80 migrate ml-migrate s3-init data-import
curl --fail http://127.0.0.1:18080/health/ready
curl --fail http://127.0.0.1:8000/healthz
dc ps -a
```

Ожидается:

- `migrate`, `ml-migrate`, `s3-init`, `data-import` завершились с кодом **0** — это нормально для одноразовых сервисов;
- `embedding-model` и `reranker-model` имеют состояние **healthy**;
- Go readiness и ML health возвращают **200**;
- `worker`, `ml-worker`, `ml-analysis-worker` работают, если обработка не была намеренно остановлена.

`/healthz` ML API подтверждает готовность HTTP-процесса, а не успешность gateway или конкретного анализа. После старта ещё нужно дождаться индексации и извлечения событий.

Наблюдать за обработкой:

```bash
dc logs -f api worker ml-api ml-worker ml-analysis-worker embedding-model reranker-model
```

`Ctrl+C` завершает просмотр логов, а не контейнеры.

### 8. Откройте продукт

| Сервис | Адрес по умолчанию |
|---|---|
| **Бизнес-пульс** | **[http://127.0.0.1:13001/pulse](http://127.0.0.1:13001/pulse)** |
| Go readiness | [http://127.0.0.1:18080/health/ready](http://127.0.0.1:18080/health/ready) |
| ML health | [http://127.0.0.1:8000/healthz](http://127.0.0.1:8000/healthz) |
| Grafana | [http://127.0.0.1:13000](http://127.0.0.1:13000) |
| Prometheus | [http://127.0.0.1:19092](http://127.0.0.1:19092) |

В Grafana локально включён просмотр dashboards без входа. Администратор по умолчанию — `admin` / `admin`, если значения не переопределены в `.env`.

## Как войти в тестовый аккаунт

В режиме `api` общего заранее созданного логина нет. При первом входе создайте своего пользователя:

1. Откройте [Бизнес-пульс](http://127.0.0.1:13001/pulse) и вкладку **«Регистрация»**.
2. Заполните форму:

   | Поле | Пример |
   |---|---|
   | Ваше имя | Тестовый пользователь |
   | Логин | `demo.user` — если занят, выберите другой |
   | Пароль и повтор пароля | Свой пароль, не менее 12 символов |
   | ID компании из набора данных | `U_76` |
   | Должность | Владелец |

3. Нажмите **«Зарегистрироваться»** — интерфейс автоматически выполнит вход.
4. При следующем входе выберите **«Вход»** и используйте созданные логин и пароль.

Пользователь, профиль и context grant создаются на сервере. Компания `U_76` берётся из импортированного CSV. Учётная запись и результаты сохраняются после перезапуска контейнеров.

Учебные `demo.logistics` / `Logistics2026!` **не работают в Go API**. Их режим описан [ниже](#автономный-макет-без-go-и-ml). Сохранённые Insights другого пользователя не переносятся в новый аккаунт.

## Первый разбор и уточнение

1. Дождитесь появления событий в ленте. Для первых банковских событий используются импортированные материалы; RSS можно добавить отдельно.
2. Выберите нужный контекст компании и откройте событие. Для первой проверки удобно выбрать банковский материал о денежно-кредитной политике.
3. Дождитесь завершения анализа. Первое вычисление проходит через gateway и обязательный verifier; оно не является мгновенным чтением готового результата.
4. Откройте **«Основания»**: проверьте цитаты, источник, версию документа и ограничения. Публичная статья открывается по своему URL; закрытый оригинал скачивается через авторизованный Go route. Лимит закрытого оригинала в этом маршруте — **32 MiB**.
5. Во вкладке **«Уточнить»** задайте вопрос, прямо покрытый текстом карточки. Например, для материала о ставке: «Какое значение ключевой ставки указано в источнике?»
6. Обновите страницу и откройте событие повторно. Обычное открытие должно восстановить существующий разбор или выполняющуюся операцию, без нового платного анализа.

Чат использует сохранённый снимок evidence карточки и не запускает новый retrieval/reranker. Generation и production verifier при новом вопросе всё равно выполняются через gateway.

`insufficient_evidence` означает, что материалов или профиля недостаточно для ответа. `VERIFICATION_FAILED` означает, что результат не прошёл проверку. Не всякое событие применимо к каждой компании. После окончательного отказа действие **«Подготовить новый разбор»** явно создаёт новую операцию и новый модельный запрос; обычный reload этого не делает.

## Как добавить новостной источник

Источниками управляет оператор через существующий CLI или административный API с отдельными правами. Регистрация обычного пользователя не выдаёт права администратора источников.

Проверьте существующие источники:

```bash
dc exec api /app/sourcectl list
```

Для новой RSS-ленты Federal Reserve подготовьте конфигурацию с загрузкой полных статей. Базовый JSON в репозитории включает только RSS; этому сайту нужен дополнительный HTML-контейнер:

```bash
mkdir -p .local
.venv/bin/python - <<'PY'
import json
from pathlib import Path

config = json.loads(Path('backend/examples/sources/federal-reserve-press.json').read_text())
config['adapter_config'] = {'article_html': True, 'article_container_id': 'article'}
path = Path('.local/federal-reserve.source.json')
path.write_text(json.dumps(config, ensure_ascii=False, indent=2), encoding='utf-8')
path.chmod(0o644)
PY

dc cp .local/federal-reserve.source.json api:/tmp/federal-reserve.source.json
dc exec api /app/sourcectl create /tmp/federal-reserve.source.json
```

Сохраните `source_id` из ответа. Не создавайте источник повторно при каждом запуске. Состояние можно получить командой `sourcectl get` с этим ID; для изменения используется `sourcectl patch SOURCE_ID CONFIG_VERSION FILE`.

Включённый источник подхватывается воркером автоматически. Полный путь: опрос → оригинал в S3 → нормализованная статья → ML-индексация → извлечение события → карточка. Полученная статья не обязательно создаёт событие: это зависит от качества и содержания материала.

Внешние RSS/HTML могут временно отвечать ошибками или менять разметку. Не называйте RSS-анонс полной статьёй. Статусы загрузки и ML-обработки проверяются отдельно:

```bash
dc logs --tail=100 worker ml-worker
dc exec db psql -U postgres -d burmalda -c "SELECT status, count(*) FROM processing_jobs GROUP BY status;"
dc exec db psql -U postgres -d burmalda -c "SELECT state, count(*) FROM ml.article_processings GROUP BY state;"
```

Для выбранных сохранённых версий есть штатные `mlctl enqueue` и `mlctl export`; их применение описано в [управлении корпусом](docs/operations/ml-ingestion.md). Не ставьте весь корпус повторно в обработку ради одной карточки.

## Повседневные команды

Функцию `dc` из шага 5 нужно определить заново в новом терминале. Она выбирает доступный Compose: плагин `docker compose` или отдельный `docker-compose`. Либо используйте полную команду `docker compose -f compose.yaml -f compose.ml.yaml --profile ml` (для отдельного бинарника замените `docker compose` на `docker-compose`).

```bash
# Статус, включая завершённые миграторы
dc ps -a

# Последние ошибки анализа
dc logs --tail=100 api ml-api ml-analysis-worker

# Перезапуск API без удаления данных
dc restart api ml-api

# Остановка полного стенда с сохранением данных
dc down

# Повторный запуск с прежними данными и весами
dc up -d
```

| Что сохраняется | Где |
|---|---|
| Пользователи, профили, Insight, диалоги, ML-операции и векторы | `docker-volume/postgres` |
| Веса Qwen и tokenizer | `docker-volume/models` |
| Оригиналы в локальном S3 | Compose volume `s3local_data` |
| Данные наблюдаемости | Остальные именованные Compose volumes |
| Gateway-ключи и локальные настройки | `.env`, вне Git |

`dc down` не удаляет эти данные. Для обычного перезапуска не используйте `down -v`, не удаляйте `docker-volume` и не выполняйте `docker system prune --volumes`.

Пересборка **не обновляет автоматически** уже работающие контейнеры: после изменения исходников используйте `dc up -d --build`. Если обновляете существующий стенд с предыдущей версии, сначала прочитайте [порядок миграции и обновления компонентов](docs/operations/pr36-integration-fixes.md).

## Если что-то не запустилось

| Симптом | Что проверить |
|---|---|
| `docker: command not found` или daemon недоступен | Установите/запустите Docker. Для Colima нужен отдельный рабочий Docker CLI с Compose |
| Порт занят | Измените порт в `.env`, затем пересоздайте контейнер; таблица переменных ниже |
| `data-import` завершился с ошибкой | Посмотрите его логи, наличие файлов, manifest и SHA-256. Go API ждёт успешного импорта |
| `embedding-model` / `reranker-model` не healthy | Память Docker, логи, обе закреплённые модели и кэш именно в `docker-volume/models` этого checkout |
| `137`, `Killed`, `OOM` | Контейнеру не хватило памяти. Проверьте лимит RAM Docker; не запускайте тяжёлые ML-тесты параллельно с импортом |
| ML API отвечает 200, но карточек нет | HTTP-процесс уже запущен, а индексация/извлечение ещё не завершились. Проверьте воркеры и состояния обработки |
| Вход работает, новые разборы недоступны | Оба Compose-файла, профиль `ml`, gateway-ключ, `ML_BASE_URL=http://ml-api:8000` и согласованный токен |
| Нет вкладки регистрации | `AUTH_PASSWORD_LOGIN_ENABLED`, `AUTH_SELF_REGISTRATION_ENABLED` и frontend build-параметр `FRONTEND_SELF_REGISTRATION_ENABLED` должны быть `true` |
| `403` при входе/регистрации с другого порта | Добавьте точный origin frontend в `API_ALLOWED_ORIGINS`; `localhost` и `127.0.0.1` — разные origin |
| Gateway DNS / connection error | Проверьте `ML_LLM_BASE_URL`, сеть и согласованный резерв. Наличие весов не исправляет недоступность gateway |
| Gateway `401` / `403` / `429` | Ключ, разрешения или лимиты провайдера. Не повторяйте платные операции бесконтрольно и не отключайте verifier |
| `insufficient_evidence` / отказ проверки | Прочитайте безопасную ошибку и код обращения; проверьте основания. Это не доказательство проблем с загрузкой весов |
| Пароль БД изменён в `.env`, а вход БД не работает | Init-скрипты задают пароли только для новой БД. Верните согласованные значения либо обновите роли штатно; не удаляйте БД ради сброса |
| Docker build сообщает PAX/tar error | Проверьте актуальность Docker/Compose/Buildx и `.dockerignore`. Не меняйте ML-алгоритм ради ошибки сборочного контекста |

### Если стандартные порты заняты

Добавьте в `.env` нужные значения **до запуска**. Например:

```dotenv
FRONTEND_HTTP_PORT=23001
BACKEND_HTTP_PORT=28080
ML_HTTP_PORT=28000
POSTGRES_PORT=25432
LOCAL_S3_PORT=28333
GRAFANA_PORT=23000
PROMETHEUS_PORT=29092
API_ALLOWED_ORIGINS=http://127.0.0.1:23001,http://localhost:23001
```

После этого frontend — `http://127.0.0.1:23001/pulse`, Go readiness — `http://127.0.0.1:28080/health/ready`, ML health — `http://127.0.0.1:28000/healthz`. Между контейнерами оставляйте `ML_BASE_URL=http://ml-api:8000`: внутренний порт не меняется вместе с внешним.

Для второго независимого стенда нужен отдельный checkout/каталог PostgreSQL, другие порты и отдельный Compose project. Одно изменение имени проекта не изолирует общий bind mount `docker-volume/postgres`.

## Другие режимы

### Go и интерфейс без новых ML-разборов

При подготовленном исходном наборе и без заданных ML-настроек запускается базовый Compose:

```bash
docker compose up -d --build
```

Доступны регистрация, сессии, импортированные данные и сохранённые новости. Этот режим не заменяет полный запуск ML. Если используется отдельный Compose, в командах этого раздела замените `docker compose` на `docker-compose`. Для чистого запуска выбирайте режим до старта: одноимённые сервисы базового и полного Compose в одном каталоге не являются двумя независимыми стендами.

### Автономный макет без Go и ML

```bash
FRONTEND_DATA_MODE=demo docker compose up -d --build --no-deps frontend
```

Откройте `http://127.0.0.1:13001/pulse` и выберите:

| Поле | Значение |
|---|---|
| Компания | `U_76` |
| Должность | Финансовый директор |
| Логин | `demo.logistics` |
| Пароль | `Logistics2026!` |

Это публичные учебные credentials для макета. Они не создают Go-пользователя и не проверяют настоящий ML. Чтобы вернуть интерфейс к работающему полному стенду:

```bash
FRONTEND_DATA_MODE=api dc up -d --build frontend
```

## Разработка и проверки

Сборка через Docker не требует локальных Go/Node, но для разработки пригодятся версии из `backend/go.mod`, `frontend/package.json` и текущего CI.

```bash
# Frontend: зависимости, форматирование, тесты, TypeScript и production build
cd frontend
npm ci
npm run check
```

Для Go-тестов из `backend/` нужна отдельная тестовая PostgreSQL и `TEST_DATABASE_URL`:

```bash
go test -race -count=1 -p 1 ./...
go vet ./...
```

Для ML runtime нужны его зависимости и отдельная `ML_TEST_DATABASE_URL`:

```bash
python -m unittest discover -s ml_runtime/tests -q
```

Не направляйте тестовые DSN на пользовательскую БД. ML1 real-weight/evaluation suites не являются обязательным шагом обычного запуска. Полные команды CI находятся в [workflow](.github/workflows/ci.yml).

Инструкция запуска сама по себе не доказывает стабильность модели на любых новостях и профилях. Результаты последних проверок исправлений, границы live-проверки и порядок обновления записаны в [отчёте интеграции](docs/operations/pr36-integration-fixes.md).

## Документация

| Документ | Когда открывать |
|---|---|
| [Обзор документации](docs/README.md) | Найти нужную часть проекта |
| [Go-бэкенд](backend/README.md) | API, авторизация, команды оператора и настройки |
| [Frontend](frontend/README.md) | Vite, режимы интерфейса и локальная разработка |
| [ML через Docker](docs/ml/docker.md) | Устройство model-сервисов и ML runtime |
| [Исходный набор](data/hackathon/README.md) | CSV, PDF/TXT, манифест, импорт и provenance |
| [Загрузка источников](docs/ingestion/source-ingestion.md) | RSS/HTML, оригиналы, версии и ошибки извлечения |
| [Управление ML-корпусом](docs/operations/ml-ingestion.md) | Адресная постановка версий и экспорт payload |
| [Обновление интеграции](docs/operations/pr36-integration-fixes.md) | Миграция, восстановление операций, источники и ограничения |
| [Внешний OpenAPI](docs/api/openapi.yaml) · [Go → ML](docs/api/ml-openapi.yaml) | HTTP-контракты |
| [Архитектура](docs/architecture/architecture.md) · [PRD](docs/product/PRD.md) | Устройство и границы продукта |
