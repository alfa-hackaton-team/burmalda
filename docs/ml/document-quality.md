# R02: deterministic quality gate сохранённой версии

R02 реализован в `ml/ingestion/` и `ml/adapters/backend_documents.py`. Версия правил — **ml1-quality-v1**. Runtime не использует сеть, файловое хранилище, часы, модель или пересчёт хеша. R01 retrieval и ML2 DTO не изменены.

## Контракт и путь данных

```text
BackendInput (article + source + pipeline_version + trace_id)
  → document_from_backend(payload)
  → SavedDocumentVersion (неизменяемая canonical версия)
  → check_document(document)
  → QualityResult (PASS | FAIL, failures, warnings, версия правил)
```

Каноническая [JSON Schema quality v1](contracts/quality/v1/quality.schema.json) содержит BackendInput, BackendArticle/Source, ExtractionMetadata, SavedDocumentVersion и QualityResult. Вход HTTP ссылается на неё через [OpenAPI](../api/ml-openapi.yaml) и NormalizedArticle; дублирующих или legacy-схем R02 нет. Python-типы — [dto.py](../../ml/ingestion/dto.py); проверка структуры — [validation.py](../../ml/ingestion/validation.py).

Некорректные ID/даты/типы/категории, несовпадающие source ID, отсутствующие extraction metadata и неизвестные поля/коды вызывают DocumentInputError(code=INVALID_DOCUMENT_INPUT) до gate. Ошибка структуры не маскируется под плохое качество текста. Сообщения не раскрывают исходный текст. Canonical dataclass также проверяет структуру при прямом создании.

## Вход и provenance

Go владеет сохранением статьи, версии и оригинала. article_id → document_id, article_version_id → document_version_id с теми же UUID; body → normalized_text без trim или исправления; fetched_at → retrieved_at. Сохраняются title, точные даты, source ID/name/role/config_version, raw_ref, content_hash, canonical_url, external_id, language, author и pipeline_version/trace_id. Отсутствующая или null published_at становится None, а не датой загрузки. Остальные необязательные поля представлены явно как None в canonical объекте.

**Metadata целевого входа обязательна:**

```json
{
  "extraction_method": "pdf_extracted",
  "extraction_errors": ["LAYOUT_LOSS"]
}
```

Методы: full_article, rss_summary, pdf_extracted. extraction_errors — массив уникальных кодов из таблицы ниже, [] при отсутствии известных ошибок. Старые metadata={}, пропущенные поля, неизвестные способы/коды не принимаются. Новое задание пользователя утвердило целевой вариант R02; backend должен передавать его, поддержка старого несовместимого POST не добавлена. full_article обозначает способ получения, не доказанную полноту. Даже [] не доказывает сохранность всех таблиц и страниц.

raw_ref имеет вид raw-artifact:<UUID> и не загружается ML. Оригиналом RSS может быть снимок ленты. content_hash — 64 lowercase hex символа; ML сохраняет его как утверждение backend, не проверяет пересчётом и не меняет версионирование.

## Категории источников

Canonical source_type: news/bank_analytics/expert/regulator; source_role: external/bank/expert; trust_level: primary/trusted/standard/unknown. Целевой вход принимает эти значения и два явно поддержанных backend-обозначения:

| Backend | Canonical | Диагностика |
|---|---|---|
| financial_press | news | SOURCE_TYPE_MAPPED |
| reviewed | unknown | SOURCE_TRUST_UNRATED |

Это явная политика R02: операторская проверка не задаёт калиброванный уровень доверия. reviewed не становится trusted. Оригиналы значений сохраняются как backend_source_type/backend_trust_level. Произвольные строки, case folding и угадывание категорий не поддерживаются. Нормализованные категории уже пригодны для будущего Chunk, без изменения enum retrieval v1. Банковский источник не обходит quality rules.

## FAIL rules

Все сработавшие правила возвращаются в стабильном порядке, без раннего выхода.

| Код результата | Условие |
|---|---|
| EMPTY_TEXT | normalized_text пуст или состоит из Unicode-пробелов |
| INVALID_UTF8_TEXT | Python-строка не может быть закодирована в UTF-8; текст не исправляется |
| TEXT_TOO_LARGE | UTF-8 размер normalized_text больше **131072 байт**, без заголовка |
| NUL_IN_TEXT | В тексте есть U+0000 |
| EXTRACTION_TEXT_TRUNCATED | Backend код TEXT_TRUNCATED |
| EXTRACTION_MISSING_PAGES | Backend код MISSING_PAGES |
| EXTRACTION_MISSING_TABLES | Backend код MISSING_TABLES |
| EXTRACTION_MISSING_FOOTNOTES | Backend код MISSING_FOOTNOTES |
| EXTRACTION_FAILED | Backend код EXTRACTION_FAILED |

Лимит включительный: 131072 допустимо, 131073 — FAIL; это байты, не JSON maxLength. Пустая строка остаётся валидной по типу входа и явно получает EMPTY_TEXT. Полнота не угадывается по одному body: extraction failures основаны на переданных backend кодах.

## WARNING rules

| Код | Условие |
|---|---|
| EMPTY_TITLE | title пустой или пробельный; сам по себе не FAIL |
| UNKNOWN_PUBLICATION_DATE | published_at неизвестна; сам по себе не FAIL |
| RSS_SUMMARY | Способ получения rss_summary; текст не объявляется полной статьёй |
| SOURCE_TYPE_MAPPED | financial_press отображён в news |
| SOURCE_TRUST_UNRATED | reviewed отображён в unknown |
| EXTRACTION_LAYOUT_LOSS | Backend код LAYOUT_LOSS, известна потеря оформления |
| EXTRACTION_READING_ORDER_UNCERTAIN | Backend код READING_ORDER_UNCERTAIN |
| UNICODE_REPLACEMENT_CHARACTER | Текст содержит U+FFFD, без автоматического исправления |

Нет произвольного минимального размера, языкового score, similarity или отказа по слову «ошибка». Сам по себе метод pdf_extracted/full_article не даёт ни FAIL, ни гарантии полноты. Старая дата банка не решается quality gate как запрет или подтверждение актуальности.

## QualityResult и граница ответственности

Результат — frozen dataclass с JSON-представлением `to_dict()`:

- document_id, document_version_id, pipeline_version;
- quality_rules_version=ml1-quality-v1;
- status=PASS при отсутствии failures, иначе FAIL;
- failures/warnings: упорядоченные массивы объектов code/field/message;
- text_utf8_bytes: измеренный размер или null при невозможности UTF-8 кодирования.

QualityResult не содержит текст документа, timestamps вычисления или состояние общей операции. При одинаковых данных и версии правил результат воспроизводим; порядок входных extraction errors не меняет порядок диагностик. Изменение trace_id не меняет quality result, но trace сохранён в canonical документе для вызывающей стороны.

Входной словарь копируется в неизменяемые dataclass/tuple; изменение запроса или сериализованного ответа не меняет документ. Gate не присваивает ready/embedded/SUCCEEDED, не создаёт чанки и не удаляет доказательства. PASS означает прохождение **этих правил**, не истинность, полноту или готовность поиска.

Компонент stateless: хранение результата принадлежит вызывающему пайплайну; `to_dict()` уже готов для его записи/передачи. БД, журнал операций и HTTP-сервис не являются недоделанной частью R02 и сюда не добавлены. При полном пайплайне PASS позволяет продолжить обработку; quality FAIL может стать FAILED общей операции с безопасной ошибкой. Полный article-processing после одного PASS не считается SUCCEEDED.

## Corpus и проверка

[Фиксированный corpus](../../ml/tests/fixtures/corpus/manifest.json) содержит 8 синтетических backend payload: новость, RSS с reviewed/пустым title/неизвестной датой, банковский PDF-текст, PDF с потерянной таблицей, PDF с потерей layout/порядка, пробельный текст, NUL и replacement character. Все имеют provenance; manifest фиксирует ожидаемые status/failures/warnings. Это не реальный импорт банковских файлов и не benchmark retrieval.

[Тесты](../../ml/tests/test_document_quality.py) дополнительно генерируют граничные ASCII/кириллица/emoji payload, проверяют strict contract, сохранность ID/текста/хеша, source mapping, все extraction-коды, неизменяемость и отсутствие I/O. Команды и пример интеграции — [ml/README.md](../../ml/README.md).

## Отложено именно до R05 / других владельцев

- Размеры чанков, перекрытие, embeddings и индексы — R05+.
- Политика непустого title будущего Chunk; R02 хранит исходный title и предупреждение, не генерирует заголовок.
- Привязка диапазонов текста к страницам PDF и её представление в Chunk — требуется backend metadata и отдельное расширение контракта; R02 страниц не выдумывает.
- Вес trust_level в ранжировании — последующий поиск; преобразование reviewed в unknown уже завершено в R02.
- Полный HTTP lifecycle, запись результатов и глобальная ML_PIPELINE_VERSION — ML2/backend. ml1-quality-v1 идентифицирует готовые правила компонента, а не объявляет полный пайплайн работающим.
- Актуальность банковской аналитики и смысл CSV — продукт/ML2/владелец данных, не quality rules.
