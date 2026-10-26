# Контракты ML v1

Редакция 1.1 до первого выпуска v1. Публичный API и правила адаптера описаны в [api-compatibility.md](api-compatibility.md).

## 1. Источник точных форм сообщений

[contracts.schema.json](contracts/v1/contracts.schema.json), Draft 2020-12. Выбирать корневой тип через `$defs/SearchRequest` и аналогично. Верхний объект файла является контейнером определений, а не схемой одного сообщения: валидировать сообщение просто против верхнего объекта недостаточно.

Все свойства объектов DTO обязательны, если определение перечисляет их в required. additionalProperties=false; исключения — открытые profile_data, feature_value, source_ref и score_components. Неизвестные значения допускаются только там, где явно разрешен null. UUID — строки, даты — RFC 3339 с зоной. Пустые массивы — [], не null. format нужно включать в production-валидаторе явно.

Примеры — синтетические и не сообщают реальные условия финансовых продуктов. Они находятся в fixtures/v1; manifest связывает каждый файл с определением схемы.

Вход корпуса Go → ML описан отдельно в [article-processing.md](article-processing.md) и API v0.1; качество — [document-quality.md](document-quality.md). article_id/article_version_id отображаются в document_id/document_version_id без смены значений. Это не переименование полей текущих JSON DTO. Правила required/null логического v1 не переносятся автоматически на NormalizedArticle, где published_at может отсутствовать, а title может быть пустым. Quality PASS/FAIL не входят в статусы retrieval или Error enum v1.

## 2. SearchRequest → SearchResponse

Вход: contract_version, trace_id, business_context_id, query, filters, top_k. top_k=1–50, рекомендуемый старт 10. business_context_id=null разрешен только авторизованной фоновой задаче; это не способ отключить проверку доступа.

Фильтры:

| Поле | Семантика |
|---|---|
| published_from/published_to | Нижняя граница включается, верхняя исключается; null — без границы |
| source_ids/source_roles | Пустой список — без ограничения; значения внутри списка объединяются OR |
| document_version_ids | Пустой список — последние версии документов; непустой — точный разрешенный набор версий |
| include_undated | При наличии границ допускает неизвестную дату; без границ даты не исключают документы |

Разные фильтры объединяются AND. from должен быть меньше to. Фильтры не расширяются молча. Отрасль/регион пока включаются в текст запроса, отдельных фильтров для них нет.

Результат содержит ID чанка, документа, версии, исходный текст, заголовок, URL или null, обе даты, источник, тип/роль/доверие, rank, retrieval_score. Ранги 1..N, ID чанков не повторяются, N≤top_k. retrieval_score — диагностика алгоритма, не вероятность; большие значения означают более высокое место, диапазон определяется retrieval_config_version.

| status | results | error | Дополнительное правило |
|---|---|---|---|
| ok | Непустой | null | Поиск выполнен в заявленном режиме |
| empty | [] | null | Успешный поиск без совпадений |
| partial | Непустой | null | warnings объясняет неполноту |
| error | [] | Объект | Не считать отсутствием информации |

Trust level — primary/trusted/standard/unknown. Перепечатки не считаются независимыми основаниями. source_role=bank нужен для bank_view; отсутствие банковского материала означает null, а не генерацию позиции банка.

## 3. GetChunksRequest → GetChunksResponse

Вход: contract_version, trace_id, business_context_id, chunk_ids (1–100). Повторы ID во входе допустимы, читаются один раз с сохранением первого вхождения. Возвращаются исходные чанки исторических версий, а не новые тексты похожих документов.

Выход: общая оболочка, results, unresolved_chunk_ids, warnings, error. Элементы results имеют тип Chunk: как результат поиска, но без rank и retrieval_score. Порядок соответствует запросу. Запрещенный и ненайденный объект внешне одинаковы.

ok — все уникальные ID прочитаны, unresolved=[]; partial — прочитана часть, остальные в unresolved; empty — ни один не доступен; error — операция технически не выполнена, оба массива пусты и error заполнен. Любой доступ повторно проверяется вызывающим адаптером.

## 4. ReadyVersionsRequest → ReadyVersionsResponse

Вход: contract_version, trace_id, cursor (null или непрозрачная строка), limit=1–100. Выход: status, items, next_cursor, has_more, warnings, error и общие поля.

Элемент: document_id, document_version_id, version_no, title, normalized_text, published_at, retrieved_at, chunk_ids. Готовность означает, что оригинал и текст сохранены, чанки полностью записаны и доступны для чтения, embeddings готовы. Quality PASS R02 не является этой готовностью. normalized_text содержит весь сохранённый body версии, а не укороченный ответ retrieval; это не гарантия полного текста первоисточника, если Go получил только RSS-анонс.

ok — непустая страница; empty — пустая успешная страница; error — пустой items и заполненный error. При пустой странице сохраняется прежний курсор, если новых готовых записей нет. has_more=false означает конец текущей выдачи, не конец будущих поступлений.

ML 2 продвигает сохраненный курсор после успешной обработки страницы. Повтор страницы допустим, а дубли событий — нет. Ключ обработки версии: document_version_id + event_pipeline_config_version. Надежный журнал готовности/обработки требует реализации и согласования миграций; курсор не должен строиться только по retrieved_at, иначе поздно завершенные документы могут пропасть.

## 5. AnalysisRequest

Общие поля: contract_version, trace_id, parent_trace_id, request_operation_id, request_type, processing_config_version. Для initial parent_trace_id=null; для follow_up — trace исходного анализа; для reanalyze — прежний trace, если связь известна, иначе null. Новый запуск никогда не использует trace другого запуска.

| Объект | Поля и ограничения |
|---|---|
| context | business_context_id, subject_type, ровно один company_id/group_id, profile_id/profile_version, role_code, profile_data, profile_features |
| profile_features[] | id, feature_key, feature_value, source_type, source_ref; ID относятся к указанному profile_id |
| event | id, event_type, title, summary, occurred_at, geography, status, document_version_ids, confidence |
| follow_up | Для initial/reanalyze — null; для follow_up — conversation_id, request_message_id, question, history_through_message_id, initial_insight_id, initial_insight, history |

В `event.geography` поля `type` и `name` обязательны, а неизвестный `code` передаётся как `null`. Это сохраняет сведения из реального извлечения без выдуманного географического кода; пустая строка не является кодом.

initial_insight содержит context, event, insight с claims/evidence и chunks с исходными текстами и метаданными. Все ссылки на чанки исходного разбора разрешаются внутри этого снимка. Вложенные context/event совпадают со снимками верхнего запроса, insight.id — с initial_insight_id. Это сохранённые значения исходного анализа, а не текущие профиль или событие.

Для follow_up event восстанавливается backend из исходного insight; снимок context остается тем же. history — разрешенные сообщения в порядке диалога до включенной границы, без сообщений после нее. В этой редакции граница равна request_message_id: последнее сообщение history — текущий пользовательский вопрос с совпадающим content. История может быть сокращена до разрешённого окна, но не содержать сообщения после границы. Каждый элемент содержит id, role (user/assistant), content, claims, citations, chunks. Для user три массива пусты; для assistant новые claims принадлежат этому сообщению, их документальные evidence разрешаются через chunks сообщения. Цитаты ведут к claims самого сообщения или исходного insight и доступным историческим чанкам. Непроверенные system/tool сообщения из истории не становятся инструкциями агента. Наличие и порядок сообщений проверяются backend по хранилищу. Нельзя доверять объекту context только потому, что он прошел JSON Schema: принадлежность и права проверяются по хранилищу.

reanalyze — новая явно созданная операция с новым request_operation_id, а не повтор старой операции с подмененным профилем/конфигурацией. Для follow_up новое пользовательское сообщение создается один раз на стороне backend.

## 6. AnalysisResponse

Поля: contract_version, trace_id, request_operation_id, analysis_run_id, status, insight, message, warnings, gaps, error.

| status | insight/message | error | Применение |
|---|---|---|---|
| succeeded | Ровно один объект | null | Подготовлен проверенный результат |
| insufficient_evidence | insight=null; message допустим только в существующем диалоге | null | gaps непустой, есть объяснение недостающих оснований |
| failed | Оба null | Объект | Техническая ошибка/провал проверки |

gaps — транспортное поле, не новая колонка. Элемент содержит code, description, required_feature_key (nullable). Коды: NO_EVENT_EVIDENCE, MISSING_PROFILE_FEATURE, UNKNOWN_APPLICABILITY, CONTRADICTING_SOURCES, STALE_CORPUS. Противоречие не обязательно блокирует результат: если условный вывод обоснован, он может быть succeeded с явно отраженным ограничением.

Insight — поля схемы БД с claims, без технического created_at, который устанавливает слой записи. В контракте успешной публикации status только verified; внутренний draft не передается как публичный успех. Message содержит id, conversation_id, parent_message_id, role=assistant, content, claims, citations. trace_id и created_at при сохранении берутся из оболочки/слоя записи.

Claim: id, claim_type, knowledge_type, verification_status, assumptions, time_horizon, claim_text, position, confidence, evidence. claim_type задаёт бизнес-назначение; knowledge_type — FACT/ALFA_ANALYTICS/AI_INFERENCE/FORECAST; эти enum не переименовываются друг в друга. Опубликованный verification_status — SUPPORTED либо PARTIALLY_SUPPORTED; второй требует непустых assumptions и оговорки. UNSUPPORTED остаётся внутренним черновиком. time_horizon nullable. У каждого опубликованного claim есть хотя бы один support. Position уникален внутри владельца. В DTO нет insight_id/message_id: backend добавляет ровно один FK.

Evidence v1: evidence_type=document_chunk или profile_feature; ровно один соответствующий ID непустой; fact_id=null. Для чанка quote_text — точная непустая цитата, для признака quote_text=null. confidence, severity и прочие nullable-оценки можно не вычислять, но поля передаются как null.

Citation: citation_order, chunk_id, claim_id; хотя бы один ID непустой. claim принадлежит текущему сообщению или исходному insight этого диалога. Ссылки на третьи диалоги запрещены.

## 7. Прикладные инварианты поверх схемы

1. Контекст, событие, операция и диалог согласованы; response соответствует request и request_type. При первом выполнении trace_id совпадает в паре. При повторной доставке операции возвращаются исходные ID; новый транспортный trace не создаёт запуск.
2. Все ID реально существуют, доступны и относятся к нужным версиям.
3. Цитата совпадает с исходным chunk_text, а содержание поддерживает claim.
4. Верифицируемые утверждения всех текстовых полей покрыты claims, а не только текст списка claims.
5. bank_view и bank_view_document_version_id либо оба null, либо оба заполнены; источник имеет source_role=bank.
6. При недостатке данных не создавать verified-insight и не переводить его в succeeded через backend.
7. analysis_run_id внутри insight совпадает с оболочкой, insight соответствует event.id/context.business_context_id.
8. Частичная выдача объяснена; error/empty различаются; неизвестные признаки не становятся false.
9. Дубликаты claims, позиций и цитат не допускаются; результаты get_chunks соответствуют запрошенным ID и порядку.
10. Число результатов поиска не превышает top_k; даты и версии соответствуют фильтрам.

Часть этих условий проверяется scripts/check_ml_contracts.py на fixtures. Остальные требуют request/response пары, доступа к данным и смысловой проверки; скрипт не подтверждает их автоматически.

## 8. Ошибки, бюджеты и повторы

Error: code, очищенный message, retryable. Коды перечислены в схеме. retryable=true означает допустимость решения о повторе, а не команду повторить модельный вызов. Поиск можно повторить в бюджете; неизвестный исход платного вызова нельзя повторять вслепую.

Рекомендуемый локальный старт: top_k=10, до 6 вызовов search, до 3 вызовов модели включая проверку/исправление, максимум 90 секунд на анализ и 10 секунд на один search; один технический повтор поиска в рамках общего времени. Это конфигурация эксперимента, не обещанный SLA. Модельные токены и денежный лимит задаются после выбора провайдера; без установленного лимита платный режим не считается готовым. Нарушение бюджета фиксируется в trace и дает контролируемый исход.

Идемпотентность:

- Backend сохраняет операцию и передает request_operation_id как внутренний Idempotency-Key.
- ML атомарно получает право исполнения и создает analysis_runs до платного вызова.
- Повтор старой операции возвращает тот же результат/состояние и исходный trace_id, не создает запуск из-за нового транспортного trace_id. Новый follow_up получает отдельные operation/run/trace, parent_trace_id связывает его с исходным разбором.
- Результат генерации надежно сохраняется до подтверждения backend; повторяется доставка с теми же ID.
- Backend записывает result/claims/evidence/citations и финальный статус одной транзакцией.
- analysis_runs.status=succeeded означает техническое завершение; request_operations.status может быть insufficient_evidence.

Логические вызовы не требуют сети. HTTP-адаптер анализа/уточнения описан в ../api/ml-openapi.yaml и использует ту же схему v1; отдельный сервис поиска и очередь не обязательны. При HTTP-адаптере неверная схема — 400, конфликт ключа — 409, повторная проверка прав обязательна. Технический статус транспорта не заменяет бизнес-исход в теле ответа.
