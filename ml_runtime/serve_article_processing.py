"""Start the Go-facing HTTP API after loading the ML1 indexer plugin."""

from __future__ import annotations

import os

import uvicorn

from .article_processing_runtime import create_article_processing_runtime, load_indexer_from_env


def main() -> int:
    host = os.environ.get("ML_HTTP_HOST", "0.0.0.0").strip()
    try:
        port = int(os.environ.get("ML_HTTP_PORT", "8000"))
    except ValueError as exc:
        raise ValueError("ML_HTTP_PORT must be an integer") from exc
    if not host or not 1 <= port <= 65535:
        raise ValueError("ML_HTTP_HOST and ML_HTTP_PORT must identify a valid bind address")
    runtime = create_article_processing_runtime(load_indexer_from_env())
    uvicorn.run(runtime.app, host=host, port=port, log_level="info")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
