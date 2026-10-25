"""Frozen real-source holdout, generic aspect coverage, and inference profiles.

Evaluation only: no automatic query decomposition, abstention threshold, or production changes.
"""
import argparse
from copy import deepcopy
import gc
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
from time import perf_counter

from ml.evaluation.dataset import DATASET, load_dataset
from ml.evaluation.qwen_reranker import INFERENCE_PROFILES, QwenReranker, keyed_scores
from ml.evaluation.reranking import metrics, rerank_experiment
from ml.evaluation.retrieval import run_pipeline

HOLDOUT = Path(__file__).parent / 'data/official_holdout_v1.json'


def aspect_coverage(aspects, ids, k):
    if not aspects:
        return None
    return sum(bool(set(a['relevant_chunk_ids']) & set(ids[:k])) for a in aspects) / len(aspects)


def balanced_ranking(ranked_aspects):
    """Unweighted round robin, input aspect order, duplicate IDs emitted once."""
    result, seen = [], set()
    for position in range(max(map(len, ranked_aspects), default=0)):
        for ranking in ranked_aspects:
            if position < len(ranking) and ranking[position] not in seen:
                seen.add(ranking[position])
                result.append(ranking[position])
    return result


def aspect_experiment(vector, reference, scorer, *, progress=None):
    by_query = {q['query_id']: q for q in reference['queries']}
    queries = []
    for query in vector['queries']:
        aspects = query.get('aspects', [])
        if len(aspects) < 2:
            continue
        candidates = query['results']
        pairs = tuple({'chunk_id': c['chunk_id'], 'chunk_text': c['chunk_text']} for c in candidates)
        rankings, outputs = [], []
        started = perf_counter()
        for aspect in aspects:
            scores = keyed_scores(pairs, scorer.score(aspect['text'], pairs))
            order = sorted(candidates, key=lambda c: (-scores[c['chunk_id']], c['rank'], c['chunk_id']))
            ranking = [c['chunk_id'] for c in order]
            rankings.append(ranking)
            outputs.append({'text': aspect['text'], 'ranking': ranking, 'scores': scores})
        ids = balanced_ranking(rankings)
        elapsed = perf_counter() - started
        if set(ids) != {c['chunk_id'] for c in candidates}:
            raise ValueError('Aspect fusion changed candidate membership')
        original = by_query[query['query_id']]
        original_ids = [r['chunk_id'] for r in original['results']]
        expected = query['relevant_chunk_ids']
        queries.append({
            'query_id': query['query_id'], 'text': query['text'], 'aspects': aspects,
            'relevant_chunk_ids': expected, 'reference': metrics(expected, original_ids),
            'multi_aspect': metrics(expected, ids),
            'reference_coverage': {str(k): aspect_coverage(aspects, original_ids, k) for k in (1, 2, 5, 10)},
            'multi_aspect_coverage': {str(k): aspect_coverage(aspects, ids, k) for k in (1, 2, 5, 10)},
            'reference_ranking': original_ids, 'ranking': ids, 'aspect_scores': outputs,
            'seconds': elapsed,
            'candidate_coverage': sum(i in ids for i in expected) / len(expected),
        })
        if progress:
            progress(query['query_id'])
    if not queries:
        raise ValueError('No multi-aspect queries in dataset')
    summary = {}
    for stage in ('reference', 'multi_aspect'):
        summary[stage] = {key: statistics.mean(q[stage][key] for q in queries)
                          for key in queries[0][stage]}
    return {'method': 'manual query-only aspects; unweighted round-robin fusion, no score averaging',
            'limitations': 'Aspects are supplied manually; this does not evaluate an automatic decomposer.',
            'query_count': len(queries), 'metrics': summary,
            'coverage_at_2': {s: statistics.mean(q[f'{s}_coverage']['2'] for q in queries)
                              for s in ('reference', 'multi_aspect')}, 'queries': queries}


def profile_comparison(reference, current):
    if [q['query_id'] for q in reference['queries']] != [q['query_id'] for q in current['queries']]:
        raise ValueError('Inference profile query set differs')
    maximum, changed_top1, changed_top5 = 0.0, [], []
    for a, b in zip(reference['queries'], current['queries'], strict=True):
        old = {r['chunk_id']: r for r in a['results']}
        if set(old) != {r['chunk_id'] for r in b['results']}:
            raise ValueError('Inference profiles must score exactly the same candidates')
        for r in b['results']:
            maximum = max(maximum, abs(r['reranker_score'] - old[r['chunk_id']]['reranker_score']))
        if a['results'][0]['chunk_id'] != b['results'][0]['chunk_id']:
            changed_top1.append(a['query_id'])
        if [r['chunk_id'] for r in a['results'][:5]] != [r['chunk_id'] for r in b['results'][:5]]:
            changed_top5.append(a['query_id'])
    return {'max_abs_score_difference': maximum, 'changed_top1_query_ids': changed_top1,
            'changed_top5_order_query_ids': changed_top5,
            'metric_delta': {k: current['metrics']['reranked'][k] - reference['metrics']['reranked'][k]
                             for k in reference['metrics']['reranked']},
            'median_latency_speedup': reference['rerank_seconds_median'] / current['rerank_seconds_median']}


def save(path, report):
    path.parent.mkdir(parents=True, exist_ok=True)
    report['study_implementation_sha256'] = {
        name: hashlib.sha256((Path(__file__).parent / name).read_bytes()).hexdigest()
        for name in ('validation_study.py', 'qwen_reranker.py', 'dataset.py', 'retrieval.py', 'reranking.py')}
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


def check_frozen_vector(vector):
    dataset, checksum = load_dataset(HOLDOUT)
    if vector['dataset_sha256'] != checksum:
        raise ValueError('Holdout changed after vector candidates were frozen')
    by_id = {q['query_id']: q for q in dataset['queries']}
    for q in vector['queries']:
        if q['text'] != by_id[q['query_id']]['text'] or q['relevant_chunk_ids'] != by_id[q['query_id']]['relevant_chunk_ids']:
            raise ValueError('Holdout queries or judgments changed')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('stage', choices=('vector', 'profile', 'aspects'))
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--profile', choices=tuple(INFERENCE_PROFILES), default='cpu-eager-b4')
    args = parser.parse_args()
    directory = args.output_dir
    if args.stage == 'vector':
        import psycopg
        from ml.chunking.tokenization import QwenTokenCounter
        from ml.embeddings import Embedder
        from ml.embeddings.qwen import QwenBatchEncoder
        dsn = os.environ.get('ML1_EVAL_POSTGRES_DSN')
        if not dsn:
            parser.error('ML1_EVAL_POSTGRES_DSN required for disposable PostgreSQL + pgvector')
        dataset, checksum = load_dataset(HOLDOUT)
        embedder = Embedder(QwenBatchEncoder(cache_dir=os.environ.get('ML1_MODEL_CACHE')), batch_size=4)
        with psycopg.connect(dsn, autocommit=True) as connection:
            report = run_pipeline(connection, dataset, embedder,
                                  QwenTokenCounter(cache_dir=os.environ.get('ML1_MODEL_CACHE')), top_k=20)
        report['dataset_sha256'] = checksum
        save(directory / 'holdout_vector.json', report)
        print('VECTOR', {k: report[k] for k in ('recall_at_5', 'recall_at_10', 'recall_at_20', 'mrr_at_10', 'ndcg_at_10')}, flush=True)
        return
    vector = json.loads((directory / 'holdout_vector.json').read_text())
    check_frozen_vector(vector)
    if args.stage == 'profile':
        profile = INFERENCE_PROFILES[args.profile]
        started = perf_counter()
        scorer = QwenReranker(cache_dir=os.environ.get('ML1_MODEL_CACHE'), profile=profile)
        load_seconds = perf_counter() - started
        warmup = vector['queries'][0]
        pairs = [{'chunk_id': r['chunk_id'], 'chunk_text': r['chunk_text']} for r in warmup['results'][:4]]
        for _ in range(2):
            scorer.score(warmup['text'], pairs)
        report = rerank_experiment(vector, scorer, progress=lambda key: print(args.profile, key, flush=True))
        # A repeated fixed query checks profile stability without turning timings into quality thresholds.
        repeat_query = vector['queries'][0]
        repeat_pairs = [{'chunk_id': r['chunk_id'], 'chunk_text': r['chunk_text']} for r in repeat_query['results']]
        repeated = dict(scorer.score(repeat_query['text'], repeat_pairs))
        original = report['queries'][0]['results']
        report['repeat_max_abs_difference'] = max(abs(r['reranker_score'] - repeated[r['chunk_id']]) for r in original)
        report['reranker_configuration'] = scorer.configuration()
        report['model_load_seconds'] = load_seconds
        report['warmup'] = '2 batches of 4 fixed first-query pairs; not included in reported query times'
        baseline_path = directory / 'holdout_cpu-eager-b4.json'
        if args.profile != 'cpu-eager-b4' and baseline_path.exists():
            report['comparison_to_cpu_eager_b4'] = profile_comparison(json.loads(baseline_path.read_text()), report)
        save(directory / f'holdout_{args.profile}.json', report)
        print('PROFILE', args.profile, report['metrics']['reranked'], 'median', report['rerank_seconds_median'], flush=True)
        return
    reference = json.loads((directory / 'holdout_cpu-eager-b4.json').read_text())
    scorer = QwenReranker(cache_dir=os.environ.get('ML1_MODEL_CACHE'))
    report = aspect_experiment(vector, reference, scorer, progress=lambda key: print('ASPECT', key, flush=True))
    report['dataset_sha256'] = vector['dataset_sha256']
    report['reranker_configuration'] = scorer.configuration()
    save(directory / 'holdout_aspects.json', report)
    # Development regression set is kept separate, never counted as holdout performance.
    old_vector = json.loads((Path(__file__).parent / 'reports/vector_v1_diagnostics.json').read_text())
    old_reference = json.loads((Path(__file__).parent / 'reports/reranker_v1.json').read_text())
    annotations_path = Path(__file__).parent / 'data/synthetic_aspects_v1.json'
    annotations = {q['query_id']: q['aspects'] for q in json.loads(annotations_path.read_text())['queries']}
    for q in old_vector['queries']:
        q['results'] = q['results'][:20]
        q['aspects'] = annotations.get(q['query_id'], [])
    old_report = aspect_experiment(old_vector, old_reference, scorer, progress=lambda key: print('DEVELOPMENT ASPECT', key, flush=True))
    old_report['dataset_sha256'] = old_vector['dataset_sha256']
    old_report['annotations_sha256'] = hashlib.sha256(annotations_path.read_bytes()).hexdigest()
    save(directory / 'development_aspects.json', old_report)
    print('ASPECT SUMMARY', report['metrics'], report['coverage_at_2'], flush=True)


if __name__ == '__main__':
    main()
