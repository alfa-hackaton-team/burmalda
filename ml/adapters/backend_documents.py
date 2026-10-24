"""Go payload -> canonical saved document; no fetching, parsing or text edits."""
from ml.ingestion.dto import (
    BackendInput, ExtractionMetadata, SavedDocumentVersion, SourceMetadata,
    canonical_source_type, canonical_trust_level,
)
from ml.ingestion.validation import DocumentInputError, validate


def document_from_backend(payload: BackendInput) -> SavedDocumentVersion:
    validate(payload, 'BackendInput')
    article, source = payload['article'], payload['source']
    if article['source_id'] != source['source_id']:
        raise DocumentInputError('Article/source identity mismatch')
    return SavedDocumentVersion(
        document_id=article['article_id'], document_version_id=article['article_version_id'],
        title=article['title'], normalized_text=article['body'],
        source=SourceMetadata(
            source_id=source['source_id'], name=source['name'],
            source_type=canonical_source_type(source['source_type']),
            source_role=source['source_role'],
            trust_level=canonical_trust_level(source['trust_level']),
            config_version=source['config_version'],
            backend_source_type=source['source_type'], backend_trust_level=source['trust_level'],
        ),
        published_at=article.get('published_at'), retrieved_at=article['fetched_at'],
        raw_ref=article['raw_ref'], content_hash=article['content_hash'],
        extraction=ExtractionMetadata(
            extraction_method=article['metadata']['extraction_method'],
            extraction_errors=tuple(article['metadata']['extraction_errors']),
        ),
        pipeline_version=payload['pipeline_version'], trace_id=payload['trace_id'],
        external_id=article.get('external_id'), canonical_url=article.get('canonical_url'),
        language=article.get('language'), author=article.get('author'),
    )
