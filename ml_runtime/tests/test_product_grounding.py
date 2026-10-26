"""Product routing must not confuse event evidence with bank context."""
from copy import deepcopy
import unittest
from uuid import uuid4

from ml_runtime.agent import AgentExecutionError, _validate_chunk_set
from ml_runtime.product_corpus import ProductSearchPlanner, ProductContextRetriever, event_topic
from ml_runtime.tests.test_analysis_contract import load_fixture


class ProductGroundingTests(unittest.TestCase):
    def test_monetary_decisions_are_financing(self):
        for value in ('monetary policy decision', 'FOMC statement', 'денежно-кредитная политика'):
            self.assertEqual(event_topic(value), 'financing')
        self.assertEqual(event_topic('supply disruption'), 'logistics')

    def test_bank_query_has_its_own_trusted_versions_and_source_role(self):
        request = load_fixture('analysis.request.json')
        original = deepcopy(request)
        bank_version = str(uuid4())
        event, bank = ProductSearchPlanner([bank_version]).plan(request)
        self.assertEqual(event['filters']['document_version_ids'], request['event']['document_version_ids'])
        self.assertEqual(bank['filters']['document_version_ids'], [bank_version])
        self.assertEqual(bank['filters']['source_roles'], ['bank'])
        self.assertEqual(event['top_k'], bank['top_k'])
        self.assertEqual(request, original)
        self.assertEqual(len(ProductSearchPlanner([]).plan(request)), 1)

    def test_outside_versions_fail_unless_explicitly_authorized_bank_context(self):
        request = load_fixture('analysis.request.json')
        chunk = deepcopy(load_fixture('chunks.ok.json')['results'][0])
        chunk['document_version_id'] = str(uuid4())
        chunk['source_role'] = 'bank'
        with self.assertRaises(AgentExecutionError):
            _validate_chunk_set(request, [chunk])
        trusted = frozenset([chunk['document_version_id']])
        _validate_chunk_set(request, [chunk], trusted)
        chunk['source_role'] = 'external'
        with self.assertRaises(AgentExecutionError):
            _validate_chunk_set(request, [chunk], trusted)

    def test_bank_routing_does_not_replace_event_search(self):
        class Retriever:
            def __init__(self, label): self.label, self.calls = label, 0
            def search(self, request): self.calls += 1; return self.label
        event, bank = Retriever('event'), Retriever('bank')
        retriever = ProductContextRetriever(event, bank, None)
        queries = ProductSearchPlanner([str(uuid4())]).plan(load_fixture('analysis.request.json'))
        self.assertEqual([retriever.search(query) for query in queries], ['event', 'bank'])
        self.assertEqual((event.calls, bank.calls), (1, 1))
