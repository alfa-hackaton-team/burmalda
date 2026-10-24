#!/usr/bin/env python3
"""Offline reference/schema checks for the ML v1 <-> HTTP/public API boundary.

Requires requirements-validation.txt. Does not implement the runtime adapter.
"""
import copy
import json
from pathlib import Path

import yaml
from jsonschema import Draft202012Validator, FormatChecker, ValidationError
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

from check_ml_contracts import load_fixtures, require, validate_scenario

DOCS = Path(__file__).resolve().parents[2]
PATHS = [DOCS / 'api' / name for name in ('openapi.yaml', 'ml-openapi.yaml', 'schemas.yaml')]
PATHS.append(DOCS / 'ml/contracts/v1/contracts.schema.json')
PATHS.append(DOCS / 'ml/contracts/quality/v1/quality.schema.json')


def walk(node):
    if isinstance(node, dict):
        yield node
        for value in node.values():
            yield from walk(value)
    elif isinstance(node, list):
        for value in node:
            yield from walk(value)


def main():
    documents = {p.resolve().as_uri(): yaml.safe_load(p.read_text()) for p in PATHS}
    registry = Registry()
    for uri, document in documents.items():
        resource = Resource.from_contents(document, default_specification=DRAFT202012)
        registry = registry.with_resource(uri, resource)
        if '$id' in document:
            registry = registry.with_resource(document['$id'], resource)
    refs = 0
    for uri, document in documents.items():
        for node in walk(document):
            if '$ref' in node:
                # All contract references must resolve from the local registry.
                registry.resolver(uri).lookup(node['$ref'])
                refs += 1
        for schema in document.get('components', {}).get('schemas', {}).values():
            Draft202012Validator.check_schema(schema)
    api_uri = PATHS[1].resolve().as_uri()
    schemas_uri = PATHS[2].resolve().as_uri()

    def validate(uri, fragment, value):
        Draft202012Validator({'$ref': uri + '#' + fragment}, registry=registry,
                             format_checker=FormatChecker()).validate(value)

    def must_reject(uri, fragment, value):
        try:
            validate(uri, fragment, value)
        except ValidationError:
            return
        raise ValueError('invalid HTTP payload was accepted')

    fixtures = load_fixtures(full=True)
    validate_scenario(fixtures)
    for path, prefix in [('/internal/v0.1/analyses', 'analysis'), ('/internal/v0.1/follow-ups', 'follow_up')]:
        pointer = '/paths/' + path.replace('~', '~0').replace('/', '~1') + '/post'
        request_schema = pointer + '/requestBody/content/application~1json/schema'
        response_schema = pointer + '/responses/200/content/application~1json/schema'
        validate(api_uri, request_schema, fixtures[prefix + '.request.json'])
        validate(api_uri, response_schema, fixtures[prefix + '.ok.json'])
        other = 'follow_up' if prefix == 'analysis' else 'analysis'
        must_reject(api_uri, request_schema, fixtures[other + '.request.json'])
    for state, file in [('SUCCEEDED', 'analysis.ok.json'), ('INSUFFICIENT_EVIDENCE', 'analysis.insufficient.json'), ('FAILED', 'analysis.failed.json')]:
        result = fixtures[file]
        operation = {'analysis_id': result['analysis_run_id'], 'state': state,
                     'trace_id': result['trace_id'], 'created_at': '2026-10-24T12:00:00Z', 'response': result}
        validate(schemas_uri, '/components/schemas/AnalysisOperation', operation)
        bad = copy.deepcopy(operation)
        bad['state'] = 'RUNNING'
        must_reject(schemas_uri, '/components/schemas/AnalysisOperation', bad)
        bad['state'] = 'FAILED' if state == 'SUCCEEDED' else 'SUCCEEDED'
        must_reject(schemas_uri, '/components/schemas/AnalysisOperation', bad)
    for file, definition in [('feed.json', 'FeedPage'), ('insight-limited.json', 'Insight'),
                             ('evidence-package.json', 'EvidencePackage'), ('insight-ml-v1.json', 'Insight')]:
        example = json.loads((DOCS / 'api/examples' / file).read_text())
        validate(schemas_uri, '/components/schemas/' + definition, example)
    projection = json.loads((DOCS / 'api/examples/insight-ml-v1.json').read_text())
    internal = fixtures['analysis.ok.json']['insight']['claims'][0]
    public = projection['response']['claims'][0]
    for origin, target in [('id', 'claim_id'), ('claim_text', 'text'), ('knowledge_type', 'claim_type'),
                           ('verification_status', 'verification_status'), ('assumptions', 'assumptions')]:
        require(internal[origin] == public[target], f'public projection mismatch: {target}')
    require(internal['claim_type'] == 'action' and public['claim_type'] == 'AI_INFERENCE', 'claim axes were conflated')
    require(any(s['section_type'] == 'SCENARIOS_ACTIONS' and internal['id'] in s['claim_ids'] for s in projection['response']['sections']), 'action section missing')
    pointer = '/paths/~1internal~1v0.1~1article-processings/post/requestBody/content/application~1json/schema'
    request = json.loads((DOCS / 'api/examples/article-processing.request.json').read_text())
    validate(api_uri, pointer, request)
    legacy = copy.deepcopy(request)
    legacy['article']['metadata'] = {}
    must_reject(api_uri, pointer, legacy)
    for state in ('running', 'succeeded', 'failed'):
        value = json.loads((DOCS / f'api/examples/article-processing.{state}.json').read_text())
        validate(schemas_uri, '/components/schemas/ArticleProcessingOperation', value)
    value = json.loads((DOCS / 'api/examples/article-processing.too-large.json').read_text())
    validate(schemas_uri, '/components/schemas/Error', value)
    print(f'OK: {refs} local references, HTTP request/response schemas, operation states, '
          '4 public fixtures, claim projection and 5 article-processing examples.')


if __name__ == '__main__':
    main()
