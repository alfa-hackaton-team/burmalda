"""R01 contract acceptance: wire forms, filters, access, versions and failures."""
import json
import unittest
from copy import deepcopy
from pathlib import Path
from typing import get_type_hints

from ml.adapters.in_memory import Candidate, DocumentVersion, InMemoryRetriever, QueryFixture
from ml.shared import dto
from ml.shared.validation import ContractValidationError, validate, validate_pair

ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / 'docs/ml/fixtures/v1'


def load(name):
    return json.loads((FIXTURES / name).read_text())


def uid(n):
    return f'10000000-0000-4000-8000-{n:012d}'


class RetrievalTests(unittest.TestCase):
    def setUp(self):
        self.request = load('search.request.json')
        self.chunk_request = load('chunks.request.json')
        self.chunk = load('chunks.ok.json')['results'][0]
        self.context = self.request['business_context_id']
        self.adapter = InMemoryRetriever.from_fixtures()

    def configured(self, chunks=None, versions=None, queries=None, allowed=None, **kwargs):
        chunks = [self.chunk] if chunks is None else chunks
        if versions is None:
            versions = list({c['document_version_id']: DocumentVersion(c['document_id'], c['document_version_id'], 1)
                             for c in chunks}.values())
        if queries is None:
            queries = {self.request['query']: QueryFixture(tuple(Candidate(c['chunk_id'], .5) for c in chunks))}
        if allowed is None:
            allowed = [c['chunk_id'] for c in chunks]
        return InMemoryRetriever(chunks=chunks, versions=versions, queries=queries,
                                 context_chunks={self.context: allowed}, **kwargs)

    def unrestricted(self):
        r = deepcopy(self.request)
        r['filters']['document_version_ids'] = []
        return r

    def other_chunk(self, n=1, **updates):
        return {**self.chunk, 'chunk_id': uid(n), 'document_id': uid(n + 100),
                'document_version_id': uid(n + 200), **updates}

    def test_canonical_happy_path_and_provenance(self):
        result = self.adapter.search(self.request)
        expected = load('search.ok.json')
        expected['retrieval_config_version'] = 'r01-fixture-v1'
        self.assertEqual(result, expected)
        self.assertEqual(self.adapter.get_chunks(self.chunk_request), load('chunks.ok.json'))
        validate_pair(self.request, result, 'search')
        self.assertEqual({k: v for k, v in result['results'][0].items()
                          if k not in ('rank', 'retrieval_score')}, self.chunk)

    def test_unknown_query_is_empty_not_error(self):
        self.request['query'] = 'нет такого fixture-запроса'
        result = self.adapter.search(self.request)
        self.assertEqual(result['status'], 'empty')
        self.assertEqual(result['results'], [])
        self.assertIsNone(result['error'])

    def test_queries_match_exactly_except_outer_whitespace(self):
        self.request['query'] = '  ' + self.request['query'] + '\n'
        self.assertEqual(self.adapter.search(self.request)['status'], 'ok')

    def test_request_validation(self):
        changes = [
            ('top_k', 0), ('top_k', 51), ('top_k', True), ('top_k', 1.0),
            ('trace_id', 'not-a-uuid'), ('business_context_id', 'bad'),
            ('query', ''), ('query', ' \n\t'), ('query', 42),
            ('filters', None), ('extra', True),
        ]
        for key, value in changes:
            with self.subTest(key=key, value=value):
                request = {**self.request, key: value}
                with self.assertRaises(ContractValidationError):
                    self.adapter.search(request)
        for key in self.request:
            request = deepcopy(self.request)
            del request[key]
            with self.subTest(missing=key), self.assertRaises(ContractValidationError):
                self.adapter.search(request)

    def test_unsupported_version_is_explicit(self):
        self.request['contract_version'] = 'v2'
        with self.assertRaises(ContractValidationError) as failure:
            self.adapter.search(self.request)
        self.assertEqual(failure.exception.code, 'UNSUPPORTED_VERSION')

    def test_get_chunks_request_limits_and_extra_fields(self):
        for ids in ([], [uid(1)] * 101, ['bad']):
            with self.subTest(ids=ids), self.assertRaises(ContractValidationError):
                self.adapter.get_chunks({**self.chunk_request, 'chunk_ids': ids})
        with self.assertRaises(ContractValidationError):
            self.adapter.get_chunks({**self.chunk_request, 'is_admin': True})

    def test_invalid_dates_and_filter_shape(self):
        for changes in (
            {'published_from': '2026-10-20T09:00:00'},
            {'published_from': '2026-13-20T09:00:00Z'},
            {'published_from': '2026-10-20T09:00:00Z', 'published_to': '2026-10-20T09:00:00Z'},
            {'published_from': '2026-10-21T00:00:00Z', 'published_to': '2026-10-20T00:00:00Z'},
            {'source_ids': ['bad']}, {'source_roles': ['admin']}, {'extra': 'x'},
        ):
            with self.subTest(changes=changes), self.assertRaises(ContractValidationError):
                self.adapter.search({**self.request, 'filters': {**self.request['filters'], **changes}})
        for key in self.request['filters']:
            filters = deepcopy(self.request['filters'])
            del filters[key]
            with self.subTest(missing=key), self.assertRaises(ContractValidationError):
                self.adapter.search({**self.request, 'filters': filters})

    def test_date_interval_is_inclusive_exclusive_and_timezone_aware(self):
        for start, end, expected in [
            ('2026-10-20T12:00:00+03:00', None, 'ok'),
            (None, '2026-10-20T12:00:00+03:00', 'empty'),
            ('2026-10-20T09:00:01Z', None, 'empty'),
            (None, '2026-10-20T09:00:01Z', 'ok'),
        ]:
            with self.subTest(start=start, end=end):
                request = deepcopy(self.request)
                request['filters'].update(published_from=start, published_to=end)
                self.assertEqual(self.adapter.search(request)['status'], expected)

    def test_undated_semantics(self):
        adapter = self.configured(chunks=[{**self.chunk, 'published_at': None}])
        for bounded in (False, True):
            for include in (False, True):
                request = deepcopy(self.request)
                request['filters'].update(include_undated=include,
                                          published_from='2026-01-01T00:00:00Z' if bounded else None)
                result = adapter.search(request)
                self.assertEqual(result['status'], 'ok' if not bounded or include else 'empty')
                if result['results']:
                    self.assertIsNone(result['results'][0]['published_at'])

    def test_filter_lists_or_and_between_filters(self):
        bank = self.other_chunk(source_id=uid(501), source_role='bank', source_type='bank_analytics')
        expert = self.other_chunk(2, source_id=uid(502), source_role='expert', source_type='expert')
        adapter = self.configured(chunks=[self.chunk, bank, expert])
        request = self.unrestricted()
        request['filters']['source_ids'] = [self.chunk['source_id'], bank['source_id']]
        request['filters']['source_roles'] = ['external', 'bank']
        self.assertEqual(len(adapter.search(request)['results']), 2)
        request['filters']['source_roles'] = ['expert']
        self.assertEqual(adapter.search(request)['status'], 'empty')
        request['filters']['source_ids'] = []
        self.assertEqual(adapter.search(request)['results'][0]['chunk_id'], expert['chunk_id'])
        request['filters']['document_version_ids'] = [self.chunk['document_version_id'], bank['document_version_id']]
        self.assertEqual(adapter.search(request)['status'], 'empty')

    def test_top_k_stable_ranking_and_null_scores(self):
        chunks = [self.other_chunk(n) for n in range(1, 5)]
        candidates = (Candidate(chunks[3]['chunk_id'], None), Candidate(chunks[2]['chunk_id'], -2),
                      Candidate(chunks[1]['chunk_id'], 4.2), Candidate(chunks[0]['chunk_id'], 4.2))
        adapter = self.configured(chunks=chunks, queries={self.request['query']: QueryFixture(candidates)})
        request = self.unrestricted()
        result = adapter.search(request)
        self.assertEqual([c['chunk_id'] for c in result['results']], [c['chunk_id'] for c in chunks])
        self.assertEqual([c['rank'] for c in result['results']], [1, 2, 3, 4])
        self.assertIsNone(result['results'][-1]['retrieval_score'])
        request['top_k'] = 2
        self.assertEqual(adapter.search(request)['results'], result['results'][:2])
        self.assertEqual(adapter.search(request)['status'], 'ok')

    def test_versions_latest_before_readiness_and_historical_reading(self):
        old = self.chunk
        new = self.other_chunk(document_id=old['document_id'])
        for ready in (False, True):
            versions = [DocumentVersion(old['document_id'], old['document_version_id'], 1),
                        DocumentVersion(new['document_id'], new['document_version_id'], 2, ready)]
            adapter = self.configured(chunks=[old, new], versions=versions)
            result = adapter.search(self.unrestricted())
            self.assertEqual([c['chunk_id'] for c in result['results']], [new['chunk_id']] if ready else [])
            self.assertEqual(adapter.search(self.request)['results'][0]['chunk_id'], old['chunk_id'])
            self.assertEqual(adapter.get_chunks(self.chunk_request)['results'], [old])

    def test_latest_version_without_chunks_still_hides_old_search_result(self):
        versions = [DocumentVersion(self.chunk['document_id'], self.chunk['document_version_id'], 1),
                    DocumentVersion(self.chunk['document_id'], uid(999), 2, False)]
        adapter = self.configured(versions=versions)
        self.assertEqual(adapter.search(self.unrestricted())['status'], 'empty')
        self.assertEqual(adapter.get_chunks(self.chunk_request)['status'], 'ok')

    def test_get_chunks_order_deduplication_and_statuses(self):
        second = self.other_chunk()
        adapter = self.configured(chunks=[self.chunk, second])
        ids = [second['chunk_id'], uid(999), self.chunk['chunk_id'], second['chunk_id'], uid(999)]
        request = {**self.chunk_request, 'chunk_ids': ids}
        result = adapter.get_chunks(request)
        self.assertEqual(result['status'], 'partial')
        self.assertEqual(result['results'], [second, self.chunk])
        self.assertEqual(result['unresolved_chunk_ids'], [uid(999)])
        validate_pair(request, result, 'get_chunks')
        result = adapter.get_chunks({**request, 'chunk_ids': [uid(999), uid(998), uid(999)]})
        self.assertEqual(result['status'], 'empty')
        self.assertEqual(result['unresolved_chunk_ids'], [uid(999), uid(998)])

    def test_access_denied_and_missing_indistinguishable(self):
        denied = self.other_chunk()
        adapter = self.configured(chunks=[self.chunk, denied], allowed=[self.chunk['chunk_id']])
        request = {**self.chunk_request, 'chunk_ids': [denied['chunk_id'], uid(999)]}
        result = adapter.get_chunks(request)
        self.assertEqual(result['unresolved_chunk_ids'], request['chunk_ids'])
        self.assertEqual(result['status'], 'empty')
        self.assertEqual(adapter.search(self.unrestricted())['results'][0]['chunk_id'], self.chunk['chunk_id'])
        empty_access = self.configured(allowed=[])
        self.assertEqual(empty_access.search(self.request)['status'], 'empty')

    def test_unknown_context_and_service_require_trusted_configuration(self):
        for context in (uid(999), None):
            for method, request in ((self.adapter.search, self.request), (self.adapter.get_chunks, self.chunk_request)):
                result = method({**request, 'business_context_id': context})
                self.assertEqual(result['status'], 'error')
                self.assertEqual(result['error']['code'], 'CONTEXT_MISMATCH')
                self.assertEqual(result['results'], [])
        service = self.configured(service_chunk_ids=[self.chunk['chunk_id']])
        self.assertEqual(service.search({**self.request, 'business_context_id': None})['status'], 'ok')
        self.assertEqual(service.get_chunks({**self.chunk_request, 'business_context_id': None})['status'], 'ok')

    def test_partial_search_requires_known_limitation_not_short_result(self):
        query = QueryFixture((Candidate(self.chunk['chunk_id'], .5),), True, ('Fixture source unavailable',))
        adapter = self.configured(queries={self.request['query']: query})
        result = adapter.search(self.request)
        self.assertEqual(result['status'], 'partial')
        self.assertTrue(result['warnings'])
        self.request['filters']['source_roles'] = ['bank']
        result = adapter.search(self.request)
        self.assertEqual(result['status'], 'empty')
        self.assertTrue(result['warnings'])
        self.assertIsNone(result['error'])

    def test_controlled_technical_failures_are_safe_and_valid(self):
        for code in ('SEARCH_UNAVAILABLE', 'TIMEOUT'):
            failure = {'code': code, 'message': 'secret private SQL', 'retryable': True}
            adapter = self.configured(failures={'search': failure, 'get_chunks': failure})
            for operation, request in (('search', self.request), ('get_chunks', self.chunk_request)):
                result = getattr(adapter, operation)(request)
                self.assertEqual(result['status'], 'error')
                self.assertEqual(result['error']['code'], code)
                self.assertTrue(result['error']['retryable'])
                self.assertNotIn('secret', json.dumps(result))
                self.assertEqual(result['results'], [])
                validate_pair(request, result, operation)
                if operation == 'get_chunks':
                    self.assertEqual(result['unresolved_chunk_ids'], [])

    def test_trace_propagated_without_reusing_fixture_trace(self):
        for operation, request in (('search', self.request), ('get_chunks', self.chunk_request)):
            request = {**request, 'trace_id': uid(999)}
            self.assertEqual(getattr(self.adapter, operation)(request)['trace_id'], uid(999))

    def test_input_output_and_configuration_isolation(self):
        chunks, request = [deepcopy(self.chunk)], deepcopy(self.request)
        adapter = self.configured(chunks=chunks)
        chunks[0]['chunk_text'] = 'mutated configuration'
        result = adapter.search(request)
        result['results'][0]['chunk_text'] = 'mutated response'
        result['warnings'].append('mutated warning')
        self.assertEqual(adapter.get_chunks(self.chunk_request)['results'], [self.chunk])
        self.assertEqual(adapter.search(request)['results'][0]['chunk_text'], self.chunk['chunk_text'])
        self.assertEqual(request, self.request)

    def test_invalid_corpus_fails_at_construction(self):
        with self.assertRaises(ValueError):
            self.configured(chunks=[self.chunk, self.chunk])
        with self.assertRaises(ValueError):
            self.configured(versions=[])
        with self.assertRaises(ValueError):
            self.configured(allowed=[uid(999)])
        for score in (float('nan'), float('inf'), float('-inf'), True):
            with self.subTest(score=score), self.assertRaises(ContractValidationError):
                self.configured(queries={self.request['query']: QueryFixture((Candidate(self.chunk['chunk_id'], score),))})
        with self.assertRaises(ValueError):
            self.configured(queries={self.request['query']: QueryFixture(incomplete=True)})
        with self.assertRaises(ValueError):
            self.configured(queries={self.request['query']: QueryFixture((Candidate(uid(999), .5),))})
        with self.assertRaises(ValueError):
            self.configured(versions=[DocumentVersion(self.chunk['document_id'], self.chunk['document_version_id'], 1),
                                      DocumentVersion(self.chunk['document_id'], uid(999), 1)])

    def test_empty_corpus(self):
        adapter = self.configured(chunks=[], queries={})
        self.assertEqual(adapter.search(self.request)['status'], 'empty')
        self.assertEqual(adapter.get_chunks(self.chunk_request)['status'], 'empty')

    def test_response_validation_rejects_inconsistent_results(self):
        original = self.adapter.search(self.request)
        for update in ({'status': 'empty'}, {'status': 'partial'}, {'error': {'code': 'TIMEOUT', 'message': 'x', 'retryable': True}},
                       {'trace_id': uid(999)}, {'results': [*original['results'], *original['results']]}):
            with self.subTest(update=update), self.assertRaises(ContractValidationError):
                validate_pair(self.request, {**original, **update}, 'search')
        response = self.adapter.get_chunks(self.chunk_request)
        with self.assertRaises(ContractValidationError):
            validate_pair(self.chunk_request, {**response, 'unresolved_chunk_ids': [uid(999)]}, 'get_chunks')

    def test_follow_up_snapshot_chunks_remain_addressable(self):
        follow_up = load('follow_up.request.json')
        snapshot = follow_up['follow_up']['initial_insight']
        request = {
            'contract_version': 'v1', 'trace_id': follow_up['trace_id'],
            'business_context_id': follow_up['context']['business_context_id'],
            'chunk_ids': [c['chunk_id'] for c in snapshot['chunks']],
        }
        result = self.adapter.get_chunks(request)
        self.assertEqual(result['status'], 'ok')
        self.assertEqual(result['results'], snapshot['chunks'])

    def test_diagnostics_do_not_log_query_or_document_contents(self):
        with self.assertLogs('ml.adapters.in_memory', level='INFO') as captured:
            self.adapter.search(self.request)
            self.adapter.get_chunks(self.chunk_request)
        for record in captured.records:
            self.assertEqual(record.trace_id, self.request['trace_id'])
            self.assertEqual(record.status, 'ok')
            self.assertEqual(record.result_count, 1)
            self.assertGreaterEqual(record.duration_ms, 0)
            self.assertEqual(record.retrieval_config_version, 'r01-fixture-v1')
            self.assertNotIn(self.request['query'], str(record.__dict__))
            self.assertNotIn(self.chunk['chunk_text'], str(record.__dict__))

    def test_all_canonical_retrieval_examples_validate(self):
        manifest = load('manifest.json')
        for item in manifest:
            if item['definition'] in ('SearchRequest', 'SearchResponse', 'GetChunksRequest', 'GetChunksResponse'):
                with self.subTest(file=item['file']):
                    validate(load(item['file']), item['definition'])

    def test_dto_required_fields_match_canonical_schema(self):
        schema = json.loads((ROOT / 'docs/ml/contracts/v1/contracts.schema.json').read_text())
        for name in ('Chunk', 'RankedChunk', 'SearchRequest', 'SearchResponse', 'GetChunksRequest', 'GetChunksResponse', 'Error'):
            with self.subTest(dto=name):
                hints = get_type_hints(getattr(dto, name))
                self.assertEqual(set(hints), set(schema['$defs'][name]['required']))


if __name__ == '__main__':
    unittest.main()
