# Приём сохранённых материалов Go → ML

HTTP-контракт: [ml-openapi.yaml](../api/ml-openapi.yaml), `NormalizedArticle` и `ArticleProcessingOperation` в [schemas.yaml](../api/schemas.yaml). Эти файлы перенесены из `main` после #27. Библиотечный R02 описан в [document-quality.md](document-quality.md).

## Данные и ответственность

Go загружает и сохраняет оригинал, текст, версию, снимок источника и задание. ML1 не создаёт второй ingestion-корпус и не скачивает `raw_ref`/URL. ML2/backend ведут HTTP lifecycle и постоянное хранение результатов.

| Вход Go | R02 | Правило |
|---|---|---|
| article_id | document_id | Сохранить значение |
| article_version_id | document_version_id | Сохранить значение |
| body | normalized_text | Без trim, исправлений и обрезания |
| fetched_at | retrieved_at | Не подменять дату публикации |
| published_at отсутствует/null | None | Не угадывать дату |
| source | SourceMetadata | Сохранить provenance и config_version |
| raw_ref/content_hash | raw_ref/content_hash | Сохранить, не скачивать и не пересчитывать |

Запрос содержит `article`, `source`, `pipeline_version`, `trace_id`. Ключ идемпотентности — `article_version_id:pipeline_version`. Повтор использует прежний снимок; новая версия пайплайна означает отдельную обработку той же версии статьи.

Лимит — **131072 UTF-8 байта body без заголовка**. Go может настроить меньший предел; превышение отклоняется без обрезания. Пустой title и неизвестная дата допустимы.

`content_hash` принадлежит Go: обычный ingestion хеширует title + NUL + body + NUL + дату, банковский импорт — body. R02 проверяет формат и сохраняет значение. Хеш оригинала хранится отдельно.

## Extraction и корпус после #27

В `article.metadata` обязательны `extraction_method` (`full_article`, `rss_summary`, `pdf_extracted`) и `extraction_errors` (уникальные известные коды). Go передаёт сохранённый снимок extraction; технический parser остаётся внутренним. `PAGE_COUNT_MISMATCH` при нехватке страниц отображается в `MISSING_PAGES`. Неизвестное происхождение или неизвестные проблемы блокируют новую доставку.

`[]` означает отсутствие известных ошибок, а не доказанную полноту. Оригинал RSS может быть снимком ленты. Банковские PDF/TXT включены в стандартные article_versions/raw_artifacts; CSV доступны отдельно для построения профиля ML2 и не являются статьями.

Go реализует постановку сохранённых версий, экспорт payload и защиту неподтверждённых legacy-снимков. Уже отправленные старые запросы нельзя незаметно менять под прежним ключом. Инструкции после обновления backend — `docs/operations/ml-ingestion.md` в main.

## HTTP и библиотечная валидация

HTTP-схемы main и quality schema R02 имеют разный охват. HTTP допускает непрозрачные строковые ID/raw_ref и требует непустой body. R02 требует UUID, `raw-artifact:<UUID>` и принимает пустую строку для явного `EMPTY_TEXT`. Текущие Go fixtures удовлетворяют обоим входам. Вызвавший R02 код должен обработать `DocumentInputError`; прохождение HTTP-схемы само по себе не гарантирует прохождения R02.

## Полная операция

- QUEUED/RUNNING: обработка ещё не завершена.
- Quality PASS: продолжить индексацию; это не SUCCEEDED.
- Quality FAIL: может завершить полную операцию FAILED с безопасной причиной.
- SUCCEEDED: результаты сохранены, чанки/embeddings доступны, обработка событий завершена; `document_version_id = article_version_id`, `event_ids` может быть `[]`.

FAILED содержит структурированные code/message/retryable; терминальный отказ автоматически не создаёт новую операцию. Общую `ML_PIPELINE_VERSION` объявляет владелец полного ML-пайплайна. `ml1-quality-v1` — версия правил компонента, не версия готового сервиса. Отдельный quality-only HTTP не определён.

## Проверенная совместимость

25 октября 2026 проверены payload #27, head `0ff0b10`, вызовом `document_from_backend → check_document`:

| Пример | Статус | Warnings |
|---|---|---|
| HTML | PASS | — |
| RSS | PASS | RSS_SUMMARY |
| Bank PDF | PASS | UNKNOWN_PUBLICATION_DATE |
| Два synthetic запроса | PASS | EMPTY_TITLE, UNKNOWN_PUBLICATION_DATE, SOURCE_TRUST_UNRATED |

55 тестов ML1 прошли. Это ручная проверка пяти файлов, не постоянный интеграционный тест, не HTTP-прогон и не проверка всех девяти банковских документов.
