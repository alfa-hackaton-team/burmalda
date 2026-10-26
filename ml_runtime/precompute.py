"""One explicit analysis for the Go precompute command; JSON in/out, no retries.

The capture wrapper records exactly the chunks passed to AnalysisAgent, before
its mandatory production semantic verifier. No evidence search after analysis.
"""
from __future__ import annotations

import hashlib
import importlib.metadata
from copy import deepcopy
import json
import os
import sys
from pathlib import Path
from time import monotonic


def runtime_manifest():
    from .model_policy import DEFAULT_AGENT_MODEL, DEFAULT_VERIFIER_MODEL, require_allowed_model
    from .model_client import ALLOWED_BASE_URLS, DEFAULT_BASE_URL
    gateway = os.environ.get("ML_LLM_BASE_URL", DEFAULT_BASE_URL).rstrip("/")
    if gateway not in ALLOWED_BASE_URLS:
        raise ValueError("Only allowlisted gateway URLs may be persisted")
    root = Path(__file__).resolve().parents[1]
    # Hash source/config/contracts, never environment secrets or provider responses.
    files = sorted(set(root.glob('ml/**/*.py')) | set(root.glob('ml_runtime/**/*.py')))
    files = [p for p in files if 'tests' not in p.parts]
    files += [root / name for name in (
        'docs/ml/contracts/v1/contracts.schema.json', 'docs/api/schemas.yaml',
        'backend/internal/userapi/projection.go', 'backend/internal/userapi/precompute.go',
    )]
    digest = hashlib.sha256()
    for path in files:
        digest.update(str(path.relative_to(root)).encode())
        digest.update(path.read_bytes())
    return {
        'contract_version': 'v1', 'source_prompt_config_sha256': digest.hexdigest(),
        'gateway': gateway,
        'dependencies': {name: importlib.metadata.version(name) for name in ('torch', 'transformers', 'tokenizers', 'openai', 'psycopg')},
        'agent_model': require_allowed_model(os.environ.get('ML_LLM_MODEL', DEFAULT_AGENT_MODEL).strip()),
        'verifier_model': require_allowed_model(os.environ.get('ML_LLM_VERIFIER_MODEL', DEFAULT_VERIFIER_MODEL).strip()),
        'reasoning_effort': 'low', 'pipeline_config': 'candidate20-final5-cpu-sdpa-b4',
        'verifier': 'LLMSemanticVerifier',
    }


class CapturingAgent:
    def __init__(self, agent):
        if agent.semantic_verifier is None:
            raise ValueError('Production semantic verifier is mandatory')
        self.agent = agent
        self.chunks = []

    def run(self, request, chunks, timeout_seconds, *, analysis_run_id=None):
        self.chunks = json.loads(json.dumps(chunks))
        return self.agent.run(request, chunks, timeout_seconds, analysis_run_id=analysis_run_id)


class CapturingReranker:
    """Observe the existing Top-20 handoff without altering ranking or retries."""
    def __init__(self, reranker):
        self.reranker = reranker
        self.calls = []

    def score(self, query, candidates):
        call = {'candidates': deepcopy(candidates)}
        self.calls.append(call)
        result = self.reranker.score(query, candidates)
        call['scores'] = deepcopy(result)
        return result


class CapturingRetriever:
    def __init__(self, pipeline, reranker, diagnostics):
        self.pipeline, self.reranker, self.diagnostics = pipeline, reranker, diagnostics

    def search(self, request):
        start = len(self.reranker.calls)
        response = self.pipeline.search(request)
        self.diagnostics['searches'].append({
            'request': deepcopy(request), 'response': deepcopy(response),
            'reranker_calls': deepcopy(self.reranker.calls[start:]),
        })
        return response

    def get_chunks(self, request):
        return self.pipeline.get_chunks(request)


def capture_model_result(diagnostics, payload, result):
    """Keep contracted output/verdict only, never prompts or provider reasoning."""
    if 'response_schema' in payload:
        from .analysis_validation import AnalysisRequestError, validate_contract_message
        try:
            validate_contract_message(result, 'AnalysisResponse')
        except (AnalysisRequestError, ValueError, TypeError):
            diagnostics['invalid_candidate_shape'] = True
        else:
            diagnostics['generated_response'] = deepcopy(result)
    elif 'claim_records' in payload:
        # Only the published verifier protocol, not arbitrary provider fields.
        if set(result) == {'claim_checks', 'unattributed_assertions', 'overall_supported'}:
            checks = result['claim_checks']
            if isinstance(checks, list) and all(
                isinstance(check, dict) and set(check) == {'claim_id', 'supported', 'reason'}
                for check in checks
            ):
                diagnostics['verifier_verdict'] = deepcopy(result)


def analyze(envelope):
    from ml.adapters.postgres_catalog import load_versions
    from ml.chunking.tokenization import QwenTokenCounter
    from ml.database import connect_runtime
    from ml.embeddings import Embedder
    from ml.pipeline import ML1Pipeline
    from .ml1_model_client import RemoteEmbeddingEncoder, RemoteQwenReranker
    from .model_client import create_agent_from_env
    from .service import AnalysisService

    manifest = runtime_manifest()
    if envelope['manifest'] != manifest:
        raise ValueError('Runtime changed after scope preparation')
    request = envelope['request']
    versions = request['event']['document_version_ids']
    if not versions:
        raise ValueError('Demo requires an explicit frozen event document scope')
    started = monotonic()
    cache = os.environ.get('ML_MODEL_CACHE') or os.environ.get('HF_HOME')
    diagnostics = {'version': 'grounding-trace-v2', 'searches': []}
    with connect_runtime() as connection:
        reranker = CapturingReranker(RemoteQwenReranker())
        # Go provides the trusted, fingerprinted allowlist for this exact event.
        pipeline = ML1Pipeline(
            connection, Embedder(RemoteEmbeddingEncoder()),
            QwenTokenCounter(cache_dir=cache, local_files_only=True),
            versions=load_versions(connection),
            context_chunks={request['context']['business_context_id']: envelope['chunk_ids']},
            service_chunk_ids=(), reranker=reranker, cache_dir=cache,
        )
        production_agent = create_agent_from_env()
        calls = []
        original_generate = production_agent.client.generate_json
        def measured_generate(**kwargs):
            call = {'model': kwargs['model_name'], 'status': 'failed'}
            calls.append(call)
            call_started = monotonic()
            try:
                result = original_generate(**kwargs)
                capture_model_result(diagnostics, kwargs['input_payload'], result)
                call['status'] = 'returned'
                return result
            finally:
                call['seconds'] = monotonic() - call_started
        production_agent.client.generate_json = measured_generate
        production_verifier = production_agent.semantic_verifier
        def captured_verification(request, response, chunks, timeout_seconds):
            # Bind the verdict to the actual candidate after summary generation,
            # not the model's independent free-text draft.
            diagnostics['candidate_response'] = deepcopy(response)
            return production_verifier(request, response, chunks, timeout_seconds)
        production_agent.semantic_verifier = captured_verification
        agent = CapturingAgent(production_agent)
        retriever = CapturingRetriever(pipeline, reranker, diagnostics)
        response = AnalysisService(retriever, agent).analyze(request)
    return {'response': response, 'chunks': agent.chunks, 'manifest': manifest,
            'elapsed_seconds': monotonic() - started, 'metrics': {'model_calls': calls},
            'diagnostics': diagnostics}


def main():
    if sys.argv[1:] == ['--manifest']:
        from .model_client import create_agent_from_env
        create_agent_from_env()  # Validate production credentials/verifier before reserving work.
        print(json.dumps(runtime_manifest()))
        return
    envelope = json.load(sys.stdin)
    print(json.dumps(analyze(envelope), ensure_ascii=False))


if __name__ == '__main__':
    main()
