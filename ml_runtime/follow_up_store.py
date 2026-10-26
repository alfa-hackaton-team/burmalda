"""Durable synchronous follow-up identity; never retries unknown model execution."""
import copy
import hashlib
import json
import os

from .analysis_validation import validate_contract_message
from .service import _failed


class FollowUpConflict(ValueError):
    pass


def fingerprint(request):
    material = {k: v for k, v in request.items() if k not in ('trace_id', 'parent_trace_id')}
    return hashlib.sha256(json.dumps(material, sort_keys=True, ensure_ascii=False,
                                     separators=(',', ':')).encode()).digest()


class PostgresFollowUpStore:
    def __init__(self, dsn=None):
        self.dsn = dsn if dsn is not None else os.environ.get('ML_DATABASE_URL', '')
        if not self.dsn:
            raise ValueError('ML_DATABASE_URL must be configured')

    def execute(self, key, request, analyze):
        """Commit reservation before analyze. Synchronous duplicates wait for it.

        The session lock spans inference, but no DB transaction does. A dead
        session releases its lock; its RUNNING row is an unknown outcome, not a
        license to rerun paid inference. Terminal failures are also replayed.
        """
        import psycopg
        digest = fingerprint(request)
        with psycopg.connect(self.dsn, autocommit=True, connect_timeout=5,
                             options='-c statement_timeout=120000') as conn:
            # Close releases this session lock even if inference/DB write crashes.
            conn.execute('SELECT pg_advisory_lock(hashtextextended(%s, 20261026))', (key,))
            row = conn.execute(
                'SELECT fingerprint,state,response FROM ml.follow_up_operations WHERE idempotency_key=%s',
                (key,)).fetchone()
            if row is not None:
                if bytes(row[0]) != digest:
                    raise FollowUpConflict('Idempotency-Key has different parameters')
                if row[2] is not None:
                    response = copy.deepcopy(row[2])
                    response['trace_id'] = request['trace_id']
                    return response
                response = _failed(request, 'PERSISTENCE_UNAVAILABLE',
                    'Previous execution has no durable result; model execution outcome is unknown. '
                    'Automatic re-execution is prohibited.', False)
            else:
                conn.execute(
                    "INSERT INTO ml.follow_up_operations(operation_id,idempotency_key,fingerprint,"
                    "request_snapshot,state) VALUES(%s,%s,%s,%s::jsonb,'RUNNING')",
                    (request['request_operation_id'], key, digest, json.dumps(request)))
                try:
                    response = analyze(request)
                    validate_contract_message(response, 'AnalysisResponse')
                    if any(response[k] != request[k] for k in (
                            'contract_version', 'trace_id', 'request_operation_id')):
                        raise ValueError('Analysis response identity changed')
                except Exception:
                    response = _failed(request, 'MODEL_UNAVAILABLE',
                                       'Follow-up execution failed; no automatic retry.', False)
            validate_contract_message(response, 'AnalysisResponse')
            state = {'succeeded': 'SUCCEEDED', 'failed': 'FAILED',
                     'insufficient_evidence': 'INSUFFICIENT_EVIDENCE'}[response['status']]
            conn.execute(
                'UPDATE ml.follow_up_operations SET state=%s,response=%s::jsonb,error=%s::jsonb,'
                'completed_at=now() WHERE idempotency_key=%s AND state=\'RUNNING\'',
                (state, json.dumps(response),
                 json.dumps(response['error']) if response['error'] is not None else None, key))
            return response
