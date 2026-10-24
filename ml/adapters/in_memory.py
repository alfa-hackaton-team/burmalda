"""Deterministic R01 adapter: configured query candidates, never a real index.

All configuration is supplied by trusted application code, not by the request.
No I/O occurs during search/get_chunks. Inputs and outputs are isolated copies.
"""
import json
import logging
from copy import deepcopy
from dataclasses import dataclass
from pathlib import Path
from time import perf_counter
from typing import Mapping, Sequence

from ml.shared.dto import Chunk, Error, ErrorCode, GetChunksRequest, GetChunksResponse, SearchRequest, SearchResponse
from ml.shared.validation import ContractValidationError, timestamp, validate, validate_pair, validate_uuid


LOGGER = logging.getLogger(__name__)


@dataclass(frozen=True)
class DocumentVersion:
    """Internal fixture metadata, not a new wire DTO or database status policy."""

    document_id: str
    document_version_id: str
    version_no: int
    ready: bool = True


@dataclass(frozen=True)
class Candidate:
    chunk_id: str
    score: float | None


@dataclass(frozen=True)
class QueryFixture:
    candidates: tuple[Candidate, ...] = ()
    incomplete: bool = False
    warnings: tuple[str, ...] = ()


class InMemoryRetriever:
    def __init__(
        self,
        *,
        chunks: Sequence[Chunk],
        versions: Sequence[DocumentVersion],
        queries: Mapping[str, QueryFixture],
        context_chunks: Mapping[str, Sequence[str]],
        retrieval_config_version: str = 'r01-fixture-v1',
        service_chunk_ids: Sequence[str] | None = None,
        failures: Mapping[str, Error] | None = None,
    ):
        """Create an isolated corpus with explicit per-context chunk allowlists.

        service_chunk_ids=None denies null context. A supplied allowlist represents
        an already authenticated service caller; it does not authenticate requests.
        failures injects controlled technical failures for ML2 contract scenarios.
        """
        self._chunks = {}
        for chunk in deepcopy(chunks):
            validate(chunk, 'Chunk')
            if chunk['chunk_id'] in self._chunks:
                raise ValueError('Duplicate configured chunk ID')
            self._chunks[chunk['chunk_id']] = chunk
        self._versions = {}
        numbers = set()
        for version in versions:
            validate_uuid(version.document_id)
            validate_uuid(version.document_version_id)
            if type(version.version_no) is not int or version.version_no < 1 or type(version.ready) is not bool:
                raise ValueError('Invalid configured version metadata')
            key = (version.document_id, version.version_no)
            if version.document_version_id in self._versions or key in numbers:
                raise ValueError('Duplicate configured version')
            self._versions[version.document_version_id] = version
            numbers.add(key)
        for chunk in self._chunks.values():
            version = self._versions.get(chunk['document_version_id'])
            if version is None or version.document_id != chunk['document_id']:
                raise ValueError('Chunk/version mismatch')
        for version in self._versions.values():
            if version.ready and not any(c['document_version_id'] == version.document_version_id for c in self._chunks.values()):
                raise ValueError('Ready fixture version has no chunks')
        self._latest = {}
        for version in self._versions.values():
            previous = self._latest.get(version.document_id)
            if previous is None or version.version_no > previous.version_no:
                self._latest[version.document_id] = version
        self._queries = deepcopy(dict(queries))
        for query, fixture in self._queries.items():
            if not isinstance(query, str) or not query.strip() or query != query.strip():
                raise ValueError('Configured query must be nonblank and trimmed')
            if type(fixture.incomplete) is not bool or (fixture.incomplete and not fixture.warnings):
                raise ValueError('Incomplete query requires warnings')
            for warning in fixture.warnings:
                if not isinstance(warning, str) or not warning.strip():
                    raise ValueError('Invalid fixture warning')
            seen = set()
            for candidate in fixture.candidates:
                if candidate.chunk_id not in self._chunks or candidate.chunk_id in seen:
                    raise ValueError('Unknown or duplicate candidate')
                validate({**self._chunks[candidate.chunk_id], 'rank': 1,
                          'retrieval_score': candidate.score}, 'RankedChunk')
                seen.add(candidate.chunk_id)
        if not isinstance(retrieval_config_version, str) or not retrieval_config_version.strip():
            raise ValueError('Missing retrieval configuration version')
        self._config = retrieval_config_version
        self._access = {}
        for context, allowed in context_chunks.items():
            validate_uuid(context)
            self._access[context] = self._allowlist(allowed)
        self._service_access = None if service_chunk_ids is None else self._allowlist(service_chunk_ids)
        self._failures = deepcopy(dict(failures or {}))
        for operation, failure in self._failures.items():
            if operation not in ('search', 'get_chunks'):
                raise ValueError('Unknown failure operation')
            validate(failure, 'Error')

    def _allowlist(self, ids: Sequence[str]) -> frozenset[str]:
        result = frozenset(ids)
        if not result <= self._chunks.keys():
            raise ValueError('Allowlist contains unknown chunk')
        return result

    def _access_for(self, context: str | None) -> frozenset[str] | None:
        return self._service_access if context is None else self._access.get(context)

    @staticmethod
    def _error(code: ErrorCode, retryable: bool = False) -> Error:
        # Never expose injected exception text, private SQL or corpus contents.
        return {'code': code, 'message': 'Retrieval operation could not be completed.', 'retryable': retryable}

    def _failure(self, operation: str, allowed: frozenset[str] | None) -> Error | None:
        if allowed is None:
            return self._error('CONTEXT_MISMATCH')
        failure = self._failures.get(operation)
        if failure:
            return self._error(failure['code'], failure['retryable'])
        return None

    def search(self, request: SearchRequest) -> SearchResponse:
        started = perf_counter()
        validate(request, 'SearchRequest')
        allowed = self._access_for(request['business_context_id'])
        error = self._failure('search', allowed)
        response: SearchResponse = {
            'contract_version': 'v1', 'trace_id': request['trace_id'],
            'status': 'error' if error else 'empty', 'results': [],
            'retrieval_config_version': self._config, 'warnings': [], 'error': error,
        }
        if error is None:
            fixture = self._queries.get(request['query'].strip(), QueryFixture())
            response['warnings'] = list(fixture.warnings)
            candidates = [c for c in fixture.candidates
                          if c.chunk_id in allowed and self._matches(self._chunks[c.chunk_id], request['filters'])]
            # Null scores follow known scores. IDs make ties stable regardless of
            # input order; scores are fixture diagnostics, never probabilities.
            candidates.sort(key=lambda c: (c.score is None, -c.score if c.score is not None else 0, c.chunk_id))
            for rank, candidate in enumerate(candidates[:request['top_k']], 1):
                response['results'].append({**deepcopy(self._chunks[candidate.chunk_id]),
                                            'rank': rank, 'retrieval_score': candidate.score})
            if response['results']:
                response['status'] = 'partial' if fixture.incomplete else 'ok'
        validate_pair(request, response, 'search')
        self._log('search', response, started)
        return response

    def _matches(self, chunk: Chunk, filters: dict) -> bool:
        version = self._versions[chunk['document_version_id']]
        explicit = filters['document_version_ids']
        if not version.ready:
            return False
        if explicit:
            if version.document_version_id not in explicit:
                return False
        elif self._latest[version.document_id].document_version_id != version.document_version_id:
            return False
        if filters['source_ids'] and chunk['source_id'] not in filters['source_ids']:
            return False
        if filters['source_roles'] and chunk['source_role'] not in filters['source_roles']:
            return False
        start, end = filters['published_from'], filters['published_to']
        if start is None and end is None:
            return True
        if chunk['published_at'] is None:
            return filters['include_undated']
        date = timestamp(chunk['published_at'])
        return (start is None or date >= timestamp(start)) and (end is None or date < timestamp(end))

    def get_chunks(self, request: GetChunksRequest) -> GetChunksResponse:
        started = perf_counter()
        validate(request, 'GetChunksRequest')
        allowed = self._access_for(request['business_context_id'])
        error = self._failure('get_chunks', allowed)
        response: GetChunksResponse = {
            'contract_version': 'v1', 'trace_id': request['trace_id'],
            'status': 'error' if error else 'empty', 'results': [],
            'unresolved_chunk_ids': [], 'warnings': [], 'error': error,
        }
        if error is None:
            for chunk_id in dict.fromkeys(request['chunk_ids']):
                if chunk_id in allowed:
                    response['results'].append(deepcopy(self._chunks[chunk_id]))
                else:
                    response['unresolved_chunk_ids'].append(chunk_id)
            if response['unresolved_chunk_ids']:
                response['warnings'] = ['Some requested chunks could not be resolved.']
            if response['results']:
                response['status'] = 'partial' if response['unresolved_chunk_ids'] else 'ok'
        validate_pair(request, response, 'get_chunks')
        self._log('get_chunks', response, started)
        return response

    def _log(self, operation: str, response: dict, started: float) -> None:
        LOGGER.info('Retrieval completed', extra={
            'operation': operation, 'trace_id': response['trace_id'],
            'retrieval_config_version': self._config, 'status': response['status'],
            'result_count': len(response['results']),
            'unresolved_count': len(response.get('unresolved_chunk_ids', [])),
            'warning_count': len(response['warnings']),
            'error_code': response['error']['code'] if response['error'] else None,
            'duration_ms': (perf_counter() - started) * 1000,
        })

    @classmethod
    def from_fixtures(cls, directory: Path | None = None) -> 'InMemoryRetriever':
        """Load the canonical synthetic happy path; only its context is allowed."""
        root = directory or Path(__file__).resolve().parents[2] / 'docs/ml/fixtures/v1'
        def load(name):
            return json.loads((root / name).read_text(encoding='utf-8'))
        request, response, chunk_response = load('search.request.json'), load('search.ok.json'), load('chunks.ok.json')
        validate_pair(request, response, 'search')
        validate(chunk_response, 'GetChunksResponse')
        chunks = {c['chunk_id']: c for c in chunk_response['results']}
        for ranked in response['results']:
            chunk = {k: v for k, v in ranked.items() if k not in ('rank', 'retrieval_score')}
            if chunk['chunk_id'] in chunks and chunks[chunk['chunk_id']] != chunk:
                raise ContractValidationError('Conflicting fixture chunk contents')
            chunks[chunk['chunk_id']] = chunk
        versions = {c['document_version_id']: DocumentVersion(c['document_id'], c['document_version_id'], 1)
                    for c in chunks.values()}
        return cls(
            chunks=list(chunks.values()), versions=list(versions.values()),
            queries={request['query']: QueryFixture(tuple(Candidate(c['chunk_id'], c['retrieval_score'])
                                                         for c in response['results']))},
            context_chunks={request['business_context_id']: list(chunks)},
        )
