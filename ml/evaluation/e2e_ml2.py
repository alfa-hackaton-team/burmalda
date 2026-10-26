"""Replay the exact saved Top-5 through real ML2; no retrieval/model tuning."""
import argparse
from copy import deepcopy
import json
from pathlib import Path
from time import perf_counter


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--query-ids', nargs='+', help='Optional fixed subset for regression or independent cases')
    args = parser.parse_args()
    from ml_runtime.model_client import create_agent_from_env
    from ml_runtime.analysis_validation import validate_analysis_request
    from ml_runtime.semantic_verifier import LLMSemanticVerifier

    report = json.loads(args.input.read_text())
    if args.query_ids:
        selected = set(args.query_ids)
        report['queries'] = [q for q in report['queries'] if q['query_id'] in selected]
        if {q['query_id'] for q in report['queries']} != selected:
            raise ValueError('Unknown query IDs')
    previous = {}
    if args.output.exists():
        previous = {q['query_id']: q for q in json.loads(args.output.read_text())['queries']
                    if q.get('model_calls')}
    report['ml2_available'] = True
    report['ml2_replay_of'] = str(args.input)
    agent = create_agent_from_env()
    original = agent.client.generate_json
    calls = []

    def record(**kwargs):
        entry = {'model': kwargs['model_name'], 'system_prompt': kwargs['system_prompt'],
                 'input': kwargs['input_payload']}
        calls.append(entry)
        started = perf_counter()
        try:
            result = original(**kwargs)
            entry['response'] = result
            return result
        except Exception as exc:
            entry['error_type'] = type(exc).__name__
            entry['code'] = getattr(exc, 'code', 'UNEXPECTED_ERROR')
            raise
        finally:
            entry['seconds'] = perf_counter()-started

    agent.client.generate_json = record
    agent.semantic_verifier = LLMSemanticVerifier(agent.client)
    for item in report['queries']:
        if item['query_id'] in previous:
            item.update(previous[item['query_id']])
            continue
        calls.clear()
        request = deepcopy(item['ml2_request'])
        request['event']['document_version_ids'] = list(dict.fromkeys(
            c['document_version_id'] for c in item['ml2_evidence']))
        validate_analysis_request(request)
        item['ml2_request'] = request
        started = perf_counter()
        try:
            item['ml2'] = agent.run(request, item['ml2_evidence'])
        except Exception as exc:
            item['ml2'] = {'status': 'failed', 'error_type': type(exc).__name__,
                'code': getattr(exc, 'code', 'UNEXPECTED_ERROR'), 'message': str(exc)}
        item['model_calls'] = deepcopy(calls)
        item['timing']['ml2_seconds'] = perf_counter()-started
        item['timing']['component_request_seconds'] = sum(
            item['timing'][k] or 0 for k in ('retrieval_seconds', 'reranker_seconds', 'ml2_seconds'))
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
        print(item['query_id'], item['ml2']['status'], item['ml2'].get('code', ''), flush=True)
    report['ml2_complete'] = all(q.get('model_calls') for q in report['queries'])
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')


if __name__ == '__main__':
    main()
