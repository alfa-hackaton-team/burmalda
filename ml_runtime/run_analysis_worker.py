"""Consume persistent analysis jobs without holding a Go HTTP request open."""
import logging
import signal
from threading import Event

from .analysis_validation import validate_contract_message
from .product_corpus import ProductCorpus
from .product_store import ProductStore
from .service import _failed


def process_next(store, corpus):
    row = store.claim()
    if row is None:
        return False
    identity = str(row['analysis_id'])
    request = row['request']
    try:
        with corpus.analysis_service(request) as service:
            response = service.analyze(request, analysis_run_id=identity)
        validate_contract_message(response, 'AnalysisResponse')
        if response['analysis_run_id'] != identity:
            raise ValueError('Analysis identity changed')
    except Exception:
        # Do not log payloads, provider errors, DSNs or keys.
        logging.getLogger(__name__).warning('Analysis execution failed: %s', identity)
        response = _failed(request, 'MODEL_UNAVAILABLE', 'Analysis could not be completed.', False)
        response['analysis_run_id'] = identity
    store.finish(identity, response)
    return True


def main():
    stopped = Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stopped.set())
    store = ProductStore()
    corpus = ProductCorpus(store)
    while not stopped.is_set():
        try:
            store.expire_running()
            if not process_next(store, corpus):
                stopped.wait(1)
        except Exception:
            logging.getLogger(__name__).warning('Analysis queue unavailable; retrying')
            stopped.wait(5)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
