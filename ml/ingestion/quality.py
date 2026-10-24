"""Deterministic R02 gate. No clocks, I/O, text repair or pipeline transitions."""
from ml.ingestion.dto import (
    ExtractionErrorCode, FailureCode, QualityIssue, QualityResult,
    SavedDocumentVersion, WarningCode,
)

MAX_TEXT_BYTES = 131072
QUALITY_RULES_VERSION = 'ml1-quality-v1'

# Stable rule order; backend list order cannot change the order of diagnostics.
EXTRACTION_FAILURES: tuple[tuple[ExtractionErrorCode, FailureCode, str], ...] = (
    ('TEXT_TRUNCATED', 'EXTRACTION_TEXT_TRUNCATED', 'Backend reports truncated text.'),
    ('MISSING_PAGES', 'EXTRACTION_MISSING_PAGES', 'Backend reports missing pages.'),
    ('MISSING_TABLES', 'EXTRACTION_MISSING_TABLES', 'Backend reports missing tables.'),
    ('MISSING_FOOTNOTES', 'EXTRACTION_MISSING_FOOTNOTES', 'Backend reports missing footnotes.'),
    ('EXTRACTION_FAILED', 'EXTRACTION_FAILED', 'Backend reports failed extraction.'),
)
EXTRACTION_WARNINGS: tuple[tuple[ExtractionErrorCode, WarningCode, str], ...] = (
    ('LAYOUT_LOSS', 'EXTRACTION_LAYOUT_LOSS', 'Backend reports loss of layout.'),
    ('READING_ORDER_UNCERTAIN', 'EXTRACTION_READING_ORDER_UNCERTAIN', 'Backend reports uncertain reading order.'),
)


def check_document(document: SavedDocumentVersion) -> QualityResult:
    if not isinstance(document, SavedDocumentVersion):
        raise TypeError('Expected SavedDocumentVersion; adapt the backend payload first')
    failures: list[QualityIssue] = []
    warnings: list[QualityIssue] = []
    text = document.normalized_text
    if not text.strip():
        failures.append(QualityIssue('EMPTY_TEXT', 'normalized_text', 'Saved text is empty or whitespace-only.'))
    try:
        size = len(text.encode('utf-8'))
    except UnicodeEncodeError:
        size = None
        failures.append(QualityIssue('INVALID_UTF8_TEXT', 'normalized_text', 'Saved text is not valid UTF-8.'))
    if size is not None and size > MAX_TEXT_BYTES:
        failures.append(QualityIssue('TEXT_TOO_LARGE', 'normalized_text', 'Saved text exceeds 131072 UTF-8 bytes.'))
    if '\x00' in text:
        failures.append(QualityIssue('NUL_IN_TEXT', 'normalized_text', 'Saved text contains a NUL character.'))
    errors = document.extraction.extraction_errors
    for backend_code, code, message in EXTRACTION_FAILURES:
        if backend_code in errors:
            failures.append(QualityIssue(code, 'extraction.extraction_errors', message))
    if not document.title.strip():
        warnings.append(QualityIssue('EMPTY_TITLE', 'title', 'Title is empty; indexing title policy applies at R05.'))
    if document.published_at is None:
        warnings.append(QualityIssue('UNKNOWN_PUBLICATION_DATE', 'published_at', 'Publication date is unknown.'))
    if document.extraction.extraction_method == 'rss_summary':
        warnings.append(QualityIssue('RSS_SUMMARY', 'extraction.extraction_method', 'Saved text was obtained as an RSS summary.'))
    if document.source.backend_source_type != document.source.source_type:
        warnings.append(QualityIssue('SOURCE_TYPE_MAPPED', 'source.source_type', 'Backend source category was mapped to the canonical category.'))
    if document.source.backend_trust_level == 'reviewed':
        warnings.append(QualityIssue('SOURCE_TRUST_UNRATED', 'source.trust_level', 'Operator review supplies no trust rating; canonical trust is unknown.'))
    for backend_code, code, message in EXTRACTION_WARNINGS:
        if backend_code in errors:
            warnings.append(QualityIssue(code, 'extraction.extraction_errors', message))
    if '\ufffd' in text:
        warnings.append(QualityIssue('UNICODE_REPLACEMENT_CHARACTER', 'normalized_text', 'Saved text contains a Unicode replacement character.'))
    return QualityResult(
        document_id=document.document_id, document_version_id=document.document_version_id,
        pipeline_version=document.pipeline_version, quality_rules_version=QUALITY_RULES_VERSION,
        status='FAIL' if failures else 'PASS', failures=tuple(failures), warnings=tuple(warnings),
        text_utf8_bytes=size,
    )
