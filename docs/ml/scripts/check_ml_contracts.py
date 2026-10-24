#!/usr/bin/env python3
"""Offline fixture checker for the JSON Schema subset used by ML v1.

Not a general JSON Schema implementation. Production adapters should use a full
Draft 2020-12 validator plus application-level checks documented in contracts.md.
"""
import argparse
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
    if definition == 'AnalysisRequest':
        validate_request(value)
    if definition == 'AnalysisResponse':
        result = value['insight'] or value['message']
        if result:
            claims = result['claims']
            if len({c['id'] for c in claims}) != len(claims) or len({c['position'] for c in claims}) != len(claims):
                raise ValueError('duplicate claim ID/position')
            for claim in claims:
                if not any(e['evidence_role'] == 'support' for e in claim['evidence']):
                    raise ValueError('claim has no supporting evidence')
                if claim['verification_status'] == 'PARTIALLY_SUPPORTED' and not claim['assumptions']:
                    raise ValueError('partial support requires explicit assumptions')
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


def require(condition, message):
    if not condition:
        raise ValueError(message)


def validate_evidence(result, context, chunks):
    """Check supplied snapshots, not authorization or semantic entailment."""
    chunk_map = {c['chunk_id']: c for c in chunks}
    require(len(chunk_map) == len(chunks), 'duplicate snapshot chunk')
    features = {f['id'] for f in context['profile_features']}
    claims = result['claims']
    require(len({c['id'] for c in claims}) == len(claims), 'duplicate claim ID')
    require(len({c['position'] for c in claims}) == len(claims), 'duplicate claim position')
    for claim in claims:
        require(any(e['evidence_role'] == 'support' for e in claim['evidence']), 'claim has no support')
        if claim['verification_status'] == 'PARTIALLY_SUPPORTED':
            require(bool(claim['assumptions']), 'partial support requires explicit assumptions')
        bank_support = False
        for evidence in claim['evidence']:
            if evidence['chunk_id'] is not None:
                chunk = chunk_map.get(evidence['chunk_id'])
                require(chunk is not None, 'unknown snapshot chunk')
                require(bool(evidence['quote_text']) and evidence['quote_text'] in chunk['chunk_text'], 'quote mismatch')
                bank_support |= chunk['source_role'] == 'bank' and evidence['evidence_role'] == 'support'
            else:
                require(evidence['profile_feature_id'] in features, 'unknown profile feature')
        if claim['knowledge_type'] == 'ALFA_ANALYTICS' or claim['claim_type'] == 'bank_view':
            require(bank_support, 'bank claim has no bank support')
    if 'bank_view' in result:
        require((result['bank_view'] is None) == (result['bank_view_document_version_id'] is None), 'bank view/reference mismatch')
        if result['bank_view'] is not None:
            require(any(c['claim_type'] == 'bank_view' and any(
                e['chunk_id'] in chunk_map and e['evidence_role'] == 'support'
                and chunk_map[e['chunk_id']]['source_role'] == 'bank'
                and chunk_map[e['chunk_id']]['document_version_id'] == result['bank_view_document_version_id']
                for e in c['evidence']) for c in claims), 'bank view has no matching claim/evidence')


def validate_citations(message, initial_claims, chunks):
    claim_ids = {c['id'] for c in message['claims']} | {c['id'] for c in initial_claims}
    chunk_ids = {c['chunk_id'] for c in chunks}
    orders = [c['citation_order'] for c in message['citations']]
    require(len(orders) == len(set(orders)), 'duplicate citation order')
    targets = [(c['chunk_id'], c['claim_id']) for c in message['citations']]
    require(len(targets) == len(set(targets)), 'duplicate citation target')
    for citation in message['citations']:
        require(citation['chunk_id'] is not None or citation['claim_id'] is not None, 'citation has no target')
        require(citation['claim_id'] is None or citation['claim_id'] in claim_ids, 'foreign citation claim')
        require(citation['chunk_id'] is None or citation['chunk_id'] in chunk_ids, 'unknown citation chunk')


def validate_request(request):
    if request['request_type'] == 'initial':
        require(request['parent_trace_id'] is None, 'initial request has parent trace')
    if request['parent_trace_id'] is not None:
        require(request['parent_trace_id'] != request['trace_id'], 'self-referencing parent trace')
    follow = request['follow_up']
    if follow is None:
        return
    require(request['parent_trace_id'] is not None, 'follow_up requires parent trace')
    snapshot = follow['initial_insight']
    insight = snapshot['insight']
    require(insight['id'] == follow['initial_insight_id'], 'initial insight ID mismatch')
    require(snapshot['context'] == request['context'], 'initial insight context/profile mismatch')
    require(snapshot['event'] == request['event'], 'initial insight event snapshot mismatch')
    require(insight['business_context_id'] == request['context']['business_context_id'], 'initial insight context mismatch')
    require(insight['event_id'] == request['event']['id'], 'initial insight event mismatch')
    validate_evidence(insight, request['context'], snapshot['chunks'])
    history = follow['history']
    ids = [m['id'] for m in history]
    require(len(ids) == len(set(ids)), 'duplicate history message')
    require(bool(history) and ids[-1] == follow['history_through_message_id'] == follow['request_message_id'], 'history boundary mismatch')
    require(history[-1]['role'] == 'user' and history[-1]['content'] == follow['question'], 'question/history mismatch')
    seen_claims = {c['id'] for c in insight['claims']}
    for message in history:
        if message['role'] == 'user':
            require(not message['claims'] and not message['citations'] and not message['chunks'], 'user history contains assistant evidence')
        else:
            require(not (seen_claims & {c['id'] for c in message['claims']}), 'history reuses claim ID')
            seen_claims.update(c['id'] for c in message['claims'])
            validate_evidence(message, request['context'], message['chunks'])
            validate_citations(message, insight['claims'], snapshot['chunks'] + [c for c in message['chunks'] if c['chunk_id'] not in {x['chunk_id'] for x in snapshot['chunks']}])


def validate_pair(request, response, chunks):
    semantic(request, 'AnalysisRequest')
    semantic(response, 'AnalysisResponse')
    for key in ('contract_version', 'request_operation_id', 'trace_id'):
        require(request[key] == response[key], f'{key} mismatch')
    follow = request['follow_up']
    result = response['insight'] or response['message']
    if response['status'] == 'succeeded':
        require((response['message'] is not None) == (follow is not None), 'result/request type mismatch')
    if response['message'] is not None:
        require(follow is not None, 'message outside a conversation')
        require(result['conversation_id'] == follow['conversation_id'], 'conversation mismatch')
        require(result['parent_message_id'] == follow['request_message_id'], 'parent message mismatch')
        require(result['id'] not in {m['id'] for m in follow['history']}, 'response reuses history message ID')
        original = follow['initial_insight']['insight']
        prior_ids = {c['id'] for c in original['claims']}
        prior_ids.update(c['id'] for m in follow['history'] for c in m['claims'])
        require(not (prior_ids & {c['id'] for c in result['claims']}), 'response reuses existing claim ID')
        validate_citations(result, original['claims'], chunks)
    if response['insight'] is not None:
        require(result['business_context_id'] == request['context']['business_context_id'], 'result context mismatch')
        require(result['event_id'] == request['event']['id'], 'result event mismatch')
    if result is not None:
        validate_evidence(result, request['context'], chunks)


def validate_scenario(loaded):
    chunks = loaded['chunks.ok.json']['results']
    for name in ('analysis.ok.json', 'analysis.insufficient.json', 'analysis.failed.json'):
        # These are alternative outcomes of one operation, not three stored runs.
        validate_pair(loaded['analysis.request.json'], loaded[name], chunks)
    validate_pair(loaded['follow_up.request.json'], loaded['follow_up.ok.json'], chunks)
    runs = [loaded['analysis.ok.json'], loaded['follow_up.ok.json']]
    for key in ('trace_id', 'analysis_run_id', 'request_operation_id'):
        require(len({r[key] for r in runs}) == len(runs), f'duplicate {key} across runs')
    follow = loaded['follow_up.request.json']
    require(follow['parent_trace_id'] == runs[0]['trace_id'], 'parent trace mismatch')
    require(follow['follow_up']['initial_insight']['insight'] == runs[0]['insight'], 'snapshot differs from saved insight')


def load_fixtures(full=False):
    directory = ROOT / 'fixtures/v1'
    manifest = json.loads((directory / 'manifest.json').read_text())
    loaded = {}
    if full:
        from jsonschema import Draft202012Validator, FormatChecker
        Draft202012Validator.check_schema(SCHEMA)
    for entry in manifest:
        value = json.loads((directory / entry['file']).read_text())
        check(value, SCHEMA['$defs'][entry['definition']])
        if full:
            schema = dict(SCHEMA, **{'$ref': f"#/$defs/{entry['definition']}"})
            Draft202012Validator(schema, format_checker=FormatChecker()).validate(value)
        semantic(value, entry['definition'])
        loaded[entry['file']] = value
    return loaded


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--full', action='store_true', help='Also validate Draft 2020-12 and formats with jsonschema')
    args = parser.parse_args()
    loaded = load_fixtures(full=args.full)
    validate_scenario(loaded)
    mode = 'Draft 2020-12 + formats' if args.full else 'schema subset'
    print(f'OK: {len(loaded)} fixtures; {mode}, request/response pairs, snapshots, traces and quotes checked.')


if __name__ == '__main__':
    main()
