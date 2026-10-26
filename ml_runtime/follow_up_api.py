"""Thin, synchronous production HTTP adapter for saved-Insight follow-ups."""
from typing import Any

from fastapi import Body, Header
from fastapi.responses import JSONResponse

from .analysis_validation import AnalysisRequestError, validate_analysis_request, validate_contract_message
from .follow_up_store import FollowUpConflict, PostgresFollowUpStore


class SnapshotOnlyRetriever:
    """Follow-up cannot enter ML1, including on an evidence miss."""
    def search(self, request):
        raise RuntimeError('Follow-up must not search')

    def get_chunks(self, request):
        raise RuntimeError('Follow-up must not reload chunks')


def create_follow_up_service():
    from .model_client import create_agent_from_env
    from .service import AnalysisService
    return AnalysisService(SnapshotOnlyRetriever(), create_agent_from_env())


def register_follow_up(app, authenticate):
    # Lazy factories keep article-processing startup independent of gateway config.
    app.state.follow_up_store_factory = PostgresFollowUpStore
    app.state.follow_up_service_factory = create_follow_up_service

    def error(status, code, message, retryable=False):
        return JSONResponse(status_code=status,
                            content={'code': code, 'message': message, 'retryable': retryable})

    @app.post('/internal/v0.1/follow-ups')
    def follow_up(payload: Any = Body(...),
                  authorization: str | None = Header(default=None),
                  idempotency_key: str | None = Header(default=None, alias='Idempotency-Key')):
        authenticate(authorization)
        try:
            validate_contract_message(payload, 'AnalysisRequest')
        except AnalysisRequestError:
            return error(400, 'VALIDATION_ERROR', 'Invalid AnalysisRequest')
        if payload['request_type'] != 'follow_up' or payload['follow_up'] is None:
            return error(400, 'VALIDATION_ERROR', 'Only follow_up requests are accepted')
        if not idempotency_key or idempotency_key != payload['request_operation_id']:
            return error(400, 'VALIDATION_ERROR', 'Idempotency-Key must equal request_operation_id')
        try:
            validate_analysis_request(payload)
        except AnalysisRequestError:
            return error(403, 'FORBIDDEN_CONTEXT', 'Follow-up snapshot/context is inconsistent')
        try:
            store = app.state.follow_up_store_factory()
            response = store.execute(idempotency_key, payload,
                lambda request: app.state.follow_up_service_factory().analyze(request))
            return JSONResponse(status_code=200, content=response)
        except FollowUpConflict:
            return error(409, 'CONFLICT', 'Idempotency-Key has different parameters')
        except Exception:
            return error(503, 'INTERNAL_ERROR', 'Follow-up persistence is unavailable', True)
