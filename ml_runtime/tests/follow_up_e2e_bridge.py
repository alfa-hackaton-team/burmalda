"""TEST ONLY HTTP adapter for backend E2E; not a production ML server.

Real AnalysisService/AnalysisAgent/guard/verifier adapter, deterministic model
responses, fail-on-call search boundary. Never reads gateway credentials.
"""
import argparse
import hmac
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from unittest.mock import Mock, patch
from uuid import uuid4

from ml_runtime.agent import AnalysisAgent
from ml_runtime.citation_support import ensure_attributed_support
from ml_runtime.semantic_verifier import LLMSemanticVerifier
from ml_runtime.service import AnalysisService
from ml_runtime.tests.test_follow_up_snapshot import SnapshotClient


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--port', type=int, default=8099)
    args = parser.parse_args()
    token = os.environ['FOLLOW_UP_E2E_TOKEN']
    counters = dict(requests=0, generation=0, guard=0, verifier=0,
                    search=0, get_chunks=0, search_path=0)

    def forbidden(name):
        def call(*args, **kwargs):
            counters[name] += 1
            raise AssertionError('Follow-up entered forbidden ' + name)
        return call

    class Client(SnapshotClient):
        def generate_json(self, **kwargs):
            if 'claim_records' in kwargs['input_payload']:
                counters['verifier'] += 1
            else:
                counters['generation'] += 1
            result = super().generate_json(**kwargs)
            if result.get('message'):
                message = result['message']
                message['id'] = str(uuid4())
                for claim in message['claims']:
                    old, claim['id'] = claim['id'], str(uuid4())
                    for citation in message['citations']:
                        if citation['claim_id'] == old:
                            citation['claim_id'] = claim['id']
            return result

    def guard(*args):
        counters['guard'] += 1
        return ensure_attributed_support(*args)

    class Handler(BaseHTTPRequestHandler):
        def send(self, status, payload):
            body = json.dumps(payload).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def authenticated(self):
            return hmac.compare_digest(self.headers.get('Authorization', ''), 'Bearer ' + token)

        def do_GET(self):
            if not self.authenticated():
                return self.send(401, {'code': 'UNAUTHENTICATED'})
            self.send(200, counters) if self.path == '/test/stats' else self.send(404, {})

        def do_POST(self):
            if not self.authenticated():
                return self.send(401, {'code': 'UNAUTHENTICATED'})
            if self.path != '/internal/v0.1/follow-ups':
                return self.send(404, {})
            try:
                length = int(self.headers.get('Content-Length', '0'))
                if not 0 < length <= 1_000_000:
                    return self.send(413, {})
                request = json.loads(self.rfile.read(length))
                if request['request_type'] != 'follow_up':
                    return self.send(400, {})
                mode = request['follow_up']['question']
                def edit(result):
                    if mode == 'insufficient':
                        result.update(status='insufficient_evidence', message=None,
                                      gaps=[{'code': 'NO_EVENT_EVIDENCE',
                                             'description': 'Not established by the saved card.',
                                             'required_feature_key': None}])
                    elif mode == 'bad-quote':
                        result['message']['claims'][0]['evidence'][0]['quote_text'] = 'Invented quote'
                    elif mode == 'outside-chunk':
                        result['message']['claims'][0]['evidence'][0]['chunk_id'] = str(uuid4())
                client = Client(edit=edit, supported=mode != 'reject')
                retriever = Mock(spec=['search', 'get_chunks'])
                retriever.search.side_effect = forbidden('search')
                retriever.get_chunks.side_effect = forbidden('get_chunks')
                counters['requests'] += 1
                with patch('ml_runtime.agent.ensure_attributed_support', side_effect=guard), \
                     patch('ml_runtime.service.BoundedSearchRunner', side_effect=forbidden('search_path')):
                    result = AnalysisService(retriever, AnalysisAgent(
                        client, semantic_verifier=LLMSemanticVerifier(client))).analyze(request)
                self.send(200, result)
            except Exception as exc:
                self.send(500, {'code': 'TEST_BRIDGE_ERROR', 'type': type(exc).__name__})

        def log_message(self, *args):
            pass

    HTTPServer(('0.0.0.0', args.port), Handler).serve_forever()


if __name__ == '__main__':
    main()
