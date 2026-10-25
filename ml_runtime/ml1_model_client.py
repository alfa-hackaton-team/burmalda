"""Internal HTTP clients for the isolated ML1 embedding and reranker containers."""

from __future__ import annotations

import json
import os
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from ml.embeddings.core import EncodedVector, EmbeddingError
from ml.embeddings.profile import V1
from ml.reranking import RerankerError


def _post_json(base_url: str, path: str, payload: dict, timeout: float) -> dict:
    request = Request(
        f"{base_url.rstrip('/')}{path}",
        data=json.dumps(payload, ensure_ascii=False).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urlopen(request, timeout=timeout) as response:
            result = json.loads(response.read())
    except (HTTPError, URLError, TimeoutError, OSError, ValueError):
        raise RuntimeError("ML1 model service request failed") from None
    if not isinstance(result, dict):
        raise RuntimeError("ML1 model service returned an invalid response")
    return result


class RemoteEmbeddingEncoder:
    """BatchEncoder compatible with ML1 Embedder; model weights stay out of worker."""

    profile = V1

    def __init__(self, base_url: str | None = None, *, timeout: float = 300):
        self.base_url = (base_url or os.environ.get("ML1_EMBEDDING_URL", "http://embedding-model:8101")).rstrip("/")
        self.timeout = timeout

    def encode_batch(self, inputs):
        try:
            result = _post_json(
                self.base_url,
                "/v1/embeddings",
                {"inputs": [{"key": item.key, "text": item.text} for item in inputs]},
                self.timeout,
            )
            rows = result["vectors"]
            if not isinstance(rows, list):
                raise ValueError
            return tuple(EncodedVector(row["key"], row["vector"]) for row in rows)
        except Exception:
            raise EmbeddingError("Embedding inference service failed") from None


class RemoteQwenReranker:
    """Qwen scorer adapter; HTTP failures use ML1's existing vector-only fallback."""

    def __init__(self, base_url: str | None = None, *, timeout: float = 300):
        self.base_url = (base_url or os.environ.get("ML1_RERANKER_URL", "http://reranker-model:8102")).rstrip("/")
        self.timeout = timeout

    def score(self, query, candidates):
        try:
            result = _post_json(
                self.base_url,
                "/v1/rerank",
                {"query": query, "candidates": list(candidates)},
                self.timeout,
            )
            scores = result["scores"]
            if not isinstance(scores, list):
                raise ValueError
            return tuple((row["chunk_id"], float(row["score"])) for row in scores)
        except Exception:
            raise RerankerError("Reranker inference service failed") from None
