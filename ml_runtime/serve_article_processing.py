"""Start the Go-facing API without loading embedding or LLM models."""

from __future__ import annotations

import os

import uvicorn

from .article_processing_runtime import create_article_processing_api_runtime


def main() -> int:
    host = os.environ.get("ML_HTTP_HOST", "0.0.0.0").strip()
    try:
        port = int(os.environ.get("ML_HTTP_PORT", "8000"))
    except ValueError as exc:
        raise ValueError("ML_HTTP_PORT must be an integer") from exc
    if not host or not 1 <= port <= 65535:
        raise ValueError("ML_HTTP_HOST and ML_HTTP_PORT must identify a valid bind address")
    runtime = create_article_processing_api_runtime()
    from .product_api import FeedRankingService, register_product_routes
    from .product_corpus import ProductCorpus
    from .product_store import ProductStore
    store = ProductStore()
    token = os.environ.get('ML_SERVICE_TOKEN', '')
    register_product_routes(runtime.app, store=store,
        ranking=FeedRankingService(store, ProductCorpus(store), token),
        service_token=token, analysis_version=os.environ.get('ML_ANALYSIS_CONFIG_VERSION', ''))
    uvicorn.run(runtime.app, host=host, port=port, log_level="info")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
