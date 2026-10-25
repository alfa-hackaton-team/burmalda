"""Chunking acceptance tests; no network or optional packages required."""
import json
import os
import random
import unittest
from dataclasses import FrozenInstanceError, replace
from pathlib import Path
from uuid import UUID

from ml.adapters.backend_documents import document_from_backend
from ml.chunking import ChunkingError, ChunkingProfile, V1, chunk_document
from ml.chunking.tokenization import QwenTokenCounter
from ml.ingestion.quality import check_document

CORPUS = Path(__file__).parent / 'fixtures/corpus'


class CharacterCounter:
    """Test-only units, deliberately never advertised as Qwen tokens."""
    counter_id = 'test-codepoints-v1'

    def count(self, text):
        return len(text)


TEST_PROFILE = ChunkingProfile('test-v1', CharacterCounter.counter_id, 24, 8, 40, 7)


def document(text=None, fixture='news'):
    saved = document_from_backend(json.loads((CORPUS / f'{fixture}.json').read_text()))
    return saved if text is None else replace(saved, normalized_text=text)


class ChunkingTests(unittest.TestCase):
    def chunks(self, text, **kwargs):
        return chunk_document(document(text), CharacterCounter(), kwargs.get('profile', TEST_PROFILE))

    def assert_invariants(self, doc, chunks, counter, profile):
        self.assertTrue(chunks)
        self.assertEqual(chunks[0].char_start, 0)
        self.assertEqual(chunks[-1].char_end, len(doc.normalized_text))
        self.assertEqual(len({c.id for c in chunks}), len(chunks))
        for i, chunk in enumerate(chunks):
            UUID(chunk.id)
            self.assertEqual(chunk.chunk_no, i)
            self.assertEqual(chunk.document_version_id, doc.document_version_id)
            self.assertEqual(chunk.chunking_profile_id, profile.profile_id)
            self.assertEqual(chunk.chunk_text, doc.normalized_text[chunk.char_start:chunk.char_end])
            self.assertTrue(chunk.chunk_text.strip())
            self.assertEqual(chunk.token_count, counter.count(chunk.chunk_text))
            self.assertLessEqual(chunk.token_count, profile.maximum)
            if i:
                previous = chunks[i - 1]
                self.assertGreater(chunk.char_start, previous.char_start)
                self.assertGreater(chunk.char_end, previous.char_end)
                self.assertLessEqual(chunk.char_start, previous.char_end)
                overlap = doc.normalized_text[chunk.char_start:previous.char_end]
                self.assertLessEqual(counter.count(overlap), profile.overlap)

    def test_small_documents_and_thresholds(self):
        for size in (1, 7, 8, 9, 23, 24, 25, 39, 40, 41, 79, 80, 81):
            doc = document('я' * size)
            self.assert_invariants(doc, self.chunks(doc.normalized_text), CharacterCounter(), TEST_PROFILE)

    def test_unicode_offsets_and_exact_whitespace(self):
        text = '  Я🙂 e\u0301.\r\n\r\n' + 'Текст «да!» Ещё?\n' * 12 + '  '
        self.assert_invariants(document(text), self.chunks(text), CharacterCounter(), TEST_PROFILE)

    def test_prefers_paragraph_boundary(self):
        text = 'A' * 18 + '\n\n' + 'B' * 18 + '\n\n' + 'C' * 18
        chunks = self.chunks(text)
        self.assertEqual(chunks[0].char_end, 20)

    def test_overlap_contains_complete_sentence(self):
        text = 'Abc. ' * 30
        chunks = self.chunks(text)
        self.assert_invariants(document(text), chunks, CharacterCounter(), TEST_PROFILE)
        self.assertTrue(any(c.char_start < p.char_end for p, c in zip(chunks, chunks[1:])))
        for p, c in zip(chunks, chunks[1:]):
            self.assertIn(text[c.char_start:p.char_end], ('', 'Abc. '))

    def test_zero_overlap_and_long_sentence(self):
        profile = replace(TEST_PROFILE, profile_id='test-no-overlap', overlap=0)
        text = 'Длинное предложение без точки ' * 50
        chunks = self.chunks(text, profile=profile)
        self.assert_invariants(document(text), chunks, CharacterCounter(), profile)
        self.assertEqual(''.join(c.chunk_text for c in chunks), text)

    def test_hard_split_long_word(self):
        chunks = self.chunks('🙂' * 101)
        self.assertEqual([c.token_count for c in chunks], [40, 40, 21])

    def test_short_tail_is_allowed(self):
        self.assertEqual([c.token_count for c in self.chunks('x' * 41)], [40, 1])

    def test_sentence_heuristics(self):
        from ml.chunking.core import _boundaries
        text = 'г. Москва. Цена 1.25. Dr. Smith! А. Иванов?'
        _, boundaries = _boundaries(text)
        self.assertNotIn(3, boundaries)
        self.assertIn(text.index('Цена'), boundaries)
        self.assertNotIn(text.index('25'), boundaries)
        self.assertNotIn(text.index('Smith'), boundaries)
        self.assertNotIn(text.index('Иванов'), boundaries)

    def test_single_crlf_is_not_a_blank_line(self):
        from ml.chunking.core import _boundaries
        for newline in ('\n', '\r', '\r\n'):
            with self.subTest(newline=newline):
                text = 'Text' + newline + 'Next'
                self.assertEqual(_boundaries(text)[0], {len(text)})
                text = 'Text' + newline + ' \t' + newline + 'Next'
                self.assertIn(text.index('Next'), _boundaries(text)[0])

    def test_count_whole_chunk_not_sum_of_parts(self):
        class PairCounter(CharacterCounter):
            counter_id = 'test-pairs'

            def count(self, text):
                return (len(text) + 1) // 2
        profile = replace(TEST_PROFILE, profile_id='test-pairs', counter_id=PairCounter.counter_id)
        doc = document('Data. Other. ' * 30)
        chunks = chunk_document(doc, PairCounter(), profile)
        self.assert_invariants(doc, chunks, PairCounter(), profile)

    def test_wrong_input_types(self):
        with self.assertRaises(TypeError):
            chunk_document({}, CharacterCounter(), TEST_PROFILE)
        with self.assertRaises(TypeError):
            chunk_document(document('data'), CharacterCounter(), {})

    def test_repeat_id_and_changed_context(self):
        doc = document('Abc. ' * 30)
        first = chunk_document(doc, CharacterCounter(), TEST_PROFILE)
        second = chunk_document(replace(doc, trace_id='another-trace', title='', pipeline_version='another'),
                                CharacterCounter(), TEST_PROFILE)
        self.assertEqual(first, second)
        self.assertEqual(first, chunk_document(doc, CharacterCounter(), TEST_PROFILE))

    def test_new_version_profile_and_text_change_ids(self):
        doc = document('A' * 30)
        first = chunk_document(doc, CharacterCounter(), TEST_PROFILE)[0].id
        for other, profile in (
            (replace(doc, document_version_id='00000000-0000-4000-8000-000000000001'), TEST_PROFILE),
            (doc, replace(TEST_PROFILE, profile_id='test-v2')),
            (replace(doc, normalized_text='B' * 30), TEST_PROFILE),
        ):
            self.assertNotEqual(first, chunk_document(other, CharacterCounter(), profile)[0].id)

    def test_repeated_text_has_distinct_ids(self):
        chunks = self.chunks('x' * 120)
        self.assertEqual(chunks[0].chunk_text, chunks[1].chunk_text)
        self.assertNotEqual(chunks[0].id, chunks[1].id)

    def test_uuid_serialization_golden(self):
        self.assertEqual(self.chunks('data')[0].id, 'deb617ed-aea4-5119-a470-7fc95fb535b4')

    def test_input_and_chunks_immutable(self):
        doc = document('data')
        snapshot = doc.to_dict()
        chunks = chunk_document(doc, CharacterCounter(), TEST_PROFILE)
        self.assertEqual(doc.to_dict(), snapshot)
        self.assertIsInstance(chunks, tuple)
        with self.assertRaises(FrozenInstanceError):
            chunks[0].chunk_text = 'changed'
        exported = chunks[0].to_dict()
        exported['chunk_text'] = 'changed'
        self.assertEqual(chunks[0].chunk_text, 'data')

    def test_corpus_pass_and_fail(self):
        for case in json.loads((CORPUS / 'manifest.json').read_text()):
            with self.subTest(case=case['file']):
                doc = document(fixture=Path(case['file']).stem)
                if case['status'] == 'PASS':
                    chunks = chunk_document(doc, CharacterCounter(), TEST_PROFILE)
                    self.assert_invariants(doc, chunks, CharacterCounter(), TEST_PROFILE)
                else:
                    with self.assertRaises(ChunkingError):
                        chunk_document(doc, CharacterCounter(), TEST_PROFILE)

    def test_empty_title_does_not_change_chunks(self):
        doc = document('A sentence.')
        for title in ('', ' \n', 'Other title'):
            self.assertEqual(chunk_document(doc, CharacterCounter(), TEST_PROFILE),
                             chunk_document(replace(doc, title=title), CharacterCounter(), TEST_PROFILE))

    def test_reject_quality_failures(self):
        for text in ('', ' \n', 'a\x00', '\ud800', 'x' * 131073):
            with self.assertRaises(ChunkingError):
                self.chunks(text)

    def test_profile_validation_and_counter_mismatch(self):
        for updates in ({'overlap': 8}, {'minimum': 0}, {'maximum': 23}, {'target': True}, {'profile_id': ''}):
            with self.assertRaises(ValueError):
                replace(TEST_PROFILE, **updates)
        with self.assertRaises(ValueError):
            replace(V1, maximum=769)
        with self.assertRaises(ChunkingError):
            chunk_document(document('text'), CharacterCounter())

    def test_tokenizer_errors_have_no_input_text(self):
        class Broken(CharacterCounter):
            def count(self, text):
                raise RuntimeError('sensitive content')
        with self.assertRaisesRegex(ChunkingError, '^Token counting failed$'):
            chunk_document(document('secret'), Broken(), TEST_PROFILE)
        for invalid in (0, -1, True, 1.5):
            class Invalid(CharacterCounter):
                def count(self, text):
                    return invalid
            with self.assertRaises(ChunkingError):
                chunk_document(document('secret'), Invalid(), TEST_PROFILE)

    def test_pathological_whitespace_explicit_failure(self):
        # Exact coverage + no blank chunks + max size cannot all hold here.
        with self.assertRaises(ChunkingError):
            self.chunks(' ' * 100 + 'a')

    def test_generated_documents(self):
        rng = random.Random(42)
        for _ in range(100):
            text = ''.join(rng.choice(('Текст. ', 'Данные? ', '🙂', '\r\n\r\n', 'e\u0301', '123.45 '))
                           for _ in range(rng.randint(1, 80)))
            if text.strip():
                self.assert_invariants(document(text), self.chunks(text), CharacterCounter(), TEST_PROFILE)

    def test_dto_validation(self):
        row = self.chunks('data')[0]
        for updates in ({'char_end': 99}, {'char_start': -1}, {'chunk_no': -1}, {'token_count': 0},
                        {'chunk_text': '    '}, {'id': 'bad'}, {'chunk_no': True}):
            with self.assertRaises(ValueError):
                replace(row, **updates)


@unittest.skipUnless(os.environ.get('ML1_TEST_QWEN_TOKENIZER') == '1', 'Explicit cached-tokenizer acceptance test')
class QwenTokenizerAcceptanceTests(unittest.TestCase):
    def test_pinned_tokenizer_corpus_and_large_multilingual_document(self):
        counter = QwenTokenCounter(cache_dir=os.environ.get('ML1_TOKENIZER_CACHE'))
        golden = chunk_document(document('Проверка текста.'), counter)[0]
        self.assertEqual(golden.id, '6e7bfb92-eeae-5cbb-af4a-d6f5bd9488c3')
        self.assertEqual(golden.token_count, 6)
        docs = [document(fixture=Path(case['file']).stem)
                for case in json.loads((CORPUS / 'manifest.json').read_text()) if case['status'] == 'PASS']
        docs.append(document(('Выручка выросла на 12.5%. Revenue increased! 🙂\r\n\r\n') * 500))
        docs.extend((document('🙂' * 2000), document('слово' * 2000)))
        for doc in docs:
            self.assertEqual(check_document(doc).status, 'PASS')
            first = chunk_document(doc, counter)
            ChunkingTests().assert_invariants(doc, first, counter, V1)
            self.assertEqual(first, chunk_document(doc, counter))
