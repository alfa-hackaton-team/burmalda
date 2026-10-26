"""Persistent trusted version ordering and local indexing readiness; no new ACL."""
from uuid import UUID

from psycopg import sql
from psycopg.rows import tuple_row

from ml.adapters.postgres_chunks import ChunkPersistenceConflict, _check_connection
from ml.database import ML_SCHEMA
from ml.retrieval.interfaces import DocumentVersion


def remember_version(connection, version: DocumentVersion, *, schema=ML_SCHEMA):
    """Register pending metadata without demoting an already committed version."""
    _check_connection(connection, schema)
    if type(version.version_no) is not int or version.version_no < 1:
        raise ValueError('version_no must be a positive integer')
    table = sql.Identifier(schema, 'document_version_catalog')
    with connection.transaction():
        with connection.cursor(row_factory=tuple_row) as cursor:
            cursor.execute(sql.SQL(
                'INSERT INTO {} (document_version_id, document_id, version_no) VALUES (%s,%s,%s) '
                'ON CONFLICT DO NOTHING'
            ).format(table), (UUID(version.document_version_id), UUID(version.document_id), version.version_no))
            cursor.execute(sql.SQL('SELECT document_id, version_no FROM {} WHERE document_version_id=%s').format(table),
                           (UUID(version.document_version_id),))
            if cursor.fetchone() != (UUID(version.document_id), version.version_no):
                raise ChunkPersistenceConflict('Conflicting document version metadata')


def load_versions(connection, *, schema=ML_SCHEMA):
    _check_connection(connection, schema)
    with connection.transaction():
        with connection.cursor(row_factory=tuple_row) as cursor:
            cursor.execute(sql.SQL('SELECT document_id, document_version_id, version_no, ready FROM {} '
                                   'ORDER BY document_id, version_no').format(
                                       sql.Identifier(schema, 'document_version_catalog')))
            return tuple(DocumentVersion(str(d), str(v), n, ready) for d, v, n, ready in cursor.fetchall())
