# ML: R01 retrieval для ML2

Реализован синхронный логический контракт `Retriever.search` / `Retriever.get_chunks` на Python 3.10+. Это fixture/in-memory реализация для интеграции ML2. Канонические DTO и семантика — [JSON Schema v1](../docs/ml/contracts/v1/contracts.schema.json) и [contracts.md](../docs/ml/contracts.md). Существующая схема не изменена.

## Запуск

Из корня репозитория:

```bash
python3 -m venv .venv
.venv/bin/python -m pip install -r ml/requirements.txt
.venv/bin/python -m unittest discover -s ml/tests -v
```

Для существующих API-проверок дополнительно установить `docs/ml/requirements-validation.txt` в это же окружение. Сборка wheel/отдельный деплой не входят в R01: код запускается из checkout с папками `ml/` и `docs/`. Схема читается при импорте, fixtures — при создании адаптера; во время поиска сетевых запросов и чтения корпуса с диска нет.

## Пример использования ML2

Выполнить из корня репозитория с Python окружения выше:

```python
import json
from pathlib import Path

from ml.adapters.in_memory import InMemoryRetriever
from ml.retrieval.interfaces import Retriever
from ml.shared.validation import ContractValidationError

retriever: Retriever = InMemoryRetriever.from_fixtures()
request = json.loads(Path('docs/ml/fixtures/v1/search.request.json').read_text())

try:
    response = retriever.search(request)
except ContractValidationError as error:
    # Ошибка DTO до исполнения; code = INVALID_REQUEST / UNSUPPORTED_VERSION.
    print(error.code, str(error))
else:
    if response['status'] in ('ok', 'partial'):
        chunks = retriever.get_chunks({
            'contract_version': 'v1',
            'trace_id': request['trace_id'],
            'business_context_id': request['business_context_id'],
            'chunk_ids': [item['chunk_id'] for item in response['results']],
        })
        print(response['status'], chunks['status'], chunks['results'][0]['chunk_text'])
    elif response['status'] == 'empty':
        print('Нет совпадений в настроенном fixture-сценарии', response['warnings'])
    else:
        print(response['error']['code'], response['error']['retryable'])
```

`from_fixtures()` разрешает только business_context_id из `search.request.json`, загружает исходные чанки и один запрос из `search.ok.json`. Использовать собственный trace_id каждого вызова можно. Это не HTTP API, авторизация пользователя или реальный семантический поиск. Для агента, меняющего формулировки запросов, явно добавить необходимые запросы в конфигурацию; неизвестный запрос возвращает `empty`.

ML2 зависит от `Retriever`, принимает/возвращает JSON-совместимые dict с TypedDict-подсказками из `ml.shared.dto`. При появлении реального адаптера меняется создание retriever; структура вызовов и wire DTO сохраняется.

## Настройка in-memory сценариев

```python
from ml.adapters.in_memory import Candidate, DocumentVersion, QueryFixture

# chunk — полный Chunk по канонической схеме, без rank/retrieval_score.
chunk = json.loads(Path('docs/ml/fixtures/v1/chunks.ok.json').read_text())['results'][0]
context_id = request['business_context_id']

retriever = InMemoryRetriever(
    chunks=[chunk],
    versions=[DocumentVersion(chunk['document_id'], chunk['document_version_id'], version_no=1)],
    queries={
        'точный запрос': QueryFixture((Candidate(chunk['chunk_id'], 0.82),)),
        'неполный сценарий': QueryFixture(
            (Candidate(chunk['chunk_id'], None),),
            incomplete=True,
            warnings=('Часть fixture-источников недоступна',),
        ),
    },
    context_chunks={context_id: [chunk['chunk_id']]},
    retrieval_config_version='r01-custom-fixture-v1',
)
```

`DocumentVersion`, `Candidate`, `QueryFixture` — внутренняя конфигурация тестового адаптера, не новые поля DTO. `version_no` задаёт порядок версий, `ready` — готовность тестового набора. Это не решение о статусах embedded/analyzed. Новую неготовую версию можно задать без чанков, чтобы проверить отсутствие подстановки старой.

`queries` сопоставляет точную строку запроса после удаления крайних пробелов с заранее заданными кандидатами и score. Нет токенизации, embeddings, similarity или ранжирующей модели. Изменение сценария/оценок требует осмысленной смены retrieval_config_version. Неизвестная строка означает пустую fixture-выдачу, а не доказанное отсутствие сведений в реальном корпусе.

Сценарий `partial` задаётся `incomplete=True` с непустыми warnings. Фильтры и доступ применяются даже в таком сценарии. Если результатов после фильтрации нет, возвращается `empty` с предупреждениями: контракт не допускает пустой `partial`.

Для контролируемого технического сбоя при создании адаптера передать, например:

```python
failures={
    'search': {'code': 'TIMEOUT', 'message': 'fixture timeout', 'retryable': True},
    'get_chunks': {'code': 'SEARCH_UNAVAILABLE', 'message': 'fixture outage', 'retryable': True},
}
```

Это конфигурация конструктора `InMemoryRetriever`, а не аргумент метода и не реальный таймер. Внешнее сообщение ошибки заменяется безопасным общим текстом; code/retryable сохраняются. Автоматических повторов нет.

## Поведение контракта

- `search`: авторизация по настроенному контексту; точные кандидаты запроса; последние версии по version_no либо явно перечисленные исторические; готовность; AND между фильтрами и OR внутри списков; даты `[from,to)`; include_undated; сортировка score по убыванию, null после известных оценок, tie-break по chunk_id; top_k и ранги 1..N.
- `get_chunks`: уникальные ID в порядке первого появления, точное чтение исторического Chunk независимо от готовности новой версии, разрешённые результаты и unresolved в порядке запроса. Ни похожий текст, ни обновлённая версия вместо исходного ID не подставляются.
- Все поля provenance сохраняются: document/version/source ID, исходный текст, заголовок, URL/null, даты, тип/роль/доверие источника. Score не становится confidence.
- `ok` — успешная непустая выдача; `empty` — успешная пустая; `partial` — непустая неполная; `error` — техническая/контекстная ошибка с пустыми результатами. Для get_chunks при error unresolved тоже пуст.
- Доступ задаётся доверенным приложением через context_chunks. Пустой allowlist разрешает вызов, но не чтение объектов. Неизвестный контекст отклоняется CONTEXT_MISMATCH. Запрещённые и отсутствующие ID не различаются в unresolved.
- `business_context_id=null` по умолчанию отклоняется. Для уже аутентифицированного сервисного процесса используется отдельный адаптер с `service_chunk_ids=[...]`; это доверенная настройка, её нельзя принимать из клиентского JSON. Производственная проверка прав по БД остаётся backend-адаптеру.
- Некорректный DTO, включая неверный UUID, лишние/отсутствующие поля, query из пробелов и неверные даты, вызывает ContractValidationError до выполнения. Данные не приводятся к другим типам. При неподдерживаемой версии code=UNSUPPORTED_VERSION. Исключение не содержит текста запроса или документа. Транспортное отображение должен выполнить будущий HTTP-слой ML2.

Выход каждого метода проверяется канонической Draft 2020-12 с format и прикладными инвариантами пары запрос/ответ. Конфигурация и ответы копируются, изменение полученного dict не изменяет хранимые доказательства.

Диагностика ответа: trace_id, status, warnings/error, retrieval_config_version у search. Logger `ml.adapters.in_memory` на INFO выдаёт структурированные поля operation, trace_id, status, config, duration_ms, result_count, unresolved_count, warning_count, error_code. Обработчики логов настраивает вызывающее приложение. Тексты документов, query, секреты и профили в эти события не включаются.

## Границы R01

Нет PostgreSQL, S3, pgvector, embeddings, BM25, reranker, ingestion, LLM, HTTP/Docker, фоновой доставки и `ReadyDocuments.list`. Нет оценки качества реального поиска или production-авторизации.

# R02: quality gate перед индексацией

Реализован отдельный deterministic компонент. Вход — целевой BackendInput из [quality schema](../docs/ml/contracts/quality/v1/quality.schema.json); документация и полный список правил — [document-quality.md](../docs/ml/document-quality.md). Зависимости те же, что у R01. Все тесты запускаются прежней командой `python -m unittest discover -s ml/tests -v`.

```python
import json
from pathlib import Path

from ml.adapters.backend_documents import document_from_backend
from ml.ingestion.quality import check_document

payload = json.loads(Path('ml/tests/fixtures/corpus/news.json').read_text())
document = document_from_backend(payload)
result = check_document(document)
print(json.dumps(result.to_dict(), ensure_ascii=False, indent=2))
if result.status == 'PASS':
    # document — неизменяемые текст/ID/provenance для будущего R05.
    # Здесь индексирование не выполняется, общий pipeline не завершён.
    assert not result.failures
```

`document_from_backend` валидирует строгий целевой вход и равенство source ID. Отсутствующие/неверные поля, старый metadata={}, неизвестные категории и extraction errors вызывают `ml.ingestion.validation.DocumentInputError` до gate. Проверка качества возвращает FAIL для дефектного текста; это отдельный результат, не ошибка DTO и не исключение сети/модели.

Обязательны `article.metadata.extraction_method` (full_article/rss_summary/pdf_extracted) и `extraction_errors` (массив известных кодов, [] при отсутствии). Вход не чинится: title/body/dates/hash/raw_ref сохраняются. financial_press → news; reviewed → unknown с предупреждением и исходным backend_trust_level, без повышения до trusted. Неизвестная published_at остаётся None.

QualityResult содержит ID, pipeline_version, quality_rules_version=ml1-quality-v1, PASS/FAIL, failures/warnings с code/field/message и размер UTF-8. FAIL: пустой/пробельный текст, >131072 UTF-8 байта, NUL, невалидный UTF-8, известная потеря текста/страниц/таблиц/сносок или ошибка extraction. WARNING: пустой title, неизвестная дата, RSS-анонс, отображение источника/неоценённое доверие, layout/порядок чтения, U+FFFD. Восемь [corpus fixtures](tests/fixtures/corpus/manifest.json) фиксируют ожидаемые решения; тесты проверяют также байтовые границы и provenance.

Результат возвращается вызывающему коду; постоянное хранение — его ответственность. Gate не скачивает raw_ref, не пересчитывает content_hash, не меняет версии, не создаёт ready/SUCCEEDED, чанки, embeddings, поиск или HTTP. PASS — разрешение продолжить обработку по данным правилам, не гарантия полноты/истинности. До R05 остаются разбиение, title политика Chunk и page mapping PDF; правила и результат R02 уже определены.
