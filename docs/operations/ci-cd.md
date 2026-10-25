# CI/CD

## Что запускается сейчас

На каждом PR и обновлении `main` workflow `CI and images` выполняет:

1. Проверку синтаксиса workflow, Go-модулей, форматирования и `go vet`.
2. Применение миграций к чистому PostgreSQL 17 дважды, затем Go-тесты с `-race` и настоящей БД.
3. Проверку ML и HTTP-контрактов, fixtures и соответствия сгенерированных файлов Grafana исходникам.
4. `govulncheck` для достижимых уязвимостей Go.
5. Сборку backend-контейнера и сканирование всех собранных Go-бинарников. В нём есть `/app/api`, `/app/worker`, `/app/observer`, `/app/retention` и `/app/migrate`.

При наличии каталога `ml/` CI дополнительно требует `ml/ci.sh` (исполняемый файл с проверками ML-сервиса) и `ml/Dockerfile`, запускает проверки и собирает образ. Сейчас в репозитории есть только документы и контракты ML; это отдельная проверка, а не подтверждение работы ML-сервиса. При появлении runtime его проверки не будут молча пропущены.

После успешных проверок коммита в `main` workflow публикует `ghcr.io/alfa-hackaton-team/burmalda/backend:sha-<commit>` и, если есть runtime, `.../ml:sha-<commit>`. Сборка отправляет SBOM и provenance в реестр. В summary запуска записываются digest образов. Для выкладки используйте digest, а не изменяемый тег. PR не получает право публикации.

Dependabot еженедельно предлагает обновления Go, Python-проверок, базового образа и GitHub Actions. Изменения проходят те же проверки.

## Kubernetes

Кластера пока нет, поэтому автоматическая выкладка в staging/production не подключена. Публикация образа в GHCR не означает успешный деплой. Перед включением выкладки нужны:

1. Кластер и отдельные namespaces для staging и production, PostgreSQL, сетевой доступ к согласованному ML-сервису и источникам, а также политика для исходящих запросов.
2. Способ аутентификации CI в кластере: предпочтительно короткоживущая identity через OIDC либо изолированный runner внутри кластера с ограниченным ServiceAccount. Доступ только к нужному namespace; не хранить постоянный admin kubeconfig в GitHub Secrets.
3. Kubernetes-манифесты или Helm chart для API, worker, observer, retention и однократного migration Job. Указать requests/limits, probes, graceful shutdown, Secret для `DATABASE_URL` и ML-токена, ConfigMap для YAML-настроек, отдельные учётные записи и права. ML runtime должен иметь собственный chart/manifests и probes.
4. GitHub Environments `staging` и `production` с ограничением веток и обязательным подтверждением production. После сборки выкладывать digest сначала в staging, запускать миграции один раз, ждать rollout и проверять `/health/ready` и метрики; затем продвигать тот же digest в production. Для несовместимых миграций применять поэтапные схемы expand/migrate/contract.
5. Настроенные алерты, резервные копии PostgreSQL, проверенный откат приложения и процедуры восстановления БД. Откат образа не откатывает миграцию автоматически.

Когда кластер появится, CD workflow должен получать его адрес и права из настроенного механизма identity. До этого добавлять фиктивные kubeconfig, IP или секреты в репозиторий нельзя.

Для merge включите branch protection на `main`: обязательные jobs `workflow-lint`, `backend`, `contracts`, `ml-runtime`, `vulnerability-check`, `images`; запрет прямых push и подтверждение ревью. GitHub secret scanning и защита push настраиваются в параметрах репозитория.
