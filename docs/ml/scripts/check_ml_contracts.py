#!/usr/bin/env python3
"""Offline fixture checker for the JSON Schema subset used by ML v1.

Not a general JSON Schema implementation. Production adapters should use a full
Draft 2020-12 validator plus application-level checks documented in contracts.md.
"""
import json
import math
from datetime import datetime
from pathlib import Path
from urllib.parse import urlparse
from uuid import UUID

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = json.loads((ROOT / 'contracts/v1/contracts.schema.json').read_text())


def check(value, schema, path='$'):
    if '$ref' in schema:
        check(value, SCHEMA['$defs'][schema['$ref'].split('/')[-1]], path)
        return
    for keyword, required in [('anyOf', 1), ('oneOf', 1)]:
        if keyword in schema:
            matches = 0
            for option in schema[keyword]:
                try:
                    check(value, option, path)
                    matches += 1
                except ValueError:
                    pass
            if matches < required or (keyword == 'oneOf' and matches != 1):
                raise ValueError(f'{path}: {keyword}, matched {matches}')
    if 'const' in schema and value != schema['const']:
        raise ValueError(f'{path}: invalid const')
    if 'enum' in schema and value not in schema['enum']:
        raise ValueError(f'{path}: invalid enum')
    kinds = {
        'null': value is None,
        'object': isinstance(value, dict),
        'array': isinstance(value, list),
        'string': isinstance(value, str),
        'integer': type(value) is int,
        'number': type(value) in (int, float) and math.isfinite(value),
        'boolean': type(value) is bool,
    }
    if 'type' in schema and not kinds[schema['type']]:
        raise ValueError(f'{path}: expected {schema["type"]}')
    if isinstance(value, dict):
        for key in schema.get('required', []):
            if key not in value:
                raise ValueError(f'{path}: missing {key}')
        props = schema.get('properties', {})
        if schema.get('additionalProperties') is False and set(value) - set(props):
            raise ValueError(f'{path}: unexpected properties {set(value) - set(props)}')
        for key in value.keys() & props.keys():
            check(value[key], props[key], f'{path}.{key}')
    if isinstance(value, list):
        if len(value) < schema.get('minItems', 0) or len(value) > schema.get('maxItems', math.inf):
            raise ValueError(f'{path}: invalid array size')
        for i, item in enumerate(value):
            check(item, schema.get('items', {}), f'{path}[{i}]')
    if isinstance(value, str):
        if len(value) < schema.get('minLength', 0):
            raise ValueError(f'{path}: string too short')
        fmt = schema.get('format')
        try:
            if fmt == 'uuid':
                UUID(value)
            elif fmt == 'date-time':
                if datetime.fromisoformat(value.replace('Z', '+00:00')).tzinfo is None:
                    raise ValueError('missing timezone')
            elif fmt == 'uri' and not urlparse(value).scheme:
                raise ValueError('missing URI scheme')
        except ValueError as error:
            raise ValueError(f'{path}: invalid {fmt}') from error
    if type(value) in (int, float):
        if value < schema.get('minimum', -math.inf) or value > schema.get('maximum', math.inf):
            raise ValueError(f'{path}: out of range')


def semantic(value, definition):
    status = value.get('status')
    if definition in ('SearchResponse', 'GetChunksResponse', 'ReadyVersionsResponse'):
        items = value.get('results', value.get('items', []))
        if (status == 'error') != (value['error'] is not None):
            raise ValueError('status/error mismatch')
        if status in ('error', 'empty') and items:
            raise ValueError('empty/error response contains items')
        if status in ('ok', 'partial') and not items:
            raise ValueError('ok/partial response has no items')
        if status == 'partial' and not value['warnings'] and not value.get('unresolved_chunk_ids'):
            raise ValueError('partial response must explain limitation')
        if definition != 'ReadyVersionsResponse':
            ids = [x['chunk_id'] for x in items]
            if len(ids) != len(set(ids)):
                raise ValueError('duplicate chunk IDs')
        if definition == 'SearchResponse' and [x['rank'] for x in items] != list(range(1, len(items) + 1)):
            raise ValueError('ranks must be contiguous')
    if definition == 'SearchRequest':
        filters = value['filters']
        start, end = filters['published_from'], filters['published_to']
        if start and end and datetime.fromisoformat(start.replace('Z', '+00:00')) >= datetime.fromisoformat(end.replace('Z', '+00:00')):
            raise ValueError('invalid date interval')
    if definition == 'AnalysisResponse':
        result = value['insight'] or value['message']
        if result:
            claims = result['claims']
            if len({c['id'] for c in claims}) != len(claims) or len({c['position'] for c in claims}) != len(claims):
                raise ValueError('duplicate claim ID/position')
            for claim in claims:
                if not any(e['evidence_role'] == 'support' for e in claim['evidence']):
                    raise ValueError('claim has no supporting evidence')
            if value['insight']:
                insight = value['insight']
                if insight['analysis_run_id'] != value['analysis_run_id']:
                    raise ValueError('run mismatch')
                if (insight['bank_view'] is None) != (insight['bank_view_document_version_id'] is None):
                    raise ValueError('bank view/reference mismatch')
            else:
                for citation in result['citations']:
                    if citation['claim_id'] is None and citation['chunk_id'] is None:
                        raise ValueError('citation has no target')


def main():
    directory = ROOT / 'fixtures/v1'
    manifest = json.loads((directory / 'manifest.json').read_text())
    loaded = {}
    for entry in manifest:
        value = json.loads((directory / entry['file']).read_text())
        check(value, SCHEMA['$defs'][entry['definition']])
        semantic(value, entry['definition'])
        loaded[entry['file']] = value
    chunks = {c['chunk_id']: c for c in loaded['chunks.ok.json']['results']}
    profile = loaded['analysis.request.json']['context']
    feature_ids = {f['id'] for f in profile['profile_features']}
    for name in ('analysis.ok.json', 'follow_up.ok.json'):
        response = loaded[name]
        result = response['insight'] or response['message']
        for claim in result['claims']:
            for evidence in claim['evidence']:
                if evidence['chunk_id']:
                    assert evidence['quote_text'] in chunks[evidence['chunk_id']]['chunk_text'], 'quote mismatch'
                if evidence['profile_feature_id']:
                    assert evidence['profile_feature_id'] in feature_ids, 'unknown profile feature'
    print(f'OK: {len(manifest)} fixtures; schema subset, status rules, IDs and quotes checked.')


if __name__ == '__main__':
    main()
