"""R02 unit/integration acceptance; all inputs are fixed or generated locally."""
import json
import unittest
from copy import deepcopy
from dataclasses import FrozenInstanceError, replace
from pathlib import Path
from unittest.mock import patch

from ml.adapters.backend_documents import document_from_backend
from ml.ingestion.dto import ExtractionMetadata, QualityIssue, QualityResult
from ml.ingestion.quality import (
    MAX_TEXT_BYTES, QUALITY_RULES_VERSION,
    check_document,
)
from ml.ingestion.validation import DocumentInputError, validate

ROOT = Path(__file__).resolve().parents[2]
CORPUS = ROOT / 'ml/tests/fixtures/corpus'

# Contract expectations, independent of runtime policy tables.
BLOCKING_ERRORS = (
    ('TEXT_TRUNCATED', 'EXTRACTION_TEXT_TRUNCATED'),
    ('MISSING_PAGES', 'EXTRACTION_MISSING_PAGES'),
    ('MISSING_TABLES', 'EXTRACTION_MISSING_TABLES'),
    ('MISSING_FOOTNOTES', 'EXTRACTION_MISSING_FOOTNOTES'),
    ('EXTRACTION_FAILED', 'EXTRACTION_FAILED'),
)
NONBLOCKING_ERRORS = (
    ('LAYOUT_LOSS', 'EXTRACTION_LAYOUT_LOSS'),
    ('READING_ORDER_UNCERTAIN', 'EXTRACTION_READING_ORDER_UNCERTAIN'),
)


def fixture(name='news'):
    return json.loads((CORPUS / (name + '.json')).read_text(encoding='utf-8'))


def codes(issues):
    return [issue.code for issue in issues]


class DocumentQualityTests(unittest.TestCase):
    def setUp(self):
        self.payload = fixture()
        self.document = document_from_backend(self.payload)

    def assess(self, **updates):
        return check_document(replace(self.document, **updates))

    def test_fixed_corpus_backend_to_gate_to_result(self):
        for case in json.loads((CORPUS / 'manifest.json').read_text()):
            with self.subTest(file=case['file']):
                payload = json.loads((CORPUS / case['file']).read_text())
                document = document_from_backend(payload)
                result = check_document(document)
                self.assertEqual(result.status, case['status'])
                self.assertEqual(codes(result.failures), case['failures'])
                self.assertEqual(codes(result.warnings), case['warnings'])
                self.assertEqual(result.document_id, payload['article']['article_id'])
                self.assertEqual(result.document_version_id, payload['article']['article_version_id'])
                self.assertEqual(result.pipeline_version, payload['pipeline_version'])
                self.assertEqual(result.quality_rules_version, QUALITY_RULES_VERSION)
                validate(result.to_dict(), 'QualityResult')

    def test_published_backend_example_is_target_input(self):
        payload = json.loads((ROOT / 'docs/api/examples/article-processing.request.json').read_text())
        result = check_document(document_from_backend(payload))
        self.assertEqual(result.status, 'PASS')
        self.assertEqual(codes(result.warnings), ['EMPTY_TITLE', 'UNKNOWN_PUBLICATION_DATE'])

    def test_empty_or_unicode_whitespace_text_fails(self):
        for text in ('', ' \n\t', '\u2003\u00a0'):
            with self.subTest(text=repr(text)):
                result = self.assess(normalized_text=text)
                self.assertEqual(result.status, 'FAIL')
                self.assertEqual(codes(result.failures), ['EMPTY_TEXT'])

    def test_utf8_byte_limit_exact_and_one_over(self):
        texts = [('a' * MAX_TEXT_BYTES, 'PASS'), ('a' * (MAX_TEXT_BYTES + 1), 'FAIL'),
                 ('я' * (MAX_TEXT_BYTES // 2), 'PASS'),
                 ('я' * (MAX_TEXT_BYTES // 2) + 'a', 'FAIL'),
                 ('😀' * (MAX_TEXT_BYTES // 4), 'PASS'),
                 ('😀' * (MAX_TEXT_BYTES // 4) + 'a', 'FAIL')]
        for text, status in texts:
            with self.subTest(size=len(text.encode('utf-8'))):
                result = self.assess(normalized_text=text)
                self.assertEqual(result.status, status)
                self.assertEqual(result.text_utf8_bytes, len(text.encode('utf-8')))
                self.assertEqual(codes(result.failures), [] if status == 'PASS' else ['TEXT_TOO_LARGE'])

    def test_title_does_not_count_toward_text_limit(self):
        result = self.assess(title='я' * MAX_TEXT_BYTES, normalized_text='a')
        self.assertEqual(result.status, 'PASS')
        self.assertEqual(result.text_utf8_bytes, 1)

    def test_empty_title_and_unknown_publication_date_only_warn(self):
        result = self.assess(title=' \n', published_at=None)
        self.assertEqual(result.status, 'PASS')
        self.assertEqual(codes(result.warnings), ['EMPTY_TITLE', 'UNKNOWN_PUBLICATION_DATE'])
        self.assertEqual(result.failures, ())

    def test_absent_and_explicit_null_date_have_same_meaning(self):
        absent = fixture('rss_reviewed')
        explicit = deepcopy(absent)
        explicit['article']['published_at'] = None
        self.assertEqual(document_from_backend(absent), document_from_backend(explicit))
        self.assertEqual(check_document(document_from_backend(absent)),
                         check_document(document_from_backend(explicit)))

    def test_canonical_text_ids_dates_and_provenance_preserved(self):
        payload = deepcopy(self.payload)
        payload['article']['body'] = '  Условие: не меняется.\n\nТаблица: 1\t2.  '
        payload['article']['published_at'] = '2026-10-20T12:00:00+03:00'
        payload['article']['external_id'] = 'source-item-1'
        payload['article']['author'] = 'Автор'
        document = document_from_backend(payload)
        self.assertEqual(document.normalized_text, payload['article']['body'])
        self.assertEqual(document.published_at, payload['article']['published_at'])
        self.assertEqual(document.retrieved_at, payload['article']['fetched_at'])
        self.assertEqual(document.raw_ref, payload['article']['raw_ref'])
        self.assertEqual(document.source.source_id, payload['source']['source_id'])
        self.assertEqual(document.source.config_version, payload['source']['config_version'])
        self.assertEqual(document.canonical_url, payload['article']['canonical_url'])
        self.assertEqual(document.external_id, 'source-item-1')
        self.assertEqual(document.author, 'Автор')
        self.assertEqual(document.trace_id, payload['trace_id'])
        self.assertEqual(check_document(document).status, 'PASS')

    def test_hash_is_opaque_never_recomputed_or_rewritten(self):
        payload = deepcopy(self.payload)
        payload['article']['content_hash'] = 'f' * 64
        payload['article']['body'] = 'Changed text; backend owns its hash.'
        with patch('hashlib.sha256', side_effect=AssertionError('Hash recomputation is out of R02')):
            document = document_from_backend(payload)
            result = check_document(document)
        self.assertEqual(document.content_hash, 'f' * 64)
        self.assertEqual(result.status, 'PASS')

    def test_all_methods_are_accepted_and_not_completeness_proof(self):
        for method in ('full_article', 'rss_summary', 'pdf_extracted'):
            with self.subTest(method=method):
                result = self.assess(extraction=ExtractionMetadata(method, ()))
                self.assertEqual(result.status, 'PASS')
                self.assertEqual(codes(result.warnings), ['RSS_SUMMARY'] if method == 'rss_summary' else [])
                self.assertNotIn('is_complete', result.to_dict())

    def test_each_blocking_extraction_error_fails(self):
        for backend_code, result_code in BLOCKING_ERRORS:
            with self.subTest(code=backend_code):
                result = self.assess(extraction=ExtractionMetadata('pdf_extracted', (backend_code,)))
                self.assertEqual(result.status, 'FAIL')
                self.assertEqual(codes(result.failures), [result_code])

    def test_each_nonblocking_extraction_error_warns(self):
        for backend_code, result_code in NONBLOCKING_ERRORS:
            with self.subTest(code=backend_code):
                result = self.assess(extraction=ExtractionMetadata('pdf_extracted', (backend_code,)))
                self.assertEqual(result.status, 'PASS')
                self.assertEqual(codes(result.warnings), [result_code])

    def test_all_diagnostics_collected_in_stable_order(self):
        errors = tuple(row[0] for row in BLOCKING_ERRORS + NONBLOCKING_ERRORS)
        a = self.assess(normalized_text='a\0\ufffd', title='', published_at=None,
                        extraction=ExtractionMetadata('rss_summary', errors))
        b = self.assess(normalized_text='a\0\ufffd', title='', published_at=None,
                        extraction=ExtractionMetadata('rss_summary', tuple(reversed(errors))))
        self.assertEqual(a, b)
        self.assertEqual(codes(a.failures), ['NUL_IN_TEXT'] + [r[1] for r in BLOCKING_ERRORS])
        self.assertEqual(codes(a.warnings), ['EMPTY_TITLE', 'UNKNOWN_PUBLICATION_DATE', 'RSS_SUMMARY']
                         + [r[1] for r in NONBLOCKING_ERRORS] + ['UNICODE_REPLACEMENT_CHARACTER'])

    def test_source_aliases_are_canonical_and_original_values_remain(self):
        payload = fixture('rss_reviewed')
        document = document_from_backend(payload)
        self.assertEqual(document.source.source_type, 'news')
        self.assertEqual(document.source.backend_source_type, 'financial_press')
        self.assertEqual(document.source.trust_level, 'unknown')
        self.assertEqual(document.source.backend_trust_level, 'reviewed')
        self.assertIn('SOURCE_TRUST_UNRATED', codes(check_document(document).warnings))
        self.assertEqual(payload['source']['trust_level'], 'reviewed')

    def test_canonical_source_categories_are_preserved(self):
        for kind in ('news', 'bank_analytics', 'expert', 'regulator'):
            for trust in ('primary', 'trusted', 'standard', 'unknown'):
                payload = deepcopy(self.payload)
                payload['source'].update(source_type=kind, trust_level=trust)
                doc = document_from_backend(payload)
                self.assertEqual(doc.source.source_type, kind)
                self.assertEqual(doc.source.trust_level, trust)
                self.assertEqual(check_document(doc).warnings, ())

    def test_bank_source_does_not_bypass_quality(self):
        doc = document_from_backend(fixture('bank_pdf'))
        self.assertEqual(check_document(replace(doc, normalized_text=' ')).status, 'FAIL')
        self.assertEqual(doc.source.source_role, 'bank')

    def test_bad_target_metadata_rejected_without_legacy_fallback(self):
        invalid = [{}, {'extraction_method': 'full_article'},
                   {'extraction_errors': []},
                   {'extraction_method': 'unknown', 'extraction_errors': []},
                   {'extraction_method': 'pdf_extracted', 'extraction_errors': ['UNKNOWN_ERROR']},
                   {'extraction_method': 'pdf_extracted', 'extraction_errors': ['LAYOUT_LOSS', 'LAYOUT_LOSS']},
                   {'extraction_method': 'pdf_extracted', 'extraction_errors': None},
                   {'extraction_method': 'pdf_extracted', 'extraction_errors': [], 'is_complete': True}]
        for metadata in invalid:
            with self.subTest(metadata=metadata), self.assertRaises(DocumentInputError):
                document_from_backend({**self.payload, 'article': {**self.payload['article'], 'metadata': metadata}})

    def test_invalid_identity_and_provenance_rejected(self):
        for key, value in [('article_id', 'bad'), ('article_version_id', 'bad'),
                           ('source_id', '20000000-0000-4000-8000-999999999999'),
                           ('raw_ref', 'https://example.org/file.pdf'),
                           ('raw_ref', 'raw-artifact:bad'), ('content_hash', 'bad'),
                           ('fetched_at', '2026-10-24T12:00:00'), ('published_at', 'unknown'),
                           ('canonical_url', 'not a URI'), ('title', None), ('body', None)]:
            with self.subTest(field=key), self.assertRaises(DocumentInputError):
                document_from_backend({**self.payload, 'article': {**self.payload['article'], key: value}})
        for key, value in [('source_type', 'arbitrary'), ('trust_level', 'reviewed_is_trusted'),
                           ('source_role', 'admin'), ('config_version', '0'), ('config_version', 1),
                           ('name', ' '), ('endpoint', 'https://publisher.example')]:
            with self.subTest(field=key), self.assertRaises(DocumentInputError):
                document_from_backend({**self.payload, 'source': {**self.payload['source'], key: value}})

    def test_required_fields_and_unknown_root_fields(self):
        for key in self.payload:
            payload = deepcopy(self.payload)
            del payload[key]
            with self.subTest(root=key), self.assertRaises(DocumentInputError):
                document_from_backend(payload)
        for key in ('article_id', 'version_no', 'article_version_id', 'source_id', 'title', 'body',
                    'fetched_at', 'raw_ref', 'content_hash', 'metadata'):
            payload = deepcopy(self.payload)
            del payload['article'][key]
            with self.subTest(article=key), self.assertRaises(DocumentInputError):
                document_from_backend(payload)
        with self.assertRaises(DocumentInputError):
            document_from_backend({**self.payload, 'legacy_mode': True})
        with self.assertRaises(DocumentInputError):
            document_from_backend({**self.payload, 'pipeline_version': ' '})

    def test_nul_failure_and_replacement_warning_preserve_text(self):
        result = self.assess(normalized_text='a\x00b\ufffd')
        self.assertEqual(result.status, 'FAIL')
        self.assertEqual(codes(result.failures), ['NUL_IN_TEXT'])
        self.assertEqual(codes(result.warnings), ['UNICODE_REPLACEMENT_CHARACTER'])

    def test_invalid_utf8_fails_safely_without_repair(self):
        result = self.assess(normalized_text='a\ud800b')
        self.assertEqual(result.status, 'FAIL')
        self.assertEqual(codes(result.failures), ['INVALID_UTF8_TEXT'])
        self.assertIsNone(result.text_utf8_bytes)
        json.dumps(result.to_dict(), allow_nan=False)

    def test_determinism_and_original_input_isolation(self):
        before = deepcopy(self.payload)
        document = document_from_backend(self.payload)
        first = check_document(document)
        self.assertEqual(first, check_document(document))
        self.assertEqual(self.payload, before)
        self.payload['article']['body'] = 'mutated'
        self.payload['article']['metadata']['extraction_errors'].append('MISSING_TABLES')
        self.payload['source']['name'] = 'mutated'
        self.assertEqual(first, check_document(document))
        serialized = document.to_dict()
        serialized['source']['name'] = 'mutated'
        serialized['extraction']['extraction_errors'].append('MISSING_TABLES')
        serialized_result = first.to_dict()
        serialized_result['warnings'].append({'code': 'fake'})
        self.assertEqual(first, check_document(document))

    def test_typed_documents_and_results_are_immutable(self):
        result = check_document(self.document)
        for obj, attr, value in [(self.document, 'normalized_text', 'changed'),
                                 (self.document.source, 'trust_level', 'trusted'),
                                 (self.document.extraction, 'extraction_errors', ('MISSING_PAGES',)),
                                 (result, 'status', 'FAIL')]:
            with self.subTest(obj=type(obj).__name__), self.assertRaises(FrozenInstanceError):
                setattr(obj, attr, value)

    def test_direct_typed_construction_validates_as_well(self):
        with self.assertRaises(DocumentInputError):
            replace(self.document, document_version_id='bad')
        with self.assertRaises(DocumentInputError):
            replace(self.document.source, trust_level='trusted', backend_trust_level='reviewed')
        with self.assertRaises(DocumentInputError):
            ExtractionMetadata('pdf_extracted', [])
        with self.assertRaises(TypeError):
            check_document(self.payload)

    def test_quality_result_schema_and_status_invariants(self):
        passed = check_document(self.document)
        self.assertNotIn('SUCCEEDED', json.dumps(passed.to_dict()))
        self.assertNotIn('ready', passed.to_dict())
        self.assertEqual(set(passed.to_dict()), {'document_id', 'document_version_id',
                         'pipeline_version', 'quality_rules_version', 'status',
                         'failures', 'warnings', 'text_utf8_bytes'})
        with self.assertRaises(DocumentInputError):
            replace(passed, status='FAIL')
        with self.assertRaises(DocumentInputError):
            replace(passed, failures=(QualityIssue('EMPTY_TEXT', 'normalized_text', 'Empty'),))
        with self.assertRaises(DocumentInputError):
            replace(passed, warnings=(QualityIssue('EMPTY_TEXT', 'normalized_text', 'Empty'),))

    def test_result_messages_do_not_expose_input_contents(self):
        doc = replace(self.document, normalized_text='secret document\0', title='')
        serialized = json.dumps(check_document(doc).to_dict())
        self.assertNotIn('secret document', serialized)
        payload = deepcopy(self.payload)
        payload['source']['trust_level'] = 'secret invalid category'
        with self.assertRaises(DocumentInputError) as caught:
            document_from_backend(payload)
        self.assertNotIn('secret invalid category', str(caught.exception))

    def test_quality_has_no_network_storage_or_hash_calls(self):
        with patch('socket.socket', side_effect=AssertionError('Network is out of scope')), \
             patch('builtins.open', side_effect=AssertionError('Runtime file I/O is out of scope')), \
             patch('hashlib.sha256', side_effect=AssertionError('Hashing is out of scope')):
            result = check_document(document_from_backend(self.payload))
        self.assertEqual(result.status, 'PASS')

    def test_does_not_guess_dates_titles_or_stale_bank_policy(self):
        doc = replace(document_from_backend(fixture('bank_pdf')), title='', published_at='2021-01-01T00:00:00Z')
        result = check_document(doc)
        self.assertEqual(result.status, 'PASS')
        self.assertEqual(doc.title, '')
        self.assertEqual(codes(result.warnings), ['EMPTY_TITLE'])
        # Temporal applicability is a downstream decision, never inferred by R02.
        self.assertEqual(doc.published_at, '2021-01-01T00:00:00Z')


if __name__ == '__main__':
    unittest.main()
