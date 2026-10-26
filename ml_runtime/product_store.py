"""Durable product operations shared by the HTTP process and analysis worker."""
from __future__ import annotations

import hashlib
import json
import os
from contextlib import contextmanager
from uuid import UUID, uuid4

import psycopg
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb


def fingerprint(request):
    # Transport retries may have a new trace, but computation must stay the same.
    value = {k: v for k, v in request.items() if k not in ('trace_id', 'parent_trace_id')}
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=True,
                                     separators=(',', ':')).encode()).digest()


class OperationConflict(ValueError):
    pass


def operation_wire(row):
    result = {'analysis_id': str(row['analysis_id']), 'state': row['state'],
              'trace_id': row['request']['trace_id'], 'created_at': row['created_at'].isoformat()}
    if row['response'] is not None:
        result['response'] = row['response']
    return result


class ProductStore:
    def __init__(self, dsn=None):
        self.dsn = dsn or os.environ.get('ML_DATABASE_URL', '')
        if not self.dsn:
            raise ValueError('ML_DATABASE_URL is required')

    @contextmanager
    def connect(self):
        with psycopg.connect(self.dsn, row_factory=dict_row, connect_timeout=5) as connection:
            yield connection

    def submit(self, request):
        identity = UUID(request['request_operation_id'])
        digest = fingerprint(request)
        with self.connect() as connection:
            connection.execute('''INSERT INTO ml.analysis_operations
                (analysis_id, request_operation_id, fingerprint, request)
                VALUES (%s,%s,%s,%s) ON CONFLICT (request_operation_id) DO NOTHING''',
                (uuid4(), identity, digest, Jsonb(request)))
            row = connection.execute('SELECT * FROM ml.analysis_operations WHERE request_operation_id=%s',
                                     (identity,)).fetchone()
            if bytes(row['fingerprint']) != digest:
                raise OperationConflict('Idempotency key is already bound to another request')
            return operation_wire(row)

    def get(self, analysis_id):
        try:
            identity = UUID(analysis_id)
        except ValueError:
            return None
        with self.connect() as connection:
            row = connection.execute('SELECT * FROM ml.analysis_operations WHERE analysis_id=%s',
                                     (identity,)).fetchone()
            return operation_wire(row) if row else None

    def claim(self):
        with self.connect() as connection:
            return connection.execute('''UPDATE ml.analysis_operations SET state='RUNNING', started_at=now()
                WHERE analysis_id=(SELECT analysis_id FROM ml.analysis_operations WHERE state='QUEUED'
                    ORDER BY created_at, analysis_id FOR UPDATE SKIP LOCKED LIMIT 1)
                RETURNING *''').fetchone()

    def finish(self, analysis_id, response):
        state = {'succeeded': 'SUCCEEDED', 'insufficient_evidence': 'INSUFFICIENT_EVIDENCE',
                 'failed': 'FAILED'}[response['status']]
        with self.connect() as connection:
            connection.execute('''UPDATE ml.analysis_operations SET state=%s, response=%s, finished_at=now()
                WHERE analysis_id=%s AND state='RUNNING' ''', (state, Jsonb(response), analysis_id))

    def expire_running(self):
        # Never automatically replay a paid call whose outcome was lost on crash.
        from .service import _failed
        with self.connect() as connection:
            rows = connection.execute('''SELECT * FROM ml.analysis_operations WHERE state='RUNNING'
                AND started_at < now() - interval '5 minutes' FOR UPDATE SKIP LOCKED''').fetchall()
            for row in rows:
                response = _failed(row['request'], 'TIMEOUT', 'Analysis worker did not complete.', False)
                response['analysis_run_id'] = str(row['analysis_id'])
                connection.execute('''UPDATE ml.analysis_operations SET state='FAILED', response=%s,
                    finished_at=now() WHERE analysis_id=%s''', (Jsonb(response), row['analysis_id']))

    def save_ranking(self, digest, items):
        identity = str(uuid4())
        with self.connect() as connection:
            connection.execute('DELETE FROM ml.ranking_snapshots WHERE expires_at <= now()')
            connection.execute('INSERT INTO ml.ranking_snapshots(id,fingerprint,items) VALUES(%s,%s,%s)',
                               (identity, digest, Jsonb(items)))
        return identity

    def get_ranking(self, identity, digest):
        with self.connect() as connection:
            row = connection.execute('''SELECT items FROM ml.ranking_snapshots
                WHERE id=%s AND fingerprint=%s AND expires_at>now()''', (identity, digest)).fetchone()
            return row['items'] if row else None
