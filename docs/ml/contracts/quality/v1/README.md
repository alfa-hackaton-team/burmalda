# Контракт качества документов R02 v1

Каноническая схема — [quality.schema.json](quality.schema.json), Draft 2020-12. Валидировать конкретный `$defs`: BackendInput, BackendArticle, BackendSource, ExtractionMetadata, SourceMetadata, SavedDocumentVersion, QualityFailure, QualityWarning, QualityResult. Верхний контейнер сам по себе не проверяет сообщение.

Runtime использует эту же схему с FormatChecker. TypedDict wire input и immutable canonical DTO находятся в ml/ingestion/dto.py; [полная спецификация правил](../../../document-quality.md) фиксирует ml1-quality-v1. Правила приёма корпуса отделены от неизменённого contracts/v1/contracts.schema.json retrieval/анализа.

Extraction metadata обязательны; старые metadata={} не поддерживаются. Неизвестный extraction error — ошибка контракта, не тихое предупреждение. reviewed → unknown с сохранением оригинала и warning; financial_press → news. Пустой/слишком большой текст принимается как строка на границе компонента и получает quality FAIL; пустой title и неизвестная дата допустимы. UTF-8 лимит и равенство source ID — прикладные проверки.

QualityResult.to_dict() проходит собственную схему; PASS не допускает failures, FAIL требует их. Backend/ML2 отображают его в lifecycle полной операции отдельно. Во входе нет fallback-полей, частей PDF, новых retrieval полей или хранения результата.

Из корня checkout с зависимостями ml/requirements.txt и docs/ml/requirements-validation.txt:

```bash
python -m unittest discover -s ml/tests -v
python docs/ml/scripts/check_ml_contracts.py --full
python docs/ml/scripts/check_api_contracts.py
```

Существующая API-проверка теперь разрешает quality schema, проверяет целевой article-processing request и отклоняет старые metadata={}; corpus проходит через реальный адаптер и gate в тестах R02. Синтетические pipeline версии примеров не означают поддерживаемую версию полного HTTP-сервиса.
