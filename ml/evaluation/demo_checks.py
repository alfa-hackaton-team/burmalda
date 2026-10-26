"""Bounded h01/h08/h16 ML2 replay with frozen, explicitly selected evidence.

This does not test retrieval or load embedding/reranker weights. The request
context is a test fixture, never a real personal demo context. Uses the existing
production-agent replay runner, including its mandatory semantic verifier.

Run: python -m ml.evaluation.demo_checks --output .local/demo-checks.json
Inspect claims, citations and attribution in the local report before accepting.
"""
import argparse
import hashlib
import json
from pathlib import Path
import sys
from uuid import uuid4

from ml.evaluation.dataset import build_corpus, load_dataset


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--tokenizer-cache', default=None)
    args = parser.parse_args()
    source = args.output.with_suffix('.input.json')
    if args.output.exists() or source.exists():
        parser.error('Choose fresh output paths; no implicit retries or reused results')

    from ml.chunking.tokenization import QwenTokenCounter
    from ml.evaluation.e2e_ml2 import main as replay

    dataset, checksum = load_dataset(Path('ml/evaluation/data/official_holdout_v1.json'))
    selection = {'h01': 'holdout_cbr_20240726', 'h08': 'holdout_cbr_20251024',
                 'h16': 'holdout_cbr_20250725'}
    # h16 deliberately receives a related key-rate release, not empty evidence:
    # it does not establish Alfa-Bank's six-month corporate deposit product rate.
    subset = {**dataset, 'documents': [d for d in dataset['documents']
                                     if d['key'] in selection.values()]}
    corpus = build_corpus(subset, QwenTokenCounter(cache_dir=args.tokenizer_cache))
    evidence = {}
    for document, (saved, chunks) in zip(subset['documents'], corpus):
        evidence[document['key']] = [{
            'chunk_id': chunk.id, 'chunk_text': chunk.chunk_text,
            **{key: getattr(saved, key) for key in (
                'document_id', 'document_version_id', 'title', 'canonical_url',
                'published_at', 'retrieved_at')},
            'source_id': saved.source.source_id, 'source_name': saved.source.name,
            **{key: getattr(saved.source, key) for key in (
                'source_type', 'source_role', 'trust_level')},
        } for chunk in chunks]

    report = {'scope': 'ML2 only; selected frozen evidence, not historical Top-5 or ML1',
              'dataset_sha256': checksum, 'queries': []}
    for query in dataset['queries']:
        if query['query_id'] not in selection:
            continue
        chunks = evidence[selection[query['query_id']]]
        assert set(query['relevant_chunk_ids']) <= {c['chunk_id'] for c in chunks}
        request = json.loads(Path('docs/ml/fixtures/v1/analysis.request.json').read_text())
        request.update(trace_id=str(uuid4()), request_operation_id=str(uuid4()))
        request['context'].update(profile_data={}, profile_features=[])
        request['event'].update(title=query['text'], summary=query['text'],
                                status='candidate', geography=[],
                                document_version_ids=list(dict.fromkeys(
                                    c['document_version_id'] for c in chunks)))
        report['queries'].append({
            'query_id': query['query_id'], 'query': query['text'],
            'expected_status': 'succeeded' if query.get('answerable') is not False
                               else 'insufficient_evidence',
            'expected': query, 'ml2_request': request, 'ml2_evidence': chunks,
            'evidence_sha256': hashlib.sha256(json.dumps(
                chunks, ensure_ascii=False, sort_keys=True).encode()).hexdigest(),
            'timing': {'retrieval_seconds': None, 'reranker_seconds': None,
                       'ml2_seconds': None},
        })
    source.parent.mkdir(parents=True, exist_ok=True)
    source.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    sys.argv = ['e2e_ml2', '--input', str(source), '--output', str(args.output)]
    replay()
    results = json.loads(args.output.read_text())['queries']
    if any(q['ml2']['status'] != q['expected_status'] for q in results):
        raise SystemExit('Selected status checks failed; inspect the report, no automatic retry')


if __name__ == '__main__':
    main()
