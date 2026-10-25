# ML2 event extraction boundary v1

This contract separates event extraction from PostgreSQL persistence. ML2 receives a saved document and its indexed chunks, then returns typed event candidates. The storage adapter owns event IDs, persistence, and stable replay behavior.

## Function boundary

```python
LLMEventExtractor.extract(
    request: ArticleProcessingInput,
    *,
    chunks: list[EventTextChunk] | None = None,
    timeout_seconds: float = 45.0,
) -> EventExtractionResult
```

After ML1 indexing, `Indexer.index()` returns `IndexedArticleVersion(document_version_id, chunks)` to `ArticleProcessingCoordinator`, which passes that same value to ML2. Each evidence quote must be an exact substring of a supplied chunk and is linked to every matching `chunk_id`. The standalone extractor accepts `chunks=None` only for fixture use; the coordinator rejects an empty chunk list, so persisted production evidence has real chunk IDs.

## Result DTO

```json
{
  "contract_version": "v1",
  "document_version_id": "00000000-0000-4000-8000-000000000001",
  "event_pipeline_config_version": "event-extraction-v1",
  "events": [
    {
      "event_type": "credit_terms_change",
      "title": "Обновление условий новых договоров продукта X",
      "summary": "С 1 ноября новые договоры продукта X заключаются по обновленным условиям.",
      "occurred_at": null,
      "geography": [],
      "status": "candidate",
      "confidence": null,
      "evidence": [
        {
          "chunk_id": "00000000-0000-4000-8000-000000000002",
          "quote_text": "С 1 ноября новые договоры продукта X заключаются по обновленным условиям.",
          "evidence_role": "support"
        }
      ]
    }
  ]
}
```

Extraction returns no database IDs. It does not mark events confirmed, merge events across documents, or assign calibrated confidence. A non-empty evidence list is required for every returned candidate. An empty `events` array is a valid result.

## Storage and retry behavior

- The idempotency key is `(document_version_id, event_pipeline_config_version)`. The event configuration version is separate from the article-processing `pipeline_version` and must be incremented when event extraction behavior changes.
- The first successfully persisted result for that key wins. A replay returns its stored event IDs without invoking the model again. If two workers race, the database uniqueness constraint selects one stored result; the losing result is discarded.
- Events from different document versions are not automatically merged. Cross-document deduplication requires a separate reviewed policy.
- Two candidates with the same normalized event type, title, occurrence time, and geography in one result are treated as a malformed duplicate response; extraction fails instead of silently dropping one.
- Persisted event links carry `evidence_role` and `is_primary`; this implementation does not infer a primary event, so `is_primary` remains false.

## Failure behavior

Malformed model output, unsupported claims, or evidence absent from the supplied chunks fail the extraction stage; no partial result is stored. Timeouts and gateway errors propagate to the article-processing coordinator, which records a failed operation. The event store writes the run, events, links, and stable IDs in one transaction.
