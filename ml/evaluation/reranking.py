"""Evaluation-only experiment: real vector Top-20 -> Qwen reranker -> final 5/10."""
import argparse
import gc
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
from time import perf_counter

from ml.evaluation.dataset import DATASET, load_dataset
from ml.evaluation.qwen_reranker import QwenReranker, keyed_scores
from ml.evaluation.retrieval import ndcg_at, recall_at, reciprocal_rank_at, run_pipeline

BASELINE = Path(__file__).parent / 'reports' / 'vector_v1_diagnostics.json'


def metrics(expected, ids):
    return {f'{name}_at_{k}': function(expected, ids, k) if expected else None
            for name, function in (('recall', recall_at), ('reciprocal_rank', reciprocal_rank_at), ('ndcg', ndcg_at))
            for k in (5, 10, 20)}


def verify_vector_baseline(vector, baseline):
    if vector['dataset_sha256'] != baseline['dataset_sha256']:
        raise ValueError('Baseline dataset fingerprint differs')
    for key in ('embedding_profile', 'chunking_profile', 'retrieval_config_version', 'batch_size'):
        if vector['configuration'][key] != baseline['configuration'][key]:
            raise ValueError('Vector configuration changed; reranker experiment would be confounded')
    # Evaluation instrumentation changes are allowed, production implementation is fixed.
    old_hashes = baseline['configuration']['implementation_sha256']
    new_hashes = vector['configuration']['implementation_sha256']
    if any(new_hashes.get(path) != checksum for path, checksum in old_hashes.items()
           if not path.startswith('ml/evaluation/')):
        raise ValueError('Production vector implementation differs from baseline')
    if [q['query_id'] for q in vector['queries']] != [q['query_id'] for q in baseline['queries']]:
        raise ValueError('Baseline query set differs')
    maximum_difference = 0.0
    for current, old in zip(vector['queries'], baseline['queries'], strict=True):
        candidates, prefix = current['results'], old['results'][:20]
        if len(candidates) != 20 or [r['chunk_id'] for r in candidates] != [r['chunk_id'] for r in prefix]:
            raise ValueError('Vector candidate IDs/order changed')
        for actual, saved in zip(candidates, prefix, strict=True):
            if actual['chunk_text'] != saved['chunk_text']:
                raise ValueError('Candidate text changed')
            difference = abs(actual['retrieval_score'] - saved['retrieval_score'])
            if difference > 1e-6:
                raise ValueError('Vector scores changed beyond float32 tolerance')
            maximum_difference = max(maximum_difference, difference)
    return {'candidate_k': 20, 'all_candidate_ids_order_text_match': True,
            'max_vector_score_difference': maximum_difference}


def rerank_experiment(vector, scorer, *, progress=None):
    queries = []
    for query in vector['queries']:
        candidates = query['results']
        # No qrels, hard-negative flags, titles or scores go into the model.
        pairs = tuple({'chunk_id': c['chunk_id'], 'chunk_text': c['chunk_text']} for c in candidates)
        started = perf_counter()
        scores = keyed_scores(pairs, scorer.score(query['text'], pairs))
        elapsed = perf_counter() - started
        ranked = sorted(candidates, key=lambda c: (-scores[c['chunk_id']], c['rank'], c['chunk_id']))
        results = [{**c, 'rank': rank, 'vector_rank': c['rank'],
                    'reranker_score': scores[c['chunk_id']]}
                   for rank, c in enumerate(ranked, 1)]
        expected = query['relevant_chunk_ids']
        before_ids = [c['chunk_id'] for c in candidates]
        after_ids = [c['chunk_id'] for c in ranked]
        before, after = metrics(expected, before_ids), metrics(expected, after_ids)
        by_id = {r['chunk_id']: r for r in results}
        queries.append({
            'query_id': query['query_id'], 'text': query['text'], 'rationale': query['rationale'],
            'expected': query['expected'], 'relevant_chunk_ids': expected,
            'hard_negative_chunk_ids': query['hard_negative_chunk_ids'],
            'vector': before, 'reranked': after,
            'delta': {key: after[key] - before[key] if before[key] is not None else None for key in before},
            'answerable': bool(expected), 'no_answer_reason': query.get('no_answer_reason'),
            'aspects': query.get('aspects', []),
            'positive_positions': [{'chunk_id': i, 'document_key': by_id[i]['document_key'],
                                    'vector_rank': by_id[i]['vector_rank'], 'reranked_rank': by_id[i]['rank'],
                                    'vector_score': by_id[i]['retrieval_score'],
                                    'reranker_score': by_id[i]['reranker_score']}
                                   for i in expected if i in by_id],
            'missing_from_candidates': [i for i in expected if i not in by_id],
            'missing_at_5': [i for i in expected if i not in after_ids[:5]],
            'missing_at_10': [i for i in expected if i not in after_ids[:10]],
            'hard_negatives_above_first_relevant': [r['chunk_id'] for r in results
                if r['hard_negative'] and r['rank'] < min((p['rank'] for p in results if p['relevant']), default=21)],
            'vector_search_seconds': query.get('search_seconds'), 'rerank_seconds': elapsed,
            'results': results,
        })
        if progress:
            progress(query['query_id'])
    aggregate = {}
    answered = [q for q in queries if q['answerable']]
    for stage in ('vector', 'reranked'):
        aggregate[stage] = {f'{name}_at_{k}': (math.fsum(q[stage][f'{field}_at_{k}'] for q in answered) / len(answered)) if answered else None
                            for name, field in (('recall', 'recall'), ('mrr', 'reciprocal_rank'), ('ndcg', 'ndcg'))
                            for k in (5, 10, 20)}
    return {
        'dataset_version': vector['dataset_version'], 'dataset_sha256': vector['dataset_sha256'],
        'document_count': vector['document_count'], 'chunk_count': vector['chunk_count'],
        'query_count': len(queries), 'positive_judgment_count': vector['positive_judgment_count'],
        'answerable_query_count': len(answered), 'no_answer_query_count': len(queries) - len(answered),
        'candidate_k': 20, 'final_k': [5, 10], 'metrics': aggregate,
        'vector_configuration': vector['configuration'], 'environment': vector['environment'],
        'latency_note': 'Single local sequential CPU run; query 1 may include warmup; excludes model load. Not an SLA.',
        'rerank_seconds_median': statistics.median(q['rerank_seconds'] for q in queries),
        'rerank_seconds_max': max(q['rerank_seconds'] for q in queries), 'queries': queries,
    }


def markdown_report(report):
    lines = ['# Experiment: vector Top-20 → Qwen reranker → final Top-5/10', '',
             'Тот же synthetic dataset и qrels, без подстройки prompt/thresholds после запуска.',
             f"Dataset SHA-256: `{report['dataset_sha256']}`", '',
             '| Метрика | Vector only | Reranker | Delta |', '|---|---:|---:|---:|']
    for name in ('recall', 'mrr', 'ndcg'):
        for k in (5, 10):
            a, b = (report['metrics'][stage][f'{name}_at_{k}'] for stage in ('vector', 'reranked'))
            lines.append(f'| {name}@{k} | {a:.6f} | {b:.6f} | {b-a:+.6f} |')
    lines.extend(['', 'Cosine similarity и reranker logit difference хранятся отдельно; их нельзя сравнивать по величине.',
                  'Все 20 кандидатов оцениваются без доступа к relevance labels; финальная выдача — префикс.', '',
                  f"Rerank latency: median={report['rerank_seconds_median']:.3f}s, max={report['rerank_seconds_max']:.3f}s. {report['latency_note']}", '',
                  '## Все запросы: позиции positives', '',
                  '| Query | Positives: vector rank → reranked rank | Recall@5 до → после | nDCG@10 до → после |',
                  '|---|---|---|---|'])
    for q in report['queries']:
        positions = '; '.join(f"{p['document_key']}: {p['vector_rank']}→{p['reranked_rank']}" for p in q['positive_positions'])
        lines.append(f"| {q['query_id']} | {positions} | {q['vector']['recall_at_5']:.3f}→{q['reranked']['recall_at_5']:.3f} | {q['vector']['ndcg_at_10']:.3f}→{q['reranked']['ndcg_at_10']:.3f} |")
    lines.extend(['', '## Полные Top-20 после reranking', '', 'Метки: + positive, ! hard negative. Тексты/UUID — в JSON.', ''])
    for q in report['queries']:
        lines.extend([f"### {q['query_id']}: {q['text']}", '', q['rationale'], ''])
        for r in q['results']:
            marker = '+' if r['relevant'] else '!' if r['hard_negative'] else ''
            lines.append(f"{r['rank']}. **{r['document_key']}{marker}**: reranker={r['reranker_score']:.6f}; vector rank={r['vector_rank']}, cosine={r['retrieval_score']:.6f}")
        lines.append('')
    return '\n'.join(lines) + '\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', type=Path, default=DATASET)
    parser.add_argument('--baseline', type=Path, default=BASELINE)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    dsn = os.environ.get('ML1_EVAL_POSTGRES_DSN')
    if not dsn:
        parser.error('ML1_EVAL_POSTGRES_DSN must point to a disposable PostgreSQL + pgvector database')
    dataset, checksum = load_dataset(args.dataset)
    from ml.chunking.tokenization import QwenTokenCounter
    from ml.embeddings import Embedder
    from ml.embeddings.qwen import QwenBatchEncoder
    import psycopg

    started = perf_counter()
    embedder = Embedder(QwenBatchEncoder(cache_dir=os.environ.get('ML1_MODEL_CACHE')), batch_size=4)
    with psycopg.connect(dsn, autocommit=True) as connection:
        vector = run_pipeline(connection, dataset, embedder,
                              QwenTokenCounter(cache_dir=os.environ.get('ML1_MODEL_CACHE')), top_k=20)
    vector['dataset_sha256'] = checksum
    baseline_check = verify_vector_baseline(vector, json.loads(args.baseline.read_text()))
    vector_stage_seconds = perf_counter() - started
    del embedder
    gc.collect()
    started = perf_counter()
    reranker = QwenReranker(cache_dir=os.environ.get('ML1_MODEL_CACHE'))
    model_load_seconds = perf_counter() - started
    report = rerank_experiment(vector, reranker)
    report.update(baseline_check=baseline_check, reranker_configuration=reranker.configuration(),
                  vector_stage_seconds=vector_stage_seconds, reranker_load_seconds=model_load_seconds)
    source = Path(__file__).parent
    report['experiment_implementation_sha256'] = {
        p.name: hashlib.sha256(p.read_bytes()).hexdigest()
        for p in (source / 'reranking.py', source / 'qwen_reranker.py')
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    text = markdown_report(report)
    args.output.with_suffix('.md').write_text(text, encoding='utf-8')
    print(text)


if __name__ == '__main__':
    main()
