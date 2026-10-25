"""No pretrained weights required. The real Qwen smoke is explicitly opt-in."""
import importlib.util
import math
import os
import tempfile
import unittest
from dataclasses import FrozenInstanceError, replace
from types import SimpleNamespace
from unittest.mock import patch
from uuid import NAMESPACE_URL, uuid4, uuid5

from ml.chunking.dto import DocumentChunk
from ml.chunking.profiles import QWEN_REVISION
from ml.embeddings import ChunkEmbedding, Embedder, EmbeddingError, V1
from ml.embeddings.core import EncodedVector, EmbeddingInput, normalized_float32, text_hash
from ml.embeddings.profile import VECTOR_COMPARE_ATOL
from ml.embeddings.qwen import QwenBatchEncoder


def unit_vector(index=0):
    return tuple(1.0 if i == index else 0.0 for i in range(1024))


def sample_chunks(count=3):
    version = str(uuid4())
    return tuple(DocumentChunk(str(uuid4()), version, 'v1', i, 'Data', i * 4, (i + 1) * 4, 1)
                 for i in range(count))


class FakeEncoder:
    profile = V1

    def __init__(self):
        self.calls = []

    def encode_batch(self, inputs):
        self.calls.append(tuple(inputs))
        return tuple(EncodedVector(item.key, unit_vector(i)) for i, item in reversed(list(enumerate(inputs))))


class EmbeddingTests(unittest.TestCase):
    def test_profile_revision_dimension_and_immutability(self):
        self.assertEqual(V1.revision, QWEN_REVISION)
        self.assertEqual(V1.dimension, 1024)
        with self.assertRaises(FrozenInstanceError):
            V1.dimension = 512
        with self.assertRaises((ValueError, TypeError)):
            replace(V1, dimension=512)

    def test_batching_preserves_key_mapping_even_if_outputs_reordered(self):
        encoder = FakeEncoder()
        chunks = sample_chunks(5)
        result = Embedder(encoder, batch_size=2).encode_chunks(chunks)
        self.assertEqual([len(batch) for batch in encoder.calls], [2, 2, 1])
        self.assertEqual([item.chunk_id for item in result], [chunk.id for chunk in chunks])
        for i, item in enumerate(result):
            self.assertEqual(item.vector, unit_vector(i % 2))
            self.assertEqual(item.text_sha256, text_hash(chunks[i].chunk_text))
            self.assertEqual((item.embedding_model, item.embedding_version), (V1.model, V1.version))
        with self.assertRaises(FrozenInstanceError):
            result[0].chunk_id = str(uuid4())

    def test_document_text_exact_no_title_or_prompt(self):
        encoder = FakeEncoder()
        chunk = replace(sample_chunks(1)[0], chunk_text=' Д🙂 ', char_end=4)
        Embedder(encoder).encode_chunks((chunk,))
        self.assertEqual(encoder.calls[0][0].text, ' Д🙂 ')

    def test_queries_use_same_profile_with_fixed_instruction(self):
        encoder = FakeEncoder()
        result = Embedder(encoder, batch_size=1).encode_queries(('Вопрос?', ' Other '))
        self.assertEqual(len(result), 2)
        self.assertEqual(encoder.calls[0][0].text, f'Instruct: {V1.query_instruction}\nQuery:Вопрос?')
        self.assertTrue(encoder.calls[1][0].text.endswith('Query: Other '))

    def test_empty_inputs_no_inference(self):
        encoder = FakeEncoder()
        embedder = Embedder(encoder)
        self.assertEqual(embedder.encode_chunks(()), ())
        self.assertEqual(embedder.encode_queries(()), ())
        self.assertEqual(encoder.calls, [])

    def test_invalid_input_batch_profile_and_duplicate_ids(self):
        for size in (0, -1, True, 1.5):
            with self.assertRaises(ValueError):
                Embedder(FakeEncoder(), batch_size=size)
        encoder = FakeEncoder()
        encoder.profile = None
        with self.assertRaises(EmbeddingError):
            Embedder(encoder)
        embedder = Embedder(FakeEncoder())
        for query in ('', ' \n', None):
            with self.assertRaises(EmbeddingError):
                embedder.encode_queries((query,))
        chunk = sample_chunks(1)[0]
        with self.assertRaises(EmbeddingError):
            embedder.encode_chunks((chunk, chunk))
        with self.assertRaises(EmbeddingError):
            embedder.encode_chunks((replace(chunk, chunking_profile_id='other'),))
        with self.assertRaises(TypeError):
            embedder.encode_chunks(({},))

    def test_missing_extra_duplicate_or_foreign_output_keys(self):
        chunks = sample_chunks(2)
        a, b = chunks
        for output in ((), (EncodedVector(a.id, unit_vector()),),
                       (EncodedVector(a.id, unit_vector()), EncodedVector(a.id, unit_vector())),
                       (EncodedVector(a.id, unit_vector()), EncodedVector('unknown', unit_vector())),
                       (EncodedVector(a.id, unit_vector()), EncodedVector(b.id, unit_vector()),
                        EncodedVector('extra', unit_vector())), ({},)):
            class Broken(FakeEncoder):
                def encode_batch(self, inputs):
                    return output
            with self.subTest(output_size=len(output)), self.assertRaises(EmbeddingError):
                Embedder(Broken()).encode_chunks(chunks)

    def test_invalid_vector_dimension_nan_inf_zero_and_bool(self):
        for vector in ((1.0,) * 1023, (1.0,) * 1025, (0.0,) * 1024,
                       (math.nan,) + unit_vector()[1:], (math.inf,) + unit_vector()[1:],
                       (-math.inf,) + unit_vector()[1:], (True,) + unit_vector()[1:]):
            with self.subTest(length=len(vector)), self.assertRaises(EmbeddingError):
                normalized_float32(vector)

    def test_normalization_and_float32_output(self):
        result = normalized_float32((3.0, 4.0) + (0.0,) * 1022)
        self.assertAlmostEqual(result[0], .6, places=6)
        self.assertAlmostEqual(result[1], .8, places=6)
        self.assertAlmostEqual(math.hypot(*result), 1, places=6)

    def test_chunk_embedding_validation(self):
        item = Embedder(FakeEncoder()).encode_chunks(sample_chunks(1))[0]
        for updates in ({'vector': (1.0,)}, {'vector': list(item.vector)}, {'text_sha256': 'bad'},
                        {'embedding_model': 'other'}, {'embedding_version': 'other'}):
            with self.assertRaises(EmbeddingError):
                replace(item, **updates)

    def test_inference_failure_second_batch_returns_no_partial_result(self):
        class Broken(FakeEncoder):
            def encode_batch(self, inputs):
                if self.calls:
                    raise RuntimeError('secret text and credentials')
                return super().encode_batch(inputs)
        with self.assertRaisesRegex(EmbeddingError, '^Embedding inference failed$'):
            Embedder(Broken(), batch_size=1).encode_chunks(sample_chunks(2))

    def test_loader_checks_pinned_runtime_before_model_loading(self):
        with patch('ml.embeddings.qwen.version', return_value='0.0'):
            with self.assertRaisesRegex(EmbeddingError, 'tokenizers'):
                QwenBatchEncoder()


@unittest.skipUnless(importlib.util.find_spec('torch') is not None, 'Optional torch runtime')
class QwenPoolingTests(unittest.TestCase):
    @unittest.skipUnless(importlib.util.find_spec('transformers') is not None, 'Optional transformers runtime')
    def test_offline_loader_pins_revision_dtype_and_disables_truncation(self):
        import torch
        from tokenizers import Tokenizer, models, pre_tokenizers
        from unittest.mock import MagicMock

        tokenizer = Tokenizer(models.WordLevel({'<|endoftext|>': 0, '[UNK]': 1, 'Data': 2}, unk_token='[UNK]'))
        tokenizer.pre_tokenizer = pre_tokenizers.Whitespace()
        tokenizer.enable_truncation(max_length=1)
        tokenizer.enable_padding(length=5)
        model = MagicMock()
        model.config.hidden_size = 1024
        model.to.return_value = model
        model.eval.return_value = model
        versions = {'tokenizers': V1.tokenizer_version, 'transformers': V1.transformers_version,
                    'torch': V1.torch_version}
        with tempfile.TemporaryDirectory() as directory:
            path = directory + '/tokenizer.json'
            tokenizer.save(path)
            with patch('ml.embeddings.qwen.version', side_effect=versions.__getitem__), \
                    patch('huggingface_hub.hf_hub_download', return_value=path) as download, \
                    patch('transformers.AutoModel.from_pretrained', return_value=model) as load:
                encoder = QwenBatchEncoder(cache_dir=directory)
            self.assertTrue(download.call_args.kwargs['local_files_only'])
            self.assertEqual(download.call_args.kwargs['revision'], QWEN_REVISION)
            self.assertEqual(load.call_args.kwargs['revision'], QWEN_REVISION)
            self.assertEqual(load.call_args.kwargs['torch_dtype'], torch.float32)
            self.assertFalse(load.call_args.kwargs['trust_remote_code'])
            self.assertTrue(load.call_args.kwargs['local_files_only'])
            self.assertEqual(len(encoder.tokenizer.encode('Data Data').ids), 2)
            self.assertIsNone(encoder.tokenizer.padding)
            model.to.assert_called_once_with('cpu')
            model.eval.assert_called_once()

    @unittest.skipUnless(importlib.util.find_spec('transformers') is not None, 'Optional transformers runtime')
    def test_tiny_qwen_architecture_without_pretrained_weights(self):
        import torch
        from transformers import AutoModel, Qwen3Config

        encoder = QwenBatchEncoder.__new__(QwenBatchEncoder)
        encoder.pad_id = 0
        encoder.tokenizer = SimpleNamespace(encode_batch=lambda texts, **kwargs: [
            SimpleNamespace(ids=[1, 2] if 'long' in text else [3]) for text in texts])
        with torch.random.fork_rng():
            torch.manual_seed(42)
            config = Qwen3Config(vocab_size=16, hidden_size=1024, intermediate_size=32,
                                 num_hidden_layers=1, num_attention_heads=2, num_key_value_heads=2,
                                 head_dim=8, pad_token_id=0)
            encoder.model = AutoModel.from_config(config, attn_implementation='eager').eval()
        batched = Embedder(encoder, batch_size=2).encode_queries(('long query', 'short'))
        single = Embedder(encoder, batch_size=1).encode_queries(('long query', 'short'))
        for a, b in zip(batched, single):
            self.assertEqual(len(a), V1.dimension)
            self.assertLess(max(abs(x - y) for x, y in zip(a, b)), 1e-6)

    def test_left_padding_last_token_and_inference_mode_without_weights(self):
        import torch

        encoder = QwenBatchEncoder.__new__(QwenBatchEncoder)
        encoder.pad_id = 0

        class Tokenizer:
            def encode_batch(self, texts, *, add_special_tokens):
                self_texts[:] = texts
                self_test.assertFalse(add_special_tokens)
                return [SimpleNamespace(ids=[1, 2]), SimpleNamespace(ids=[3])]

        class Model:
            def __call__(self, *, input_ids, attention_mask):
                self_test.assertEqual(input_ids.tolist(), [[1, 2], [0, 3]])
                self_test.assertEqual(attention_mask.tolist(), [[1, 1], [0, 1]])
                self_test.assertFalse(torch.is_grad_enabled())
                states = torch.zeros((2, 2, 1024))
                states[0, 1, 4] = 2
                states[1, 1, 5] = 3
                return SimpleNamespace(last_hidden_state=states)

        self_test, self_texts = self, []
        encoder.tokenizer = Tokenizer()
        encoder.model = Model()
        result = Embedder(encoder).encode_queries(('first', 'second'))
        self.assertEqual(result, (unit_vector(4), unit_vector(5)))
        self.assertEqual(len(self_texts), 2)

    def test_overlength_input_rejected_without_model_call_or_truncation(self):
        encoder = QwenBatchEncoder.__new__(QwenBatchEncoder)
        encoder.tokenizer = SimpleNamespace(encode_batch=lambda *args, **kwargs: [
            SimpleNamespace(ids=[1] * (V1.max_input_tokens + 1))])
        with self.assertRaisesRegex(EmbeddingError, 'truncation is forbidden'):
            encoder.encode_batch((EmbeddingInput('key', 'text'),))


@unittest.skipUnless(os.environ.get('ML1_TEST_QWEN_MODEL') == '1', 'Real model smoke requires explicit cached weights')
class QwenRealModelSmokeTests(unittest.TestCase):
    def test_cached_model_chunks_queries_and_batch_consistency(self):
        from ml.chunking import chunk_document
        from ml.chunking.tokenization import QwenTokenCounter
        from ml.tests.test_chunking import document

        encoder = QwenBatchEncoder(cache_dir=os.environ.get('ML1_MODEL_CACHE'))
        counter = QwenTokenCounter(cache_dir=os.environ.get('ML1_MODEL_CACHE'))
        texts = (
            'ЦБ повысил ключевую ставку.',
            'Банк России увеличил ключевую ставку.',
            'Из-за дорогих кредитов компании сократили инвестиции.',
            'Компания открыла новый завод.',
            'Предприятие запустило новый производственный завод.',
            'На действующем заводе обновили оборудование.',
            'На побережье ожидаются сильные дожди.',
            'Банк повысил процентные ставки по вкладам.',
            'Доходность банковских депозитов увеличилась.',
            'Банк снизил ставки по ипотечным кредитам.',
            'Футбольная команда выиграла чемпионат.',
            'ЦБ повысил ключевую ставку.',  # Same text, different document/chunk ID.
        )
        chunks = []
        for index, text in enumerate(texts):
            saved = replace(document(text),
                            document_id=str(uuid5(NAMESPACE_URL, f'ml1-smoke-document:{index}')),
                            document_version_id=str(uuid5(NAMESPACE_URL, f'ml1-smoke-version:{index}')))
            generated = chunk_document(saved, counter)
            self.assertEqual(len(generated), 1)
            chunks.extend(generated)
        first = Embedder(encoder, batch_size=4).encode_chunks(chunks)
        repeated = Embedder(encoder, batch_size=4).encode_chunks(chunks)
        self.assertEqual([item.chunk_id for item in first], [chunk.id for chunk in chunks])
        repeat_max_abs = 0.0
        for chunk, a, b in zip(chunks, first, repeated):
            self.assertEqual(a.text_sha256, text_hash(chunk.chunk_text))
            self.assertEqual(len(a.vector), 1024)
            self.assertTrue(all(math.isfinite(value) for value in a.vector))
            self.assertAlmostEqual(math.hypot(*a.vector), 1.0, places=6)
            difference = max(abs(x - y) for x, y in zip(a.vector, b.vector))
            repeat_max_abs = max(repeat_max_abs, difference)
            self.assertLessEqual(difference, VECTOR_COMPARE_ATOL)
        selected = (chunks[3], chunks[0], chunks[8])
        separate = Embedder(encoder, batch_size=1).encode_chunks(selected)
        by_id = {item.chunk_id: item for item in first}
        differences = [max(abs(x - y) for x, y in zip(item.vector, by_id[item.chunk_id].vector))
                       for item in separate]
        self.assertLessEqual(max(differences), VECTOR_COMPARE_ATOL)
        self.assertNotEqual(first[0].chunk_id, first[-1].chunk_id)
        duplicate_difference = max(abs(x - y) for x, y in zip(first[0].vector, first[-1].vector))
        self.assertLessEqual(duplicate_difference, VECTOR_COMPARE_ATOL)

        def cosine(i, j):
            a, b = first[i].vector, first[j].vector
            return math.fsum(x * y for x, y in zip(a, b)) / (math.hypot(*a) * math.hypot(*b))

        # Relative behavior only: no absolute cosine quality threshold.
        for label, anchor, close, related, unrelated in (
            ('key_rate', 0, 1, 2, 6), ('factory', 3, 4, 5, 1), ('deposits', 7, 8, 9, 10),
        ):
            values = [cosine(anchor, index) for index in (close, related, unrelated)]
            print(f'\nCOSINE {label}: close={values[0]:.9f}; related={values[1]:.9f}; unrelated={values[2]:.9f}', flush=True)
            self.assertGreater(values[0], values[1], label)
            self.assertGreater(values[1], values[2], label)
        print(f'COSINE rate_vs_factory={cosine(0, 3):.9f}; '
              f'duplicate={cosine(0, 11):.9f}; duplicate_max_abs={duplicate_difference:.3g}; '
              f'batch_max_abs={max(differences):.3g}', flush=True)
        print(f'TECH chunks={len(chunks)}; dimension=1024; finite=PASS; ids=PASS; '
              f'repeat_max_abs={repeat_max_abs:.3g}', flush=True)
        self.assertGreater(cosine(0, 1), cosine(0, 3))
        queries = Embedder(encoder).encode_queries(('Что происходит с банками?',))
        self.assertEqual(len(queries[0]), 1024)
