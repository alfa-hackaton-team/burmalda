"""Disposable real ML HTTP/worker smoke; does not claim to exercise Go/frontend."""
import argparse
from copy import deepcopy
import hashlib
import json
import os
from pathlib import Path
from time import perf_counter
from uuid import uuid4


def require_success(report):
    failed = [name for name, passed in report['checks'].items() if not passed]
    if not report.get('complete') or failed:
        raise AssertionError('Incomplete or failed ML smoke: ' + ', '.join(failed))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    import psycopg
    from psycopg import sql
    from psycopg.conninfo import make_conninfo
    from fastapi.testclient import TestClient
    from ml.evaluation.dataset import load_dataset, saved_document
    from ml.evaluation.e2e import backend_payload
    from ml.evaluation.validation_study import HOLDOUT
    from ml.migrate import apply_migrations
    from ml.pipeline import ML1Pipeline
    from ml.adapters.postgres_catalog import load_versions
    from ml_runtime.migrate import apply_migrations as migrate_runtime
    from ml_runtime.article_processing_runtime import (
        create_article_processing_runtime, create_article_processing_api_runtime)
    from ml_runtime.postgres_store import PostgresOperationStore
    from ml_runtime.event_store import PostgresEventStore
    from ml_runtime.model_client import create_agent_from_env
    from ml_runtime.model_client import OpenAICompatibleModelClient, DEFAULT_BASE_URL
    from ml_runtime.service import AnalysisService

    dataset, checksum = load_dataset(HOLDOUT)
    data = backend_payload(saved_document(dataset['documents'][0]))
    base = os.environ['ML1_EVAL_POSTGRES_DSN']
    name = 'ml_http_smoke_' + uuid4().hex
    report = {'scope': 'FastAPI TestClient + real worker/models/pgvector/gateway; no Go/frontend',
              'dataset_sha256': checksum, 'checks': {}, 'timing': {}, 'model_calls': []}

    def save():
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')

    def record_client(client, stage):
        original = client.generate_json
        def generate(**kwargs):
            call = {'stage': stage, 'model': kwargs['model_name']}
            report['model_calls'].append(call)
            t = perf_counter()
            try:
                result = original(**kwargs)
                call['status'] = 'ok'
                return result
            except Exception as exc:
                causes = []
                current = exc
                while current is not None and len(causes) < 8:
                    causes.append(type(current).__name__)
                    current = current.__cause__
                call['error_types'] = causes
                raise
            finally:
                call['seconds'] = perf_counter()-t
                save()
        client.generate_json = generate
        return client

    with psycopg.connect(base, autocommit=True) as admin:
        admin.execute(sql.SQL('CREATE DATABASE {}').format(sql.Identifier(name)))
        dsn = make_conninfo(base, dbname=name)
        try:
            with psycopg.connect(dsn, autocommit=True) as conn:
                conn.execute('CREATE EXTENSION vector')
                apply_migrations(conn)
                migrate_runtime(dsn)
                os.environ.update(ML_SERVICE_TOKEN=uuid4().hex, ML_MAX_TEXT_BYTES='131072',
                                  ML_PIPELINE_VERSIONS='ml-pipeline-v1')
                pipeline = ML1Pipeline.from_local_models(conn)
                event_client = record_client(OpenAICompatibleModelClient(
                    os.environ['ML_LLM_API_KEY'], os.environ.get('ML_LLM_BASE_URL', DEFAULT_BASE_URL),
                    reasoning_effort='low'), 'event_extraction')
                runtime = create_article_processing_runtime(pipeline, model_client=event_client,
                    operation_store=PostgresOperationStore(dsn), event_store=PostgresEventStore(dsn))
                client = TestClient(runtime.app)
                headers = {'Authorization': 'Bearer '+os.environ['ML_SERVICE_TOKEN'],
                           'Idempotency-Key': data['article']['article_version_id']+':ml-pipeline-v1'}
                started = perf_counter()
                response = client.post('/internal/v0.1/article-processings', json=data, headers=headers)
                report['submit'] = {'http': response.status_code, 'body': response.json()}
                assert response.status_code == 202
                worker_result = runtime.worker.run_once()
                report['worker'] = worker_result
                report['timing']['http_index_and_events_seconds'] = perf_counter()-started
                operation_id = response.json()['processing_id']
                fetched = client.get('/internal/v0.1/article-processings/'+operation_id, headers=headers)
                report['get'] = {'http': fetched.status_code, 'body': fetched.json()}
                save()
                report['checks']['indexed_and_events_saved'] = fetched.json().get('state') == 'SUCCEEDED'
                repeated = client.post('/internal/v0.1/article-processings', json=data, headers=headers)
                report['checks']['repeat_same_operation'] = repeated.json().get('processing_id') == operation_id
                report['checks']['repeat_no_worker_job'] = runtime.worker.run_once() is None
                reconstructed = create_article_processing_api_runtime(
                    operation_store=PostgresOperationStore(dsn))
                report['checks']['api_reconstruction_preserves_result'] = TestClient(
                    reconstructed.app).get('/internal/v0.1/article-processings/'+operation_id,
                        headers=headers).json() == fetched.json()
                bad = deepcopy(data)
                bad['article'].update(article_id=str(uuid4()), article_version_id=str(uuid4()), body=' ')
                bad['article']['content_hash'] = hashlib.sha256(b' ').hexdigest()
                bad_headers = {**headers, 'Idempotency-Key': bad['article']['article_version_id']+':ml-pipeline-v1'}
                bad_response = client.post('/internal/v0.1/article-processings', json=bad, headers=bad_headers)
                report['quality_fail'] = {'http': bad_response.status_code, 'body': bad_response.json()}
                rejected = runtime.worker.run_once()
                report['quality_fail']['worker'] = rejected
                report['checks']['quality_fail_not_indexed'] = (rejected['state'] == 'FAILED' and
                    conn.execute('SELECT count(*) FROM ml.document_chunks WHERE document_version_id=%s',
                        (bad['article']['article_version_id'],)).fetchone()[0] == 0)
                report['checks']['unauthenticated_401'] = client.post(
                    '/internal/v0.1/article-processings', json=data).status_code == 401
                report['checks']['malformed_400'] = client.post(
                    '/internal/v0.1/article-processings', json={}, headers=headers).status_code == 400
                ids = [r[0] for r in conn.execute('SELECT id::text FROM ml.document_chunks').fetchall()]
                report['persisted_chunks'] = len(ids)
                report['checks']['complete_vectors'] = conn.execute(
                    'SELECT count(*) FROM ml.document_chunks WHERE embedding IS NULL').fetchone()[0] == 0
                request = json.loads(Path('docs/ml/fixtures/v1/analysis.request.json').read_text())
                request['context'].update(profile_features=[], profile_data={})
                request['event'].update(title=dataset['queries'][0]['text'], summary=dataset['queries'][0]['text'],
                    document_version_ids=[data['article']['article_version_id']], status='candidate')
                # Simulate process reconstruction from durable catalog, with trusted test access.
                restored = ML1Pipeline(conn, pipeline.embedder, pipeline.token_counter,
                    versions=load_versions(conn), context_chunks={request['context']['business_context_id']: ids},
                    service_chunk_ids=ids)
                started = perf_counter()
                agent = create_agent_from_env()
                record_client(agent.client, 'analysis')
                report['analysis'] = AnalysisService(restored, agent).analyze(request)
                report['timing']['analysis_seconds'] = perf_counter()-started
                report['checks']['analysis_succeeded'] = report['analysis']['status'] == 'succeeded'
                class UnavailableReranker:
                    def score(self, *args):
                        raise RuntimeError('intentional smoke outage')
                restored._reranker = UnavailableReranker()
                fallback = restored.search_with_scores(dataset['queries'][0]['text'])
                report['checks']['reranker_fallback'] = (not fallback.reranker_applied
                    and bool(fallback.response['results']) and bool(fallback.response['warnings']))
                report['complete'] = True
                save()
        except Exception as exc:
            report['exception'] = {'type': type(exc).__name__, 'message': str(exc)}
            save()
            raise
        finally:
            admin.execute(sql.SQL('DROP DATABASE {} WITH (FORCE)').format(sql.Identifier(name)))
    require_success(report)
    print(json.dumps(report['checks']))


if __name__ == '__main__':
    main()
