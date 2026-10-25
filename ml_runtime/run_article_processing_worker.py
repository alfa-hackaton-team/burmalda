"""Run the durable article-processing queue worker."""

from __future__ import annotations

import logging
import os
import time

from .article_processing_runtime import create_article_processing_runtime, load_indexer_from_env


def main() -> int:
    try:
        poll_seconds = float(os.environ.get("ML_WORKER_POLL_SECONDS", "1"))
    except ValueError as exc:
        raise ValueError("ML_WORKER_POLL_SECONDS must be a positive number") from exc
    if poll_seconds <= 0:
        raise ValueError("ML_WORKER_POLL_SECONDS must be a positive number")

    logging.basicConfig(level=os.environ.get("ML_LOG_LEVEL", "INFO"))
    runtime = create_article_processing_runtime(load_indexer_from_env())
    logging.info("ML article-processing worker started")
    while True:
        operation = runtime.worker.run_once()
        if operation is None:
            time.sleep(poll_seconds)
            continue
        logging.info(
            "Article processing finished",
            extra={"processing_id": operation.get("processing_id"), "state": operation.get("state")},
        )


if __name__ == "__main__":
    raise SystemExit(main())
