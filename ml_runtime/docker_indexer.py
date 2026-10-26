"""Factory for ML1's worker-side pipeline using separate model services."""

from __future__ import annotations

import os


def create_ml1_pipeline():
    """Create the worker pipeline with cached tokenizer files and remote model services."""
    from ml.database import connect_runtime
    from ml.chunking.tokenization import QwenTokenCounter
    from ml.embeddings import Embedder
    from ml.pipeline import ML1Pipeline
    from ml.adapters.postgres_catalog import load_versions
    from .ml1_model_client import RemoteEmbeddingEncoder, RemoteQwenReranker

    connection = connect_runtime()
    cache_dir = os.environ.get("ML_MODEL_CACHE") or os.environ.get("HF_HOME")
    try:
        return ML1Pipeline(
            connection,
            Embedder(RemoteEmbeddingEncoder(), batch_size=int(os.environ.get('ML_EMBEDDING_BATCH_SIZE', '1'))),
            QwenTokenCounter(cache_dir=cache_dir, local_files_only=True),
            versions=load_versions(connection),
            reranker=RemoteQwenReranker(),
            cache_dir=cache_dir,
        )
    except Exception:
        connection.close()
        raise
