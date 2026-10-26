"""Summarize traces without treating schema/verifier acceptance as human review."""
import argparse
from collections import Counter
import json
from pathlib import Path
import statistics


def replay_handoff(report):
    """Replay recorded stage durations at the service boundary, WITHOUT an LLM.

    This quantifies configuration drops, not real end-to-end answer success.
    """
    from copy import deepcopy
    from unittest.mock import patch
    from ml_runtime.service import AnalysisService
    results = []
    for case in report['queries']:
        clock = [0.0]
        delivered = []
        ranked = sorted(case['vector_top20'], key=lambda r: -case['reranker_scores'][r['chunk_id']])
        request = deepcopy(case['ml2_request'])
        request['event']['document_version_ids'] = list(dict.fromkeys(
            r['document_version_id'] for r in ranked))

        class RecordedRetriever:
            def search(self, query):
                clock[0] += case['timing']['retrieval_seconds'] + case['timing']['reranker_seconds']
                delivered.extend({**r, 'rank': i} for i, r in enumerate(ranked[:query['top_k']], 1))
                return {'contract_version': 'v1', 'trace_id': query['trace_id'], 'status': 'ok',
                        'results': delivered, 'warnings': [], 'error': None}

            def get_chunks(self, query):
                return {'contract_version': 'v1', 'trace_id': query['trace_id'], 'status': 'ok',
                    'results': [{k: v for k, v in r.items() if k not in ('rank', 'retrieval_score')}
                        for r in delivered], 'warnings': [], 'error': None, 'unresolved_chunk_ids': []}

        class HandoffSentinel:
            def run(self, request, chunks, remaining):
                return {'status': 'EVIDENCE_DELIVERED', 'warnings': [], 'chunk_count': len(chunks)}

        with patch('ml_runtime.search_plan.monotonic', side_effect=lambda: clock[0]), \
                patch('ml_runtime.service.monotonic', side_effect=lambda: clock[0]):
            response = AnalysisService(RecordedRetriever(), HandoffSentinel()).analyze(request)
        results.append({'query_id': case['query_id'], 'response': response,
            'recorded_retrieval_and_rerank_seconds': clock[0]})
    return {'scope': 'deterministic replay of measured durations; sentinel agent, NOT a model/HTTP E2E',
            'delivered': sum(r['response']['status'] == 'EVIDENCE_DELIVERED' for r in results),
            'total': len(results), 'cases': results}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--handoff-output', type=Path)
    args = parser.parse_args()
    report = json.loads(args.input.read_text())
    if args.handoff_output:
        args.handoff_output.write_text(json.dumps(replay_handoff(report), ensure_ascii=False, indent=2)+'\n')
    queries = report['queries']
    answered = [q for q in queries if q['recall_at_20'] is not None]
    metrics = {key: statistics.mean(q[key] for q in answered)
               for key in ('recall_at_20', 'recall_at_5', 'mrr_at_5')} if answered else {}
    categories = Counter(q['ml1_category'] for q in queries)
    categories['MULTI_ASPECT_MISS'] = sum(q['aspect_coverage_at_5'] is not None
        and q['aspect_coverage_at_5'] < 1 for q in queries)
    categories['LATENCY_PROBLEM'] = sum(q['timing']['retrieval_seconds']
        + q['timing']['reranker_seconds'] > 10 for q in queries)
    model_statuses = Counter(q['ml2']['status'] for q in queries if q.get('model_calls'))
    lines = ['# Baseline diagnostic table', '',
        'Component run; full Go → frontend E2E is not demonstrated. '
        'ML1 misses count missing gold chunks, even if another aspect is present.', '',
        f'Documents: {report["document_count"]}; queries recorded: {len(queries)}/{report["query_count"]}.',
        f'Dataset SHA-256: `{report["dataset_sha256"]}`.', '',
        'ML2 rows with AnalysisRequestError belong to the corrected evaluation harness, '
        'not to production failures. Use baseline_ml2.json for real ML2 results.', '',
        '| Case | Top20 gold recall | Top5 gold recall | Aspect coverage | ML2 status | Grounded (review) | Acceptable (review) | ML1 category |',
        '|---|---:|---:|---:|---|---|---|---|']
    for q in queries:
        value = lambda x: 'N/A' if x is None else str(round(x, 3)) if isinstance(x, float) else str(x)
        lines.append('| '+' | '.join(map(str, (q['query_id'],value(q['recall_at_20']),
            value(q['recall_at_5']),value(q['aspect_coverage_at_5']),q['ml2']['status'],
            value(q.get('ml2_grounded')),value(q.get('final_acceptable')),q['ml1_category'])))+' |')
    lines.extend(['', 'Metrics (answerable only): `'+json.dumps(metrics)+'`.', '',
                  'Category counts (can overlap): `'+json.dumps(categories)+'`.', '',
                  'Real ML2 statuses: `'+json.dumps(model_statuses)+'`.', '',
                  'Latency medians, seconds:'])
    for key in ('retrieval_seconds', 'reranker_seconds', 'ml2_seconds', 'component_request_seconds'):
        measured = [q['timing'][key] for q in queries if q['timing'].get(key) is not None]
        if measured:
            lines.append(f'- {key}: {statistics.median(measured):.3f} (max {max(measured):.3f})')
    lines.extend(['', 'No-answer results:'])
    for q in queries:
        if q['recall_at_20'] is None:
            lines.append(f'- {q["query_id"]}: {q["ml2"]["status"]}; {q["query"]}')
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text('\n'.join(lines)+'\n')


if __name__ == '__main__':
    main()
