"""Run existing eight adversarial evidence cases through the deployed verifier.

One batched call records per-claim verdicts. This tests verification, not the
agent's ability to produce answers. No scores or gold labels enter model input.
"""
import argparse
from copy import deepcopy
import json
from pathlib import Path
from time import perf_counter
from uuid import uuid4


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    from ml.chunking.tokenization import QwenTokenCounter
    from ml.evaluation.dataset import build_corpus, load_dataset
    from ml.evaluation.validation_study import HOLDOUT
    from ml_runtime.model_client import OpenAICompatibleModelClient
    from ml_runtime.semantic_verifier import LLMSemanticVerifier
    import os

    cases = json.loads(Path('ml/evaluation/data/ml2_evidence_cases_v1.json').read_text())['cases']
    dataset, checksum = load_dataset(HOLDOUT)
    chunks = [{'chunk_id': c.id, 'chunk_text': c.chunk_text,
               'document_version_id': s.document_version_id, 'source_role': s.source.source_role,
               'published_at': s.published_at, 'title': s.title, 'canonical_url': s.canonical_url}
              for s, group in build_corpus(dataset, QwenTokenCounter()) for c in group]
    request = json.loads(Path('docs/ml/fixtures/v1/analysis.request.json').read_text())
    response = json.loads(Path('docs/ml/fixtures/v1/analysis.ok.json').read_text())
    template = response['insight']['claims'][0]
    claims = []
    mapping = {}
    for case in cases:
        claim = deepcopy(template)
        claim.update(id=str(uuid4()), position=len(claims)+1, claim_type='event_fact',
            claim_text=case['claim_text'], knowledge_type='FACT', assumptions=[])
        claim['evidence'] = [{**template['evidence'][0], 'chunk_id': case['chunk_id'],
                              'quote_text': case['quote_text']}]
        claims.append(claim)
        mapping[claim['id']] = case
    response['insight'].update(claims=claims, summary='', consequences=[], recommended_actions=[])
    client = OpenAICompatibleModelClient(os.environ['ML_LLM_API_KEY'],
        os.environ.get('ML_LLM_BASE_URL', 'https://changellenge.up.railway.app/v1'))
    original = client.generate_json
    calls = []

    def record(**kwargs):
        started = perf_counter()
        result = original(**kwargs)
        calls.append({'model': kwargs['model_name'], 'input': kwargs['input_payload'],
                      'response': result, 'seconds': perf_counter()-started})
        return result

    client.generate_json = record
    verdict = LLMSemanticVerifier(client)(request, response, chunks, 90)
    checks = calls[0]['response']['claim_checks'] if calls else []
    results = [{**mapping[c['claim_id']], 'actual_supported': c['supported'], 'reason': c['reason'],
                'correct': c['supported'] == mapping[c['claim_id']]['expected_publishable']}
               for c in checks if c['claim_id'] in mapping]
    report = {'dataset_sha256': checksum, 'scope': 'real verifier, gold adversarial claims; not generated answers',
              'overall_verdict': verdict, 'cases': results, 'calls': calls,
              'correct': sum(c['correct'] for c in results), 'total': len(cases)}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
    print('Verifier cases:', report['correct'], '/', report['total'])


if __name__ == '__main__':
    main()
