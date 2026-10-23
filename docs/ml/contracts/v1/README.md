# Контракты ML v1

Файл contracts.schema.json содержит определения Draft 2020-12 в `$defs`.
Для SearchRequest валидируйте через `$ref: #/$defs/SearchRequest` с корнем этого файла. Остальные корневые типы: SearchResponse, GetChunksRequest, GetChunksResponse, ReadyVersionsRequest, ReadyVersionsResponse, AnalysisRequest, AnalysisResponse.

Включайте проверку format в выбранном валидаторе. Правила, требующие пары запроса/ответа, чтения БД и смысловой оценки, перечислены в contracts.md и не выражаются полностью JSON Schema.

Проверка примеров из папки `ml`:

```bash
python3 scripts/check_ml_contracts.py
```

Скрипт поддерживает только использованные в этой схеме ключевые слова; это не production-валидатор общего назначения. Изменение схемы требует проверки его поддержки либо использования полноценного валидатора.
