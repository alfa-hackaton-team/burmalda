"""Run fixed qrels through Retriever v1; measure recall without quality gates."""
import argparse
from dataclasses import asdict
import hashlib
from importlib.metadata import version
import json
import math
import os
from pathlib import Path
import platform
from time import perf_counter
from uuid import uuid4

from ml.evaluation.dataset import DATASET, build_corpus, identity, load_dataset
from ml.shared.validation import validate_pair


def recall_at(expected, retrieved, k):
    if type(k) is not int or k < 1:
        raise ValueError('k must be a positive integer')
    expected, retrieved = tuple(expected), tuple(retrieved)
    if not expected or len(set(expected)) != len(expected):
        raise ValueError('Recall needs distinct positive judgments')
    if len(set(retrieved)) != len(retrieved):
        raise ValueError('Duplicate retrieved chunk IDs')
    return len(set(expected) & set(retrieved[:k])) / len(expected)


def reciprocal_rank_at(expected, retrieved, k):
    recall_at(expected, retrieved, k)  # Shared judgment/ranking validation.
    return next((1.0 / rank for rank, identity in enumerate(retrieved[:k], 1)
                 if identity in expected), 0.0)


def ndcg_at(expected, retrieved, k):
    """Binary relevance; ideal DCG includes every judged positive up to K."""
    recall_at(expected, retrieved, k)
    dcg = math.fsum(1.0 / math.log2(rank + 1) for rank, identity in enumerate(retrieved[:k], 1)
                    if identity in expected)
    ideal = math.fsum(1.0 / math.log2(rank + 1) for rank in range(1, min(k, len(expected)) + 1))
    return dcg / ideal


def evaluate(retriever, dataset, *, full_ranking=False, top_k=10):
    """Default K=10; optional complete fixture ranking uses the same search API."""
    labels = {c['chunk_id']: item['key'] for item in dataset['documents'] for c in item['chunks']}
    top_k = len(labels) if full_ranking else top_k
    if type(top_k) is not int or not 1 <= top_k <= 50:
        raise ValueError('Evaluation supports at most 50 candidates (Retriever v1 limit)')
    cutoffs = (5, 10, 20, 40) if full_ranking else (5, 10, 20) if top_k >= 20 else (5, 10)
    queries = []
    for query in dataset['queries']:
        request = {
            'contract_version': 'v1', 'trace_id': identity(f"search-trace:{query['query_id']}"),
            'business_context_id': identity('business-context'), 'query': query['text'], 'top_k': top_k,
            'filters': {'published_from': None, 'published_to': None, 'source_ids': [],
                        'source_roles': [], 'document_version_ids': [], 'include_undated': False},
        }
        started = perf_counter()
        response = retriever.search(request)
        search_seconds = perf_counter() - started
        validate_pair(request, response, 'search')
        if response['status'] not in ('ok', 'empty'):
            raise RuntimeError(f"Search incomplete for {query['query_id']}: {response['status']}")
        results = response['results']
        if any(r['chunk_id'] not in labels or r['retrieval_score'] is None
               or not math.isfinite(r['retrieval_score']) for r in results):
            raise ValueError('Unexpected corpus chunk or missing score')
        ids = [r['chunk_id'] for r in results]
        if full_ranking and set(ids) != set(labels):
            raise ValueError('Full-ranking diagnostics did not return the complete allowed fixture corpus')
        scores = [r['retrieval_score'] for r in results]
        if scores != sorted(scores, reverse=True):
            raise ValueError('Retrieval scores are not descending')
        expected = query['relevant_chunk_ids']
        first_relevant = next((r['rank'] for r in results if r['chunk_id'] in expected), None)
        measured = {
            f'{name}_at_{k}': function(expected, ids, k) if expected else None
            for name, function in (('recall', recall_at), ('reciprocal_rank', reciprocal_rank_at), ('ndcg', ndcg_at))
            for k in cutoffs
        }
        item = {
            **query,
            'search_seconds': search_seconds,
            'expected': [{'chunk_id': i, 'document_key': labels[i]} for i in expected],
            **measured,
            'missing_at_5': [i for i in expected if i not in ids[:5]],
            'missing_at_10': [i for i in expected if i not in ids[:10]],
            'hard_negatives_above_first_relevant': [
                r['chunk_id'] for r in results if r['chunk_id'] in query['hard_negative_chunk_ids']
                and (first_relevant is None or r['rank'] < first_relevant)
            ],
            'results': [{**r, 'document_key': labels[r['chunk_id']],
                         'relevant': r['chunk_id'] in expected,
                         'hard_negative': r['chunk_id'] in query['hard_negative_chunk_ids']} for r in results],
        }
        if full_ranking:
            by_id = {r['chunk_id']: r for r in item['results']}
            item['relevant_positions'] = [
                {'chunk_id': i, 'document_key': labels[i], 'rank': by_id[i]['rank'],
                 'score': by_id[i]['retrieval_score'],
                 'score_gap_from_top_10': (results[9]['retrieval_score'] - by_id[i]['retrieval_score']
                                           if len(results) >= 10 and by_id[i]['rank'] > 10 else None),
                 'outranking_hard_negatives': [
                     {'chunk_id': n, 'document_key': labels[n], 'rank': by_id[n]['rank'],
                      'score': by_id[n]['retrieval_score']}
                     for n in query['hard_negative_chunk_ids'] if by_id[n]['rank'] < by_id[i]['rank']
                 ]}
                for i in expected
            ]
            item['required_candidate_k'] = max((by_id[i]['rank'] for i in expected), default=None)
        queries.append(item)
    answered = [q for q in queries if q['relevant_chunk_ids']]
    aggregate = {
        f'{name}_at_{k}': (math.fsum(q[f'{field}_at_{k}'] for q in answered) / len(answered)) if answered else None
        for name, field in (('recall', 'recall'), ('mrr', 'reciprocal_rank'), ('ndcg', 'ndcg')) for k in cutoffs
    }
    return {
        'dataset_version': dataset['dataset_version'],
        'document_count': len(dataset['documents']), 'chunk_count': len(labels), 'query_count': len(queries),
        'answerable_query_count': len(answered), 'no_answer_query_count': len(queries) - len(answered),
        'positive_judgment_count': sum(len(q['relevant_chunk_ids']) for q in queries),
        'metric': 'macro Recall@K = mean_q(|relevant_q intersect top_K_q| / |relevant_q|)',
        'ranking_metric': 'MRR@K uses first positive; nDCG@K uses binary relevance; both macro averaged',
        'full_ranking': full_ranking, 'retrieved_k': top_k, **aggregate,
        'queries': queries,
    }


def run_pipeline(connection, dataset, embedder, counter, *, full_ranking=False, top_k=10):
    """Only a newly created evaluation schema is written/dropped, never ml/Go."""
    from psycopg import sql
    from psycopg.pq import TransactionStatus
    from ml.adapters.postgres_chunks import save_document_embeddings, save_document_version_and_chunks
    from ml.adapters.postgres_retrieval import PostgresRetriever, RETRIEVAL_CONFIG_VERSION
    from ml.chunking.profiles import V1 as chunking_profile
    from ml.embeddings.profile import V1 as embedding_profile
    from ml.migrate import apply_migrations
    from ml.retrieval.interfaces import DocumentVersion

    if connection.info.transaction_status != TransactionStatus.IDLE:
        raise ValueError('Evaluation requires an idle connection')
    corpus = build_corpus(dataset, counter)  # Fail stale qrels before any DB writes/model calls.
    schema = 'ml1_eval_' + uuid4().hex
    with connection.transaction():
        connection.execute(sql.SQL('CREATE SCHEMA {}').format(sql.Identifier(schema)))
    try:
        apply_migrations(connection, schema=schema)  # Existing DDL, no new migration files.
        catalog, ids = [], []
        for saved, chunks in corpus:
            save_document_version_and_chunks(connection, saved, chunks, schema=schema)
            ids.extend(c.id for c in chunks)
            catalog.append(DocumentVersion(saved.document_id, saved.document_version_id, 1, ready=True))
        chunks = tuple(c for _, group in corpus for c in group)
        vectors = {v.chunk_id: v for v in embedder.encode_chunks(chunks)}
        for _, group in corpus:
            save_document_embeddings(connection, group, tuple(vectors[c.id] for c in group), schema=schema)
        retriever = PostgresRetriever(connection, embedder, versions=catalog,
                                      context_chunks={identity('business-context'): ids}, schema=schema)
        report = evaluate(retriever, dataset, full_ranking=full_ranking, top_k=top_k)
        # These are reproducibility metadata, not new production DTO fields.
        with connection.cursor() as cursor:
            cursor.execute('SHOW server_version')
            postgres = cursor.fetchone()[0]
            cursor.execute("SELECT extversion FROM pg_extension WHERE extname='vector'")
            pgvector = cursor.fetchone()[0]
        root = Path(__file__).resolve().parents[2]
        paths = ('ml/adapters/postgres_retrieval.py', 'ml/embeddings/core.py', 'ml/embeddings/qwen.py',
                 'ml/embeddings/profile.py', 'ml/chunking/core.py', 'ml/chunking/profiles.py',
                 'ml/migrations/000001_document_storage.sql', 'ml/evaluation/retrieval.py',
                 'ml/evaluation/dataset.py')
        report['configuration'] = {
            'retrieval_config_version': RETRIEVAL_CONFIG_VERSION,
            'embedding_profile': asdict(embedding_profile), 'chunking_profile': asdict(chunking_profile),
            'batch_size': embedder.batch_size, 'device': 'cpu', 'dtype': 'float32',
            'top_k': report['retrieved_k'], 'catalog': 'fixture version_no=1, ready=true', 'access': 'all fixture chunks',
            'implementation_sha256': {p: hashlib.sha256((root / p).read_bytes()).hexdigest() for p in paths},
        }
        report['environment'] = {
            'python': platform.python_version(), 'platform': platform.platform(),
            'postgres': postgres, 'pgvector': pgvector,
            'packages': {p: version(p) for p in ('torch', 'transformers', 'tokenizers', 'psycopg')},
        }
        return report
    finally:
        connection.rollback()
        with connection.transaction():
            connection.execute(sql.SQL('DROP SCHEMA {} CASCADE').format(sql.Identifier(schema)))


def markdown_report(report):
    lines = [
        '# Vector-only retrieval: synthetic business news v1', '',
        'Вымышленные события; разметка зафиксирована до запуска модели. Это диагностический набор, не оценка production-качества.', '',
        f"Dataset SHA-256: `{report['dataset_sha256']}`", '',
        f"Документов: {report['document_count']}; чанков: {report['chunk_count']}; запросов: {report['query_count']}; positives: {report['positive_judgment_count']}.",
        f"Macro Recall@5: **{report['recall_at_5']:.6f}**; Macro Recall@10: **{report['recall_at_10']:.6f}**.", '',
        '`+` — релевантный; `!` — размеченный hard negative; отсутствие метки — нерелевантный. Scores = cosine similarity.',
        'Полные chunk UUID, тексты, конфигурация и окружение — в соседнем JSON.', '',
    ]
    for query in report['queries']:
        lines.extend([
            f"## {query['query_id']}: {query['text']}", '',
            'Ожидаются: ' + ', '.join(f"{e['document_key']} (`{e['chunk_id']}`)" for e in query['expected']) + '.',
            f"Recall@5={query['recall_at_5']:.3f}; Recall@10={query['recall_at_10']:.3f}. {query['rationale']}", '',
        ])
        for result in query['results']:
            marker = '+' if result['relevant'] else '!' if result['hard_negative'] else ''
            lines.append(f"{result['rank']}. **{result['document_key']}{marker}** — {result['retrieval_score']:.6f}")
        lines.append('')
    return '\n'.join(lines) + '\n'


def diagnostic_markdown(report):
    lines = ['# Vector retrieval: full-ranking diagnostics', '',
             f"Dataset SHA-256: `{report['dataset_sha256']}`", '',
             f"Тот же corpus, qrels, Qwen profile и pgvector search; диагностический top_k={report['retrieved_k']}.",
             'Recall@40=1 при выдаче всего разрешённого корпуса — проверка покрытия, не доказательство качества.', '',
             '| K | Macro Recall | MRR | Binary nDCG |', '|---|---:|---:|---:|']
    for k in (5, 10, 20, 40):
        lines.append(f"| {k} | {report[f'recall_at_{k}']:.6f} | {report[f'mrr_at_{k}']:.6f} | {report[f'ndcg_at_{k}']:.6f} |")
    lines.extend(['', '## Позиции всех релевантных чанков', '',
                  '| Query | Relevant chunks: rank (score) | K для всех positives |', '|---|---|---:|'])
    for query in report['queries']:
        positions = '; '.join(f"{p['document_key']}: {p['rank']} ({p['score']:.6f})" for p in query['relevant_positions'])
        lines.append(f"| {query['query_id']} | {positions} | {query['required_candidate_k']} |")
    lines.extend(['', '## Пропуски Top-10 и конкурирующие hard negatives', ''])
    for query in report['queries']:
        if not query['missing_at_10'] and not query['hard_negatives_above_first_relevant']:
            continue
        lines.extend([f"### {query['query_id']}: {query['text']}", '', query['rationale'], ''])
        for position in query['relevant_positions']:
            lines.append(f"- Positive **{position['document_key']}**: rank={position['rank']}, score={position['score']:.6f}.")
            if position['score_gap_from_top_10'] is not None:
                lines.append(f"  Отставание от 10-го места: {position['score_gap_from_top_10']:.6f}.")
            for negative in position['outranking_hard_negatives']:
                lines.append(f"  Hard negative **{negative['document_key']}**: rank={negative['rank']}, score={negative['score']:.6f}.")
        lines.append('')
    lines.extend(['## Полная выдача по каждому query', '', 'Метки: + positive, ! hard negative. Полные UUID/тексты — в JSON.', ''])
    for query in report['queries']:
        lines.extend([f"### {query['query_id']}: {query['text']}", ''])
        for result in query['results']:
            marker = '+' if result['relevant'] else '!' if result['hard_negative'] else ''
            lines.append(f"{result['rank']}. {result['document_key']}{marker}: {result['retrieval_score']:.6f}")
        lines.append('')
    return '\n'.join(lines) + '\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', type=Path, default=DATASET)
    parser.add_argument('--output', type=Path, required=True, help='Report JSON path; .md is written alongside')
    parser.add_argument('--batch-size', type=int, default=4)
    parser.add_argument('--full-ranking', action='store_true', help='Diagnose all fixture ranks; at most 50 chunks')
    args = parser.parse_args()
    dsn = os.environ.get('ML1_EVAL_POSTGRES_DSN')
    if not dsn:
        parser.error('ML1_EVAL_POSTGRES_DSN must point to a disposable PostgreSQL + pgvector database')
    dataset, checksum = load_dataset(args.dataset)
    from ml.chunking.tokenization import QwenTokenCounter
    from ml.embeddings import Embedder
    from ml.embeddings.qwen import QwenBatchEncoder
    import psycopg

    embedder = Embedder(QwenBatchEncoder(cache_dir=os.environ.get('ML1_MODEL_CACHE')), batch_size=args.batch_size)
    counter = QwenTokenCounter(cache_dir=os.environ.get('ML1_MODEL_CACHE'))
    with psycopg.connect(dsn, autocommit=True) as connection:
        report = run_pipeline(connection, dataset, embedder, counter, full_ranking=args.full_ranking)
    report['dataset_sha256'] = checksum
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    markdown = diagnostic_markdown(report) if args.full_ranking else markdown_report(report)
    args.output.with_suffix('.md').write_text(markdown, encoding='utf-8')
    print(markdown)


if __name__ == '__main__':
    main()
