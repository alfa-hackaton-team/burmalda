"""Validation boundary for Go's merged article-processing contract.

This module validates and fingerprints a request before an operation is queued.
The durable operation store and ML1/ML2 processing stages are separate adapters;
they must be connected before the HTTP endpoint can report SUCCEEDED.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from datetime import datetime
from typing import Any, Collection
from urllib.parse import urlparse
from uuid import UUID


class ProcessingContractError(ValueError):
    def __init__(self, status: int, code: str, message: str, retryable: bool = False) -> None:
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message
        self.retryable = retryable

    def as_error(self) -> dict[str, Any]:
        return {"code": self.code, "message": self.message, "retryable": self.retryable}


@dataclass(frozen=True)
class ArticleProcessingInput:
    article: dict[str, Any]
    source: dict[str, str]
    pipeline_version: str
    trace_id: str
    idempotency_key: str
    fingerprint: str


_ARTICLE_REQUIRED = {
    "article_id", "version_no", "article_version_id", "source_id", "title", "body",
    "fetched_at", "raw_ref", "content_hash", "metadata",
}
_ARTICLE_OPTIONAL = {
    "external_id", "canonical_url", "published_at", "language", "author",
}
_SOURCE_REQUIRED = {
    "source_id", "name", "source_role", "source_type", "trust_level", "config_version",
}
_SOURCE_ROLES = {"external", "bank", "expert"}
_SOURCE_TYPES = {"news", "bank_analytics", "expert", "regulator", "financial_press"}
_TRUST_LEVELS = {"primary", "trusted", "standard", "unknown", "reviewed"}
_EXTRACTION_METHODS = {"full_article", "rss_summary", "pdf_extracted"}
_EXTRACTION_ERRORS = {
    "TEXT_TRUNCATED", "MISSING_PAGES", "MISSING_TABLES", "MISSING_FOOTNOTES",
    "EXTRACTION_FAILED", "LAYOUT_LOSS", "READING_ORDER_UNCERTAIN",
}
_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_CONFIG_VERSION = re.compile(r"^[1-9][0-9]*$")


def parse_article_processing(
    payload: Any,
    idempotency_key: str | None,
    *,
    max_text_bytes: int,
    supported_pipeline_versions: Collection[str],
) -> ArticleProcessingInput:
    """Validate a Go POST body and derive a stable request fingerprint.

    ``max_text_bytes`` is measured over UTF-8 ``article.body`` only, excluding
    the title, as defined by the merged OpenAPI contract. The supported version
    set comes from ML deployment configuration; this code intentionally picks
    no default version on behalf of the team.
    """
    if max_text_bytes <= 0 or not supported_pipeline_versions:
        raise RuntimeError("ML_MAX_TEXT_BYTES and supported pipeline versions must be configured")
    if not isinstance(payload, dict):
        raise _invalid("request body must be an object")
    _only_keys(payload, {"article", "source", "pipeline_version", "trace_id"}, "request")
    for key in ("article", "source", "pipeline_version", "trace_id"):
        if key not in payload:
            raise _invalid(f"request is missing {key}")

    article = payload["article"]
    source = payload["source"]
    if not isinstance(article, dict) or not isinstance(source, dict):
        raise _invalid("article and source must be objects")
    _only_keys(article, _ARTICLE_REQUIRED | _ARTICLE_OPTIONAL, "article")
    _only_keys(source, _SOURCE_REQUIRED, "source")
    for key in _ARTICLE_REQUIRED:
        if key not in article:
            raise _invalid(f"article is missing {key}")
    for key in _SOURCE_REQUIRED:
        if key not in source:
            raise _invalid(f"source is missing {key}")

    for field in ("article_id", "article_version_id", "source_id", "fetched_at", "raw_ref", "content_hash"):
        _nonempty_string(article[field], f"article.{field}")
    for field in ("article_id", "article_version_id", "source_id"):
        _uuid(article[field], f"article.{field}")
    if type(article["version_no"]) is not int or article["version_no"] < 1:
        raise _invalid("article.version_no must be a positive integer")
    title = article["title"]
    if not isinstance(title, str):
        raise _invalid("article.title must be a string")
    body = article["body"]
    if not isinstance(body, str):
        raise _invalid("article.body must be a string")
    try:
        body_size = len(body.encode("utf-8"))
    except UnicodeEncodeError:
        # R02 owns this quality failure; malformed Unicode must reach its
        # deterministic gate instead of escaping as an unhandled 500 here.
        body_size = None
    if body_size is not None and body_size > max_text_bytes:
        raise ProcessingContractError(413, "PAYLOAD_TOO_LARGE", "UTF-8 article.body exceeds the configured limit")
    if not _SHA256.fullmatch(article["content_hash"]):
        raise _invalid("article.content_hash must be a lowercase SHA-256 hex digest")
    _validate_extraction_metadata(article["metadata"])

    for field in ("external_id", "language", "author"):
        if field in article and not isinstance(article[field], str):
            raise _invalid(f"article.{field} must be a string")
    if "canonical_url" in article:
        _nonempty_string(article["canonical_url"], "article.canonical_url")
        if not urlparse(article["canonical_url"]).scheme:
            raise _invalid("article.canonical_url must be an absolute URI")
    for field in ("fetched_at", "published_at"):
        if field in article and article[field] is not None:
            _datetime(article[field], f"article.{field}")
    if "published_at" in article and article["published_at"] is not None and not isinstance(article["published_at"], str):
        raise _invalid("article.published_at must be a date-time or null")

    for field in _SOURCE_REQUIRED:
        _nonempty_string(source[field], f"source.{field}")
    _uuid(source["source_id"], "source.source_id")
    if source["source_role"] not in _SOURCE_ROLES:
        raise _invalid("source.source_role is unsupported")
    if source["source_type"] not in _SOURCE_TYPES:
        raise _invalid("source.source_type is unsupported")
    if source["trust_level"] not in _TRUST_LEVELS:
        raise _invalid("source.trust_level is unsupported")
    if not _CONFIG_VERSION.fullmatch(source["config_version"]):
        raise _invalid("source.config_version must be a positive decimal version")
    if article["source_id"] != source["source_id"]:
        raise _invalid("article.source_id must match source.source_id")

    pipeline_version = payload["pipeline_version"]
    trace_id = payload["trace_id"]
    _nonempty_string(pipeline_version, "pipeline_version")
    _nonempty_string(trace_id, "trace_id")
    if pipeline_version not in supported_pipeline_versions:
        raise ProcessingContractError(422, "UNSUPPORTED_PIPELINE_VERSION", "pipeline_version is not supported")
    _nonempty_string(idempotency_key, "Idempotency-Key", status=400)

    expected_key = f"{article['article_version_id']}:{pipeline_version}"
    if idempotency_key != expected_key:
        raise _invalid("Idempotency-Key must be article_version_id:pipeline_version")

    # The transport trace is deliberately excluded so retries with a fresh trace
    # still identify the same logical request. Source config_version is included.
    stable = {
        "article": article,
        "source": source,
        "pipeline_version": pipeline_version,
    }
    try:
        stable_bytes = json.dumps(
            stable, ensure_ascii=False, sort_keys=True, separators=(",", ":")
        ).encode("utf-8")
    except UnicodeEncodeError:
        # Preserve malformed-but-valid-JSON Unicode for R02 to reject with a
        # deterministic INVALID_UTF8_TEXT quality result rather than a 500.
        stable_bytes = json.dumps(
            stable, ensure_ascii=True, sort_keys=True, separators=(",", ":")
        ).encode("utf-8")
    fingerprint = hashlib.sha256(stable_bytes).hexdigest()
    return ArticleProcessingInput(
        article=article,
        source=source,
        pipeline_version=pipeline_version,
        trace_id=trace_id,
        idempotency_key=idempotency_key,
        fingerprint=fingerprint,
    )


def _only_keys(value: dict[str, Any], allowed: set[str], label: str) -> None:
    extra = set(value) - allowed
    if extra:
        raise _invalid(f"{label} contains unsupported fields: {', '.join(sorted(extra))}")


def _validate_extraction_metadata(value: Any) -> None:
    if not isinstance(value, dict):
        raise _invalid("article.metadata must be an object")
    _only_keys(value, {"extraction_method", "extraction_errors"}, "article.metadata")
    if set(value) != {"extraction_method", "extraction_errors"}:
        raise _invalid("article.metadata requires extraction_method and extraction_errors")
    method = value["extraction_method"]
    if not isinstance(method, str) or method not in _EXTRACTION_METHODS:
        raise _invalid("article.metadata.extraction_method is unsupported")
    errors = value["extraction_errors"]
    if not isinstance(errors, list) or any(
        not isinstance(code, str) or code not in _EXTRACTION_ERRORS for code in errors
    ):
        raise _invalid("article.metadata.extraction_errors contains an unsupported code")
    if len(errors) != len(set(errors)):
        raise _invalid("article.metadata.extraction_errors must not contain duplicates")


def _nonempty_string(value: Any, label: str, status: int = 400) -> None:
    if not isinstance(value, str) or not value.strip():
        raise ProcessingContractError(status, "VALIDATION_ERROR", f"{label} must be a non-empty string")


def _uuid(value: str, label: str) -> None:
    try:
        UUID(value)
    except (ValueError, AttributeError) as exc:
        raise _invalid(f"{label} must be a UUID") from exc


def _datetime(value: Any, label: str) -> None:
    if not isinstance(value, str):
        raise _invalid(f"{label} must be an RFC 3339 date-time")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise _invalid(f"{label} must be an RFC 3339 date-time") from exc
    if parsed.tzinfo is None:
        raise _invalid(f"{label} must include a timezone")


def _invalid(message: str) -> ProcessingContractError:
    return ProcessingContractError(400, "VALIDATION_ERROR", message)
