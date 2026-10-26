"""Authenticated Go-facing product API; no fixture fallbacks."""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
from functools import lru_cache
from pathlib import Path
from typing import Any

from fastapi import Body, Header, HTTPException
from fastapi.responses import JSONResponse
from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource

from .analysis_validation import AnalysisRequestError, validate_analysis_request
from .product_store import OperationConflict


@lru_cache
def public_validator(name):
    root = Path(__file__).resolve().parents[1]
    schema = json.loads((root / 'backend/internal/apicontract/public.json').read_text())
    ml_path = root / 'docs/ml/contracts/v1/contracts.schema.json'
    registry = Registry().with_resource(ml_path.as_uri(), Resource.from_contents(json.loads(ml_path.read_text())))
    return Draft202012Validator({**schema, '$id': (root / 'docs/api/schemas.yaml').as_uri(),
        '$ref': '#/components/schemas/' + name}, registry=registry, format_checker=FormatChecker())


def validate_ranking_request(payload):
    if not isinstance(payload, dict) or set(payload) - {'request_context', 'limit', 'topic', 'cursor'}:
        raise ValueError('Invalid ranking request')
    public_validator('TrustedRequestContext').validate(payload.get('request_context'))
    limit = payload.get('limit', 100)
    if type(limit) is not int or not 1 <= limit <= 100:
        raise ValueError('limit must be between 1 and 100')
    if 'topic' in payload and (not isinstance(payload['topic'], str) or len(payload['topic']) > 100):
        raise ValueError('Invalid topic')
    if 'cursor' in payload and (not isinstance(payload['cursor'], str) or len(payload['cursor']) > 1024):
        raise ValueError('Invalid cursor')
    return limit


class FeedRankingService:
    """Stable pages of real extracted events, newest first until a relevance policy is calibrated.

    No predicted impact or invented profile matches are presented as established facts.
    Go supplies the authorized business context; this service only exposes the shared
    external/bank/expert corpus, never company-private documents.
    """
    def __init__(self, store, corpus, token):
        self.store, self.corpus, self.token = store, corpus, token.encode()

    def rank(self, request):
        limit = validate_ranking_request(request)
        context = dict(request['request_context'])
        context.pop('request_id')
        context.pop('trace_id')
        digest = hashlib.sha256(json.dumps({'context': context, 'topic': request.get('topic'),
            'limit': limit}, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        offset = 0
        if request.get('cursor'):
            try:
                encoded, signature = request['cursor'].split('.')
                if not hmac.compare_digest(signature, hmac.new(self.token, encoded.encode(), 'sha256').hexdigest()):
                    raise ValueError('Invalid cursor signature')
                identity, offset = json.loads(base64.urlsafe_b64decode(encoded))
                if type(offset) is not int or offset < 0:
                    raise ValueError('Invalid cursor offset')
                items = self.store.get_ranking(identity, digest)
                if items is None or offset > len(items):
                    raise ValueError('Ranking cursor expired or does not match this context')
            except (TypeError, ValueError, KeyError) as exc:
                raise ValueError('Invalid or expired ranking cursor') from exc
        else:
            items = self.corpus.feed_items(request.get('topic'))
            identity = self.store.save_ranking(digest, items)
        end = offset + limit
        cursor = None
        if end < len(items):
            encoded = base64.urlsafe_b64encode(json.dumps([identity, end]).encode()).decode()
            cursor = encoded + '.' + hmac.new(self.token, encoded.encode(), 'sha256').hexdigest()
        result = {'items': items[offset:end], 'next_cursor': cursor}
        public_validator('FeedPage').validate(result)
        return result


def register_product_routes(app, *, store, ranking, service_token, analysis_version):
    if not service_token or not analysis_version:
        raise ValueError('ML_SERVICE_TOKEN and ML_ANALYSIS_CONFIG_VERSION are required')

    def authenticate(authorization):
        if not isinstance(authorization, str) or not hmac.compare_digest(
                authorization.encode(), ('Bearer ' + service_token).encode()):
            raise HTTPException(401, 'Bearer service token is invalid')

    def submit(payload, key, authorization, follow_up):
        authenticate(authorization)
        try:
            validate_analysis_request(payload)
            if payload['request_type'] not in (('follow_up',) if follow_up else ('initial', 'reanalyze')):
                raise ValueError('request_type does not match this endpoint')
            if not key or key != payload['request_operation_id']:
                raise ValueError('Idempotency-Key must equal request_operation_id')
            if payload['processing_config_version'] != analysis_version:
                raise HTTPException(422, 'Unsupported processing configuration')
        except (AnalysisRequestError, ValueError, TypeError):
            raise HTTPException(400, 'Invalid analysis request') from None
        try:
            return JSONResponse(status_code=202, content=store.submit(payload))
        except OperationConflict:
            raise HTTPException(409, 'Idempotency key has different parameters') from None

    @app.post('/internal/v0.1/analyses')
    def analyze(payload: Any = Body(...), idempotency_key: str | None = Header(default=None),
                authorization: str | None = Header(default=None)):
        return submit(payload, idempotency_key, authorization, False)

    @app.post('/internal/v0.1/follow-ups')
    def follow_up(payload: Any = Body(...), idempotency_key: str | None = Header(default=None),
                  authorization: str | None = Header(default=None)):
        return submit(payload, idempotency_key, authorization, True)

    @app.get('/internal/v0.1/analyses/{analysis_id}')
    def get_analysis(analysis_id: str, authorization: str | None = Header(default=None)):
        authenticate(authorization)
        result = store.get(analysis_id)
        if result is None:
            raise HTTPException(404, 'Analysis operation not found')
        return result

    @app.post('/internal/v0.1/feed-rankings')
    def rank_feed(payload: Any = Body(...), authorization: str | None = Header(default=None)):
        authenticate(authorization)
        from jsonschema import ValidationError
        try:
            return ranking.rank(payload)
        except (ValueError, TypeError, ValidationError):
            raise HTTPException(400, 'Invalid ranking request or cursor') from None
