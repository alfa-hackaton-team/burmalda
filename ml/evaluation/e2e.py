"""Small real-source diagnostic run; never interprets relevance as claim support.

Uses the frozen validation_v1 holdout and production ML1 entry points. Writes a
checkpoint after each document/query. ML2 results require human grounding review;
an accepted model response is not automatically an acceptable business answer.
This is a component integration run, not a replacement for the Go HTTP smoke.
"""
import argparse
from dataclasses import asdict
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
from time import perf_counter
from uuid import uuid4

from ml.evaluation.dataset import build_corpus, load_dataset
from ml.evaluation.retrieval import recall_at, reciprocal_rank_at
from ml.evaluation.validation_study import HOLDOUT, aspect_coverage


def backend_payload(saved):
    return {
        'article': {
            'article_id': saved.document_id, 'article_version_id': saved.document_version_id,
            'version_no': 1, 'source_id': saved.source.source_id,
            'title': saved.title, 'body': saved.normalized_text,
            'published_at': saved.published_at, 'fetched_at': saved.retrieved_at,
            'raw_ref': saved.raw_ref, 'content_hash': saved.content_hash,
            'canonical_url': saved.canonical_url, 'language': saved.language,
            'metadata': saved.extraction.to_dict(),
        },
        'source': {k: getattr(saved.source, k) for k in (
            'source_id', 'name', 'source_type', 'source_role', 'trust_level', 'config_version')},
        'pipeline_version': 'ml-pipeline-v1', 'trace_id': str(uuid4()),
    }


class RecordingReranker:
    def __init__(self, scorer):
        self.scorer = scorer
        self.seconds = None
        self.scores = {}

    def score(self, query, candidates):
        from ml.reranking import keyed_scores
        started = perf_counter()
        result = self.scorer.score(query, candidates)
        self.seconds = perf_counter() - started
        self.scores = keyed_scores(candidates, result)
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    import psycopg
    from psycopg import sql
    from ml.adapters.postgres_catalog import load_versions
    from ml.chunking.tokenization import QwenTokenCounter
    from ml.embeddings import Embedder
    from ml.embeddings.qwen import QwenBatchEncoder
    from ml.migrate import apply_migrations
    from ml.pipeline import ML1Pipeline, PipelineConfig
    from ml.reranking import QwenReranker, INFERENCE_PROFILES

    dataset, checksum = load_dataset(HOLDOUT)
    report = {
        'scope': 'Real ML1 + optional real ML2; Go/HTTP/frontend not exercised',
        'dataset_sha256': checksum, 'document_count': len(dataset['documents']),
        'query_count': len(dataset['queries']), 'config': asdict(PipelineConfig()),
        'ingestion': [], 'queries': [], 'checks': {},
        'ml2_available': bool(os.environ.get('ML_LLM_API_KEY')),
    }

    def save():
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')

    save()
    counter = QwenTokenCounter()
    corpus = build_corpus(dataset, counter)  # Check frozen qrels BEFORE inference.
    embedder = Embedder(QwenBatchEncoder())
    recorder = RecordingReranker(QwenReranker(profile=INFERENCE_PROFILES['cpu-sdpa-b4']))
    agent = None
    if report['ml2_available']:
        from ml_runtime.model_client import create_agent_from_env
        agent = create_agent_from_env()
    schema = 'ml_e2e_' + uuid4().hex
    with psycopg.connect(os.environ['ML1_EVAL_POSTGRES_DSN'], autocommit=True) as conn:
        conn.execute(sql.SQL('CREATE SCHEMA {}').format(sql.Identifier(schema)))
        try:
            apply_migrations(conn, schema=schema)
            pipeline = ML1Pipeline(conn, embedder, counter, schema=schema, reranker=recorder)
            table = sql.Identifier(schema, 'document_chunks')
            for saved, expected in corpus:
                started = perf_counter()
                result = pipeline.index(backend_payload(saved), service_access=True)
                assert result.indexed and result.chunk_ids == tuple(c.id for c in expected)
                rows = conn.execute(sql.SQL(
                    'SELECT id::text, chunk_text, char_start, char_end, embedding::text '
                    'FROM {} WHERE document_version_id=%s ORDER BY chunk_no').format(table),
                    (saved.document_version_id,)).fetchall()
                assert len(rows) == len(expected)
                for _, text, start, end, vector in rows:
                    assert text == saved.normalized_text[start:end]
                    values = json.loads(vector)
                    assert len(values) == 1024 and all(math.isfinite(v) for v in values)
                report['ingestion'].append({'document_version_id': saved.document_version_id,
                    'chunk_count': len(rows), 'quality': result.quality.status,
                    'seconds': perf_counter() - started, 'text_and_vectors_valid': True})
                save()
                print('indexed', saved.document_version_id, len(rows), flush=True)

            before = conn.execute(sql.SQL('SELECT count(*) FROM {}').format(table)).fetchone()[0]
            repeated = pipeline.index(backend_payload(corpus[0][0]), service_access=True)
            assert repeated.indexed
            assert before == conn.execute(sql.SQL('SELECT count(*) FROM {}').format(table)).fetchone()[0]
            report['checks']['repeat_no_duplicates'] = True
            bad = backend_payload(corpus[0][0])
            bad['article'].update(article_id=str(uuid4()), article_version_id=str(uuid4()), body=' ')
            bad['article']['content_hash'] = hashlib.sha256(b' ').hexdigest()
            rejected = pipeline.index(bad, service_access=True)
            assert rejected.quality.status == 'FAIL' and not rejected.indexed
            assert before == conn.execute(sql.SQL('SELECT count(*) FROM {}').format(table)).fetchone()[0]
            report['checks']['quality_fail_not_indexed'] = True
            # New instance reloads persisted readiness; explicit service access is deployment-owned.
            ids = [c.id for _, group in corpus for c in group]
            pipeline = ML1Pipeline(conn, embedder, counter, schema=schema, reranker=recorder,
                versions=load_versions(conn, schema=schema), service_chunk_ids=ids)
            report['checks']['catalog_reloaded'] = True

            for query in dataset['queries']:
                started = perf_counter()
                retriever = pipeline._retriever()
                vector = retriever.search({'contract_version': 'v1', 'trace_id': str(uuid4()),
                    'business_context_id': None, 'query': query['text'], 'top_k': 20,
                    'filters': {'published_from': None, 'published_to': None, 'source_ids': [],
                        'source_roles': [], 'document_version_ids': [], 'include_undated': False}})
                vector_seconds = perf_counter() - started
                assert vector['status'] == 'ok'
                ranked = pipeline.search_with_scores(query['text'])
                assert ranked.reranker_applied
                v_ids = [r['chunk_id'] for r in vector['results']]
                top = [r['chunk_id'] for r in ranked.response['results']]
                gold = query['relevant_chunk_ids']
                missing20, missing5 = set(gold) - set(v_ids), set(gold) - set(top)
                category = ('CANDIDATE_RETRIEVAL_MISS' if missing20 else
                    'RERANKER_MISS' if missing5 else 'ML1_SUCCESS') if gold else 'NO_ANSWER_PENDING_ML2'
                item = {'query_id': query['query_id'], 'query': query['text'],
                    'expected': {k: query.get(k) for k in ('relevant_chunk_ids', 'evidence', 'rationale', 'aspects', 'no_answer_reason')},
                    'vector_top20': vector['results'], 'reranker_top5': ranked.response['results'],
                    'reranker_scores': recorder.scores,
                    'relevant_positions': [{'chunk_id': i, 'vector_rank': v_ids.index(i)+1 if i in v_ids else None,
                        'reranked_rank': top.index(i)+1 if i in top else None} for i in gold],
                    'recall_at_20': recall_at(gold, v_ids, 20) if gold else None,
                    'recall_at_5': recall_at(gold, top, 5) if gold else None,
                    'mrr_at_5': reciprocal_rank_at(gold, top, 5) if gold else None,
                    'aspect_coverage_at_5': aspect_coverage(query.get('aspects', []), top, 5),
                    'ml1_category': category, 'ml2_evidence': ranked.response['results'],
                    'ml2': {'status': 'BLOCKED_MISSING_GATEWAY_KEY'},
                    'ml2_grounded': None, 'final_acceptable': None,
                    'timing': {'retrieval_seconds': vector_seconds, 'reranker_seconds': recorder.seconds,
                        'ml2_seconds': None, 'component_request_seconds': None}}
                if agent:
                    request = json.loads(Path('docs/ml/fixtures/v1/analysis.request.json').read_text())
                    request.update(trace_id=str(uuid4()), request_operation_id=str(uuid4()))
                    request['context']['profile_features'] = []
                    request['context']['profile_data'] = {}
                    request['event'].update(title=query['text'], summary=query['text'],
                        document_version_ids=list(dict.fromkeys(c['document_version_id']
                            for c in item['ml2_evidence'])), status='candidate')
                    item['ml2_request'] = request
                    t = perf_counter()
                    try:
                        item['ml2'] = agent.run(request, item['ml2_evidence'])
                    except Exception as exc:
                        item['ml2'] = {'status': 'failed', 'error_type': type(exc).__name__,
                            'code': getattr(exc, 'code', 'UNEXPECTED_ERROR')}
                    item['timing']['ml2_seconds'] = perf_counter()-t
                item['timing']['component_request_seconds'] = perf_counter()-started
                report['queries'].append(item)
                save()
                print(query['query_id'], category, item['ml2']['status'], flush=True)
            answerable = [q for q in report['queries'] if q['recall_at_20'] is not None]
            report['metrics'] = {key: statistics.mean(q[key] for q in answerable)
                for key in ('recall_at_20', 'recall_at_5', 'mrr_at_5')}
            report['complete'] = True
            save()
        finally:
            conn.execute(sql.SQL('DROP SCHEMA {} CASCADE').format(sql.Identifier(schema)))


if __name__ == '__main__':
    main()
