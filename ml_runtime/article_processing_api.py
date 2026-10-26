"""Authenticated HTTP boundary for Go article-processing operations."""

from __future__ import annotations

import hmac
import os
from typing import Any, Collection

from fastapi import Body, FastAPI, Header, HTTPException, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

from .article_processing import ProcessingContractError, parse_article_processing
from .operations import ArticleProcessingAPI


POST_PATH = "/internal/v0.1/article-processings"
GET_PATH = POST_PATH + "/{processing_id}"


def create_article_processing_app(
    coordinator: ArticleProcessingAPI,
    *,
    service_token: str,
    max_text_bytes: int,
    supported_pipeline_versions: Collection[str],
) -> FastAPI:
    """Build the Go-facing API around injected processing stages and storage.

    This factory intentionally requires a token, size limit, and supported
    pipeline versions. Deployment code must provide them from secret/config
    management; no permissive defaults are selected here.
    """
    if not service_token.strip():
        raise ValueError("ML_SERVICE_TOKEN must be configured")
    if max_text_bytes <= 0 or not supported_pipeline_versions:
        raise ValueError("ML_MAX_TEXT_BYTES and supported pipeline versions must be configured")
    quality_limit = getattr(coordinator.quality_gate, "max_text_bytes", None)
    if quality_limit is not None and max_text_bytes != quality_limit:
        raise ValueError(
            f"ML_MAX_TEXT_BYTES must match the configured quality gate limit ({quality_limit})"
        )

    app = FastAPI(title="Burmalda ML article processing", version="0.1")

    @app.exception_handler(HTTPException)
    async def http_error_handler(_request: Request, exc: HTTPException) -> JSONResponse:
        code = {
            401: "UNAUTHENTICATED",
            403: "FORBIDDEN",
            404: "NOT_FOUND",
            400: "VALIDATION_ERROR",
            409: "CONFLICT",
            422: "UNSUPPORTED_VERSION",
        }.get(exc.status_code, "INTERNAL_ERROR")
        return JSONResponse(
            status_code=exc.status_code,
            content={"code": code, "message": str(exc.detail), "retryable": False},
        )

    @app.exception_handler(RequestValidationError)
    async def request_error_handler(_request: Request, _exc: RequestValidationError) -> JSONResponse:
        return JSONResponse(
            status_code=400,
            content={"code": "VALIDATION_ERROR", "message": "Invalid JSON request", "retryable": False},
        )

    def authenticate(authorization: str | None) -> None:
        if not authorization:
            raise HTTPException(status_code=401, detail="Bearer service token is required")
        scheme, separator, supplied = authorization.partition(" ")
        if (
            not separator
            or scheme.lower() != "bearer"
            or not supplied
            or not hmac.compare_digest(supplied, service_token)
        ):
            raise HTTPException(status_code=401, detail="Bearer service token is invalid")

    @app.post(POST_PATH, status_code=202)
    def submit_article_processing(
        payload: Any = Body(...),
        idempotency_key: str | None = Header(default=None, alias="Idempotency-Key"),
        authorization: str | None = Header(default=None, alias="Authorization"),
    ) -> JSONResponse:
        authenticate(authorization)
        try:
            request = parse_article_processing(
                payload,
                idempotency_key,
                max_text_bytes=max_text_bytes,
                supported_pipeline_versions=supported_pipeline_versions,
            )
            response, _created = coordinator.submit(request)
        except ProcessingContractError as exc:
            return JSONResponse(status_code=exc.status, content=exc.as_error())
        except Exception:
            return JSONResponse(
                status_code=503,
                content={
                    "code": "INTERNAL_ERROR",
                    "message": "Article-processing operation could not be persisted",
                    "retryable": True,
                },
                headers={"Retry-After": "2"},
            )
        return JSONResponse(status_code=202, content=response)

    @app.get(GET_PATH)
    def get_article_processing(
        processing_id: str,
        authorization: str | None = Header(default=None, alias="Authorization"),
    ) -> JSONResponse:
        authenticate(authorization)
        response = coordinator.get(processing_id)
        if response is None:
            raise HTTPException(status_code=404, detail="Article-processing operation not found")
        return JSONResponse(status_code=200, content=response)

    app.state.article_processing_coordinator = coordinator

    @app.get("/healthz", include_in_schema=False)
    def healthcheck() -> dict[str, str]:
        return {"status": "ok"}

    return app


def create_article_processing_app_from_env(coordinator: ArticleProcessingAPI) -> FastAPI:
    """Read deployment configuration and construct the authenticated API."""
    token = os.environ.get("ML_SERVICE_TOKEN", "")
    try:
        max_text_bytes = int(os.environ.get("ML_MAX_TEXT_BYTES", ""))
    except ValueError as exc:
        raise ValueError("ML_MAX_TEXT_BYTES must be a positive integer") from exc
    versions = frozenset(
        version.strip()
        for version in os.environ.get("ML_PIPELINE_VERSIONS", "").split(",")
        if version.strip()
    )
    return create_article_processing_app(
        coordinator,
        service_token=token,
        max_text_bytes=max_text_bytes,
        supported_pipeline_versions=versions,
    )
