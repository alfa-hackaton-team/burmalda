"""Typed backend wire input and immutable canonical R02 document/result."""
from dataclasses import asdict, dataclass
from typing import Literal, TypedDict

from ml.ingestion.validation import DocumentInputError, validate

SourceType = Literal['news', 'bank_analytics', 'expert', 'regulator']
SourceRole = Literal['external', 'bank', 'expert']
TrustLevel = Literal['primary', 'trusted', 'standard', 'unknown']
ExtractionMethod = Literal['full_article', 'rss_summary', 'pdf_extracted']
ExtractionErrorCode = Literal[
    'TEXT_TRUNCATED', 'MISSING_PAGES', 'MISSING_TABLES', 'MISSING_FOOTNOTES',
    'EXTRACTION_FAILED', 'LAYOUT_LOSS', 'READING_ORDER_UNCERTAIN',
]
FailureCode = Literal[
    'EMPTY_TEXT', 'TEXT_TOO_LARGE', 'INVALID_UTF8_TEXT', 'NUL_IN_TEXT',
    'EXTRACTION_TEXT_TRUNCATED', 'EXTRACTION_MISSING_PAGES',
    'EXTRACTION_MISSING_TABLES', 'EXTRACTION_MISSING_FOOTNOTES', 'EXTRACTION_FAILED',
]
WarningCode = Literal[
    'EMPTY_TITLE', 'UNKNOWN_PUBLICATION_DATE', 'RSS_SUMMARY', 'SOURCE_TYPE_MAPPED',
    'SOURCE_TRUST_UNRATED', 'EXTRACTION_LAYOUT_LOSS',
    'EXTRACTION_READING_ORDER_UNCERTAIN', 'UNICODE_REPLACEMENT_CHARACTER',
]


class BackendExtraction(TypedDict):
    extraction_method: ExtractionMethod
    extraction_errors: list[ExtractionErrorCode]


class BackendSource(TypedDict):
    source_id: str
    name: str
    source_type: SourceType | Literal['financial_press']
    source_role: SourceRole
    trust_level: TrustLevel | Literal['reviewed']
    config_version: str


class _OptionalArticleFields(TypedDict, total=False):
    published_at: str | None
    external_id: str
    canonical_url: str
    language: str
    author: str


class BackendArticle(_OptionalArticleFields):
    article_id: str
    version_no: int
    article_version_id: str
    source_id: str
    title: str
    body: str
    fetched_at: str
    raw_ref: str
    content_hash: str
    metadata: BackendExtraction


class BackendInput(TypedDict):
    article: BackendArticle
    source: BackendSource
    pipeline_version: str
    trace_id: str


def canonical_source_type(value: SourceType | Literal['financial_press']) -> SourceType:
    return 'news' if value == 'financial_press' else value


def canonical_trust_level(value: TrustLevel | Literal['reviewed']) -> TrustLevel:
    # Operator review does not supply a calibrated trust category.
    return 'unknown' if value == 'reviewed' else value


@dataclass(frozen=True)
class SourceMetadata:
    source_id: str
    name: str
    source_type: SourceType
    source_role: SourceRole
    trust_level: TrustLevel
    config_version: str
    backend_source_type: SourceType | Literal['financial_press']
    backend_trust_level: TrustLevel | Literal['reviewed']

    def __post_init__(self):
        validate(self.to_dict(), 'SourceMetadata')
        if (self.source_type != canonical_source_type(self.backend_source_type)
                or self.trust_level != canonical_trust_level(self.backend_trust_level)):
            raise DocumentInputError('Inconsistent canonical source categories')

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass(frozen=True)
class ExtractionMetadata:
    extraction_method: ExtractionMethod
    extraction_errors: tuple[ExtractionErrorCode, ...]

    def __post_init__(self):
        if not isinstance(self.extraction_errors, tuple):
            raise DocumentInputError('Extraction errors must be an immutable tuple')
        validate(self.to_dict(), 'ExtractionMetadata')

    def to_dict(self) -> dict:
        return {'extraction_method': self.extraction_method,
                'extraction_errors': list(self.extraction_errors)}


@dataclass(frozen=True)
class SavedDocumentVersion:
    document_id: str
    document_version_id: str
    title: str
    normalized_text: str
    source: SourceMetadata
    published_at: str | None
    retrieved_at: str
    raw_ref: str
    content_hash: str
    extraction: ExtractionMetadata
    pipeline_version: str
    trace_id: str
    external_id: str | None
    canonical_url: str | None
    language: str | None
    author: str | None

    def __post_init__(self):
        if not isinstance(self.source, SourceMetadata) or not isinstance(self.extraction, ExtractionMetadata):
            raise DocumentInputError('Expected typed source and extraction metadata')
        validate(self.to_dict(), 'SavedDocumentVersion')

    def to_dict(self) -> dict:
        value = asdict(self)
        value['extraction'] = self.extraction.to_dict()
        return value


@dataclass(frozen=True)
class QualityIssue:
    code: FailureCode | WarningCode
    field: str
    message: str

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass(frozen=True)
class QualityResult:
    document_id: str
    document_version_id: str
    pipeline_version: str
    quality_rules_version: str
    status: Literal['PASS', 'FAIL']
    failures: tuple[QualityIssue, ...]
    warnings: tuple[QualityIssue, ...]
    text_utf8_bytes: int | None

    def __post_init__(self):
        if not isinstance(self.failures, tuple) or not isinstance(self.warnings, tuple):
            raise ValueError('Quality diagnostics must be immutable tuples')
        validate(self.to_dict(), 'QualityResult')

    def to_dict(self) -> dict:
        value = asdict(self)
        value['failures'] = [issue.to_dict() for issue in self.failures]
        value['warnings'] = [issue.to_dict() for issue in self.warnings]
        return value
