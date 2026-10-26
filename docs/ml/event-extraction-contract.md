# ML2 event extraction boundary v1

This contract separates event extraction from PostgreSQL persistence. ML2 receives a saved document and its indexed chunks, then returns typed event candidates. The storage adapter owns event IDs, persistence, and stable replay behavior.

The 120-second default is an internal ML inference deadline for extraction plus semantic verification. It is not an HTTP contract guarantee; callers can pass a shorter explicit deadline.

`occurred_at` must be an ISO 8601 timestamp with an explicit timezone or offset, and only when the source states that instant clearly. A date without a time, or a time without a timezone, maps to `null`; extraction never guesses a timezone or invents midnight.

## Function boundary

```python
LLMEventExtractor.extract(
    request: ArticleProcessingInput,
    *,
    chunks: list[EventTextChunk] | None = None,
    timeout_seconds: float = 120.0,
) -> EventExtractionResult
```

ML1 owns canonical document validation and indexing. After indexing, its adapter returns `IndexedArticleVersion(document_version_id, chunks)` to `ArticleProcessingCoordinator`; each chunk contains a stable UUID `chunk_id` and its indexed `text`. The `document_version_id` must equal the submitted `article_version_id`. Repeating the same operation must return the same chunk IDs and text, so persisted evidence continues to point at the same indexed content. ML2 consumes those chunks directly and does not fetch Go tables, re-index the article, or persist a second copy of the document. Each evidence quote must be an exact substring of a supplied chunk and is linked to every matching `chunk_id`. The standalone extractor accepts `chunks=None` only for fixture use; the coordinator rejects an empty chunk list, so persisted production evidence has real chunk IDs. Before saving, the storage boundary parses the DTO again and checks document/config versions, candidate shape, and every evidence chunk ID and quote against the indexed result.

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

- The HTTP operation is idempotent by `(article_version_id, pipeline_version)`; Go sends this as `Idempotency-Key: article_version_id:pipeline_version`. ML stores the event result's `event_pipeline_config_version` on that operation for provenance. An operation may have one event result. To intentionally process the same document version with a changed pipeline, use a new supported `pipeline_version`, which produces a new Go idempotency key and ML operation.
- The operation row stores the stable `event_ids`, including `[]` for a successful extraction with no candidates. The event rows, evidence links, event configuration version, and IDs are committed in one transaction. A replay returns those IDs without invoking the model again. A configuration mismatch against an already saved result fails closed instead of overwriting it.
- Events from different document versions are not automatically merged. Cross-document deduplication requires a separate reviewed policy.
- Two candidates with the same normalized event type, title, occurrence time, and geography in one result are treated as a malformed duplicate response; extraction fails instead of silently dropping one.
- Persisted event links carry `evidence_role` and `is_primary`; this implementation does not infer a primary event, so `is_primary` remains false.

## Failure behavior

Malformed model output, unsupported claims, an invalid DTO, or evidence absent from the supplied chunks fail the extraction stage; no partial result is stored. Timeouts and gateway errors propagate to the article-processing coordinator, which records a failed operation. The event store writes events, links, and stable IDs to the operation in one transaction.
