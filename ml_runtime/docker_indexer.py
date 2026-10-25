"""Factory for the ML1 production pipeline in the Docker worker container."""

from __future__ import annotations

import os


def create_ml1_pipeline():
    """Load pinned local embedding/tokenizer weights and keep DB connection open."""
    from ml.database import connect_runtime
    from ml.pipeline import ML1Pipeline

    connection = connect_runtime()
    cache_dir = os.environ.get("ML_MODEL_CACHE") or os.environ.get("HF_HOME")
    try:
        return ML1Pipeline.from_local_models(connection, cache_dir=cache_dir)
    except Exception:
        connection.close()
        raise
