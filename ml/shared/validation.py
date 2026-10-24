"""Full canonical schema validation plus retrieval application invariants."""
import json
import math
from datetime import datetime
from functools import lru_cache
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker
from jsonschema.exceptions import FormatError

# This repository layout has ml/ and docs/ as siblings.
SCHEMA_PATH = Path(__file__).resolve().parents[2] / 'docs/ml/contracts/v1/contracts.schema.json'
_SCHEMA = json.loads(SCHEMA_PATH.read_text(encoding='utf-8'))


class ContractValidationError(ValueError):
    """Invalid DTO; safe message never includes supplied document/query contents."""

    def __init__(self, message: str, code: str = 'INVALID_REQUEST'):
        super().__init__(message)
        self.code = code


def validate_uuid(value: str) -> None:
    try:
        if not isinstance(value, str):
            raise ContractValidationError('Invalid UUID')
        FormatChecker().check(value, 'uuid')
    except FormatError as error:
        raise ContractValidationError('Invalid UUID') from error


def timestamp(value: str) -> datetime:
    return datetime.fromisoformat(value.replace('z', '+00:00').replace('Z', '+00:00').replace('t', 'T'))


@lru_cache(maxsize=None)
def _validator(definition: str) -> Draft202012Validator:
    schema = _SCHEMA
    if definition not in schema['$defs']:
        raise ValueError('Unknown contract definition')
    return Draft202012Validator(
        {'$ref': f'#/$defs/{definition}', '$defs': schema['$defs']},
        format_checker=FormatChecker(),
    )


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ContractValidationError(message)


def validate(value: object, definition: str) -> None:
    """Validate JSON-native data without coercion; raises ContractValidationError."""
    if isinstance(value, dict) and 'contract_version' in value and value['contract_version'] != 'v1':
        raise ContractValidationError('Unsupported contract version', 'UNSUPPORTED_VERSION')
    if next(_validator(definition).iter_errors(value), None) is not None:
        raise ContractValidationError(f'{definition}: JSON Schema validation failed')
    # jsonschema permits mathematically integral floats; this boundary accepts JSON
    # integer values only, as required by the logical DTO documentation.
    if definition == 'SearchRequest':
        _require(type(value['top_k']) is int, 'top_k must be an integer')
        _require(bool(value['query'].strip()), 'query must not be blank')
        f = value['filters']
        if f['published_from'] and f['published_to']:
            _require(timestamp(f['published_from']) < timestamp(f['published_to']), 'Invalid date interval')
    if definition in ('RankedChunk', 'SearchResponse'):
        items = [value] if definition == 'RankedChunk' else value['results']
        for item in items:
            score = item['retrieval_score']
            _require(score is None or type(score) is int or math.isfinite(score), 'Score must be finite or null')
            _require(type(item['rank']) is int, 'rank must be an integer')
    if definition not in ('SearchResponse', 'GetChunksResponse'):
        return
    status, results = value['status'], value['results']
    _require((status == 'error') == (value['error'] is not None), 'status/error mismatch')
    _require(bool(results) == (status in ('ok', 'partial')), 'status/results mismatch')
    ids = [c['chunk_id'] for c in results]
    _require(len(ids) == len(set(ids)), 'Duplicate chunk IDs')
    if definition == 'SearchResponse':
        _require([c['rank'] for c in results] == list(range(1, len(results) + 1)), 'Invalid ranks')
        _require(status != 'partial' or bool(value['warnings']), 'Partial search requires warnings')
    else:
        unresolved = value['unresolved_chunk_ids']
        _require(len(unresolved) == len(set(unresolved)), 'Duplicate unresolved IDs')
        _require(not set(ids).intersection(unresolved), 'Resolved/unresolved overlap')
        _require(status not in ('ok', 'error') or not unresolved, 'Unexpected unresolved IDs')
        _require(status not in ('partial', 'empty') or bool(unresolved), 'Missing unresolved IDs')


def validate_pair(request: dict, response: dict, operation: str) -> None:
    """Check schema and invariants requiring both sides of one logical call."""
    if operation not in ('search', 'get_chunks'):
        raise ValueError('Unknown retrieval operation')
    prefix = 'Search' if operation == 'search' else 'GetChunks'
    validate(request, prefix + 'Request')
    validate(response, prefix + 'Response')
    _require(response['trace_id'] == request['trace_id'], 'Trace mismatch')
    if operation == 'search':
        _require(len(response['results']) <= request['top_k'], 'top_k exceeded')
    elif response['status'] != 'error':
        unique = list(dict.fromkeys(request['chunk_ids']))
        resolved = [c['chunk_id'] for c in response['results']]
        unresolved = response['unresolved_chunk_ids']
        _require(set(resolved) | set(unresolved) == set(unique), 'Incomplete ID partition')
        _require(resolved == [i for i in unique if i in resolved], 'Invalid result order')
        _require(unresolved == [i for i in unique if i in unresolved], 'Invalid unresolved order')
