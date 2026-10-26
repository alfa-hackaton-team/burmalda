"""Worker adapters keep ML1 model inference in dedicated services."""
import unittest
from unittest.mock import patch

from ml.embeddings.core import EmbeddingError, EmbeddingInput
from ml.reranking import RerankerError
from ml_runtime.ml1_model_client import RemoteEmbeddingEncoder, RemoteQwenReranker


class ModelServiceClientTests(unittest.TestCase):
    def test_embedding_adapter_preserves_keys_and_vectors(self):
        encoder = RemoteEmbeddingEncoder("http://embed.test")
        inputs = (EmbeddingInput("chunk-a", "text a"), EmbeddingInput("chunk-b", "text b"))
        payload = {"vectors": [{"key": "chunk-b", "vector": [0.0, 1.0]},
                               {"key": "chunk-a", "vector": [1.0, 0.0]}]}
        with patch("ml_runtime.ml1_model_client._post_json", return_value=payload) as post:
            result = encoder.encode_batch(inputs)
        self.assertEqual([(row.key, row.vector) for row in result],
                         [("chunk-b", [0.0, 1.0]), ("chunk-a", [1.0, 0.0])])
        self.assertEqual(post.call_args.args[:2], ("http://embed.test", "/v1/embeddings"))
        self.assertEqual(post.call_args.args[2]["inputs"],
                         [{"key": "chunk-a", "text": "text a"},
                          {"key": "chunk-b", "text": "text b"}])

    def test_embedding_transport_error_is_safe(self):
        with patch("ml_runtime.ml1_model_client._post_json", side_effect=RuntimeError("secret response")):
            with self.assertRaisesRegex(EmbeddingError, "Embedding inference service failed"):
                RemoteEmbeddingEncoder().encode_batch((EmbeddingInput("key", "text"),))

    def test_reranker_adapter_returns_keyed_scores(self):
        reranker = RemoteQwenReranker("http://rerank.test")
        candidates = [{"chunk_id": "chunk-a", "chunk_text": "text a"}]
        with patch("ml_runtime.ml1_model_client._post_json", return_value={
            "scores": [{"chunk_id": "chunk-a", "score": 0.75}],
        }) as post:
            result = reranker.score("query", candidates)
        self.assertEqual(result, (("chunk-a", 0.75),))
        self.assertEqual(post.call_args.args[:2], ("http://rerank.test", "/v1/rerank"))
        self.assertEqual(post.call_args.args[2], {"query": "query", "candidates": candidates})

    def test_reranker_transport_error_uses_existing_fallback_signal(self):
        with patch("ml_runtime.ml1_model_client._post_json", side_effect=RuntimeError("secret response")):
            with self.assertRaisesRegex(RerankerError, "Reranker inference service failed"):
                RemoteQwenReranker().score("query", [{"chunk_id": "chunk-a", "chunk_text": "text"}])


if __name__ == "__main__":
    unittest.main()
