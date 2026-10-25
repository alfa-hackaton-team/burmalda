"""Trace ONE saved Top-5 through AnalysisAgent without indexing or retrieval."""
import argparse
from copy import deepcopy
import json
from pathlib import Path
from time import perf_counter, process_time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--case-id', required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--env-file', type=Path)
    parser.add_argument('--timeout', type=float, default=90)
    args = parser.parse_args()
    if args.env_file:
        from dotenv import load_dotenv
        load_dotenv(args.env_file)
    from ml_runtime.model_client import create_agent_from_env
    from ml_runtime.semantic_verifier import LLMSemanticVerifier

    source = json.loads(args.input.read_text())
    case = next(q for q in source['queries'] if q['query_id'] == args.case_id)
    request = deepcopy(case['ml2_request'])
    evidence = case['ml2_evidence']
    request['event']['document_version_ids'] = list(dict.fromkeys(
        c['document_version_id'] for c in evidence))
    report = {'case_id': args.case_id, 'query': case['query'], 'request': request,
        'evidence': evidence, 'timeout_seconds': args.timeout, 'events': [], 'model_calls': [],
        'tool_call_count': 0, 'retrieval_call_count': 0,
        'timing_limit': 'Non-streaming response cannot separate gateway queue from server generation without server timing.'}
    started = perf_counter()
    cpu_started = process_time()

    def save():
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')

    def event(name, **details):
        item = {'event': name, 'seconds_from_start': perf_counter()-started, **details}
        report['events'].append(item)
        save()
        print(name, round(item['seconds_from_start'], 3), flush=True)

    agent = create_agent_from_env()
    http_client = agent.client._client._client

    def trace(name, info):
        # Never record headers, bodies, credentials or hidden reasoning.
        details = {}
        if 'exception' in info:
            details['exception_type'] = type(info['exception']).__name__
        event('network.'+name, **details)

    def on_request(req):
        req.extensions['trace'] = trace
        event('gateway.request', host=req.url.host, path=req.url.path)

    def on_response(response):
        event('gateway.headers', status_code=response.status_code,
              server_timing=response.headers.get('server-timing'))

    http_client.event_hooks = {'request': [on_request], 'response': [on_response]}
    sdk_create = agent.client._client.chat.completions.create

    def traced_create(*values, **kwargs):
        if report['model_calls']:
            report['model_calls'][-1]['reasoning_effort'] = kwargs.get('reasoning_effort',
                kwargs.get('extra_body', {}).get('reasoning', {}).get('effort', 'provider_default'))
        result = sdk_create(*values, **kwargs)
        if report['model_calls']:
            call = report['model_calls'][-1]
            call['usage'] = result.usage.model_dump() if result.usage is not None else None
            call['finish_reasons'] = [choice.finish_reason for choice in result.choices]
        return result

    agent.client._client.chat.completions.create = traced_create
    original = agent.client.generate_json

    def record(**kwargs):
        call = {'model': kwargs['model_name'], 'budget_seconds': kwargs['timeout_seconds'],
                'input_bytes': len(json.dumps(kwargs['input_payload'], ensure_ascii=False).encode())}
        report['model_calls'].append(call)
        t = perf_counter()
        event('model.start', model=kwargs['model_name'])
        try:
            result = original(**kwargs)
            call['response'] = result
            return result
        except Exception as exc:
            call['error_type'] = type(exc).__name__
            call['code'] = getattr(exc, 'code', 'UNEXPECTED_ERROR')
            raise
        finally:
            call['seconds'] = perf_counter()-t
            event('model.finish', model=kwargs['model_name'], duration_seconds=call['seconds'])

    agent.client.generate_json = record
    verifier = LLMSemanticVerifier(agent.client)

    def timed_verifier(*values, **kwargs):
        t = perf_counter()
        event('verifier.start')
        try:
            return verifier(*values, **kwargs)
        finally:
            report['verifier_seconds'] = perf_counter()-t
            event('verifier.finish', duration_seconds=report['verifier_seconds'])

    agent.semantic_verifier = timed_verifier
    event('AnalysisAgent.start')
    try:
        report['response'] = agent.run(request, evidence, args.timeout)
    except Exception as exc:
        report['response'] = {'status': 'failed', 'error_type': type(exc).__name__,
            'code': getattr(exc, 'code', 'UNEXPECTED_ERROR'), 'message': str(exc)}
    finally:
        report['agent_seconds'] = perf_counter()-started
        report['process_cpu_seconds'] = process_time()-cpu_started
        event('AnalysisAgent.finish', status=report['response']['status'])
        agent.client._client.close()


if __name__ == '__main__':
    main()
