"""Atomic PostgreSQL persistence of one complete, immutable chunk set.

No DDL, model calls or pipeline state transitions. ML snapshot writes are local.
All writers of a version/profile set must use this locking protocol.
"""
import hashlib
import json
from collections.abc import Sequence
from uuid import UUID

from psycopg import Connection, DatabaseError, sql
from psycopg.errors import UniqueViolation
from psycopg.pq import TransactionStatus
from psycopg.rows import tuple_row
from psycopg.types.json import Jsonb

from ml.chunking.dto import DocumentChunk
from ml.database import ML_SCHEMA
from ml.ingestion.dto import SavedDocumentVersion
from ml.ingestion.quality import check_document


class ChunkPersistenceError(RuntimeError):
    """Storage failure; the entire operation was rolled back."""

    code = 'CHUNK_PERSISTENCE_FAILED'


class ChunkPersistenceConflict(ChunkPersistenceError):
    """Existing immutable rows differ from the supplied complete set."""

    code = 'CHUNK_PERSISTENCE_CONFLICT'


_COLUMNS = ('id', 'document_version_id', 'chunking_profile_id', 'chunk_no',
            'chunk_text', 'char_start', 'char_end', 'token_count')


def _lock_key(schema, version, profile):
    lock_name = json.dumps([schema, 'document_chunks', str(version), profile], separators=(',', ':'))
    return int.from_bytes(hashlib.sha256(lock_name.encode('utf-8')).digest()[:8],
                          byteorder='big', signed=True)


def _rows(chunks: Sequence[DocumentChunk]) -> tuple[tuple, ...]:
    chunks = tuple(chunks)
    if not chunks or any(not isinstance(chunk, DocumentChunk) for chunk in chunks):
        raise ValueError('Expected a nonempty complete DocumentChunk sequence')
    version, profile = chunks[0].document_version_id, chunks[0].chunking_profile_id
    if any((chunk.document_version_id, chunk.chunking_profile_id) != (version, profile)
           for chunk in chunks):
        raise ValueError('Expected one document version and chunking profile')
    if len({UUID(chunk.id) for chunk in chunks}) != len(chunks):
        raise ValueError('Duplicate chunk UUID')
    for number, chunk in enumerate(chunks):
        if chunk.chunk_no != number:
            raise ValueError('Chunks must be ordered and numbered from zero')
        if number == 0 and chunk.char_start != 0:
            raise ValueError('Complete chunk set must start at zero')
        if number:
            previous = chunks[number - 1]
            if not (previous.char_start < chunk.char_start <= previous.char_end < chunk.char_end):
                raise ValueError('Chunk ranges must advance without gaps')
            overlap = previous.char_end - chunk.char_start
            if overlap and previous.chunk_text[-overlap:] != chunk.chunk_text[:overlap]:
                raise ValueError('Overlapping chunk text does not match')
    return tuple((UUID(c.id), UUID(c.document_version_id), c.chunking_profile_id,
                  c.chunk_no, c.chunk_text, c.char_start, c.char_end, c.token_count)
                 for c in chunks)


def _write_chunks(cursor, rows, schema):
    """Shared transaction body; caller owns commit/rollback."""
    table = sql.Identifier(schema, 'document_chunks')
    columns = sql.SQL(', ').join(map(sql.Identifier, _COLUMNS))
    version, profile = rows[0][1:3]
    cursor.execute('SELECT pg_advisory_xact_lock(%s)', (_lock_key(schema, version, profile),))
    cursor.execute(sql.SQL(
        'SELECT {} FROM {} WHERE document_version_id = %s '
        'AND chunking_profile_id = %s ORDER BY chunk_no FOR UPDATE'
    ).format(columns, table), (version, profile))
    existing = tuple(cursor.fetchall())
    if existing:
        if existing != rows:
            raise ChunkPersistenceConflict('Existing chunk set differs from input')
        return
    insert = sql.SQL('INSERT INTO {} ({}) VALUES ({})').format(
        table, columns, sql.SQL(', ').join(sql.Placeholder() for _ in _COLUMNS),
    )
    for row in rows:
        cursor.execute(insert, row)


def _check_connection(connection, schema):
    if not isinstance(schema, str) or not schema.strip():
        raise ValueError('Expected a nonblank schema name')
    if connection.info.transaction_status != TransactionStatus.IDLE:
        raise ValueError('Persistence requires an idle connection')


def save_document_chunks(
    connection: Connection, chunks: Sequence[DocumentChunk], *, schema: str = ML_SCHEMA,
) -> None:
    """Commit an entire set or raise; an identical retry is a read-only no-op.

    Caller supplies an open, idle synchronous psycopg3 connection (either
    autocommit setting). This function owns the transaction, never closes the
    connection and refuses to commit/rollback an existing caller transaction.
    Uses READ COMMITTED so a waiter sees the preceding writer after its lock.

    Parent version/provenance must already exist according to the deployed FK.
    Completeness against the parent's body is guaranteed by the chunker/caller;
    this boundary checks ordering, range continuity and matching overlap only.
    Existing partial/superset sets are conflicts, not silently repaired.
    """
    rows = _rows(chunks)
    _check_connection(connection, schema)
    try:
        with connection.transaction():
            with connection.cursor(row_factory=tuple_row) as cursor:
                cursor.execute('SET TRANSACTION ISOLATION LEVEL READ COMMITTED')
                _write_chunks(cursor, rows, schema)
    except UniqueViolation:
        raise ChunkPersistenceConflict('Chunk UUID or position already exists') from None
    except DatabaseError:
        # Do not expose DSN, document contents or PostgreSQL diagnostic details.
        raise ChunkPersistenceError('PostgreSQL chunk persistence failed') from None


def _write_document(cursor, table, lock_key, version, document_id, snapshot, rows, schema):
    # Lock version before profile: different profiles must agree on
    # the same parent snapshot. Runtime role has no UPDATE on it.
    cursor.execute('SELECT pg_advisory_xact_lock(%s)', (lock_key,))
    cursor.execute(sql.SQL('SELECT document_id, snapshot FROM {} WHERE id = %s').format(table), (version,))
    existing = cursor.fetchone()
    if existing is None:
        cursor.execute(sql.SQL('INSERT INTO {} (id, document_id, snapshot) VALUES (%s, %s, %s)').format(table),
                       (version, document_id, Jsonb(snapshot)))
    elif existing != (document_id, snapshot):
        raise ChunkPersistenceConflict('Existing document snapshot differs from input')
    _write_chunks(cursor, rows, schema)


def _prepare_document(document, chunks, schema):
    if not isinstance(document, SavedDocumentVersion):
        raise TypeError('Expected SavedDocumentVersion')
    chunks = tuple(chunks)
    rows = _rows(chunks)
    if check_document(document).status != 'PASS':
        raise ValueError('Only quality PASS documents may be persisted with chunks')
    version, document_id = UUID(document.document_version_id), UUID(document.document_id)
    if rows[0][1] != version or chunks[-1].char_end != len(document.normalized_text):
        raise ValueError('Chunk set must cover the supplied document version completely')
    if any(chunk.chunk_text != document.normalized_text[chunk.char_start:chunk.char_end] for chunk in chunks):
        raise ValueError('Chunk text differs from the exact document slice')
    snapshot = document.to_dict()
    snapshot.pop('trace_id')
    snapshot.pop('pipeline_version')
    table = sql.Identifier(schema, 'document_versions')
    lock_name = json.dumps([schema, 'document_versions', str(version)], separators=(',', ':'))
    lock_key = int.from_bytes(hashlib.sha256(lock_name.encode()).digest()[:8], 'big', signed=True)
    return table, lock_key, version, document_id, snapshot, rows


def save_document_version_and_chunks(connection: Connection, document: SavedDocumentVersion,
                                     chunks: Sequence[DocumentChunk], *, schema: str = ML_SCHEMA) -> None:
    """Commit a local immutable snapshot and its full chunk set atomically.

    Snapshot identity excludes execution context (trace_id/pipeline_version).
    All other canonical fields/provenance are stored exactly in JSONB, including
    original timestamp strings and nullable/empty title. No Go table is queried.
    """
    table, lock_key, version, document_id, snapshot, rows = _prepare_document(document, chunks, schema)
    _check_connection(connection, schema)
    try:
        with connection.transaction():
            with connection.cursor(row_factory=tuple_row) as cursor:
                cursor.execute('SET TRANSACTION ISOLATION LEVEL READ COMMITTED')
                _write_document(cursor, table, lock_key, version, document_id, snapshot, rows, schema)
    except UniqueViolation:
        raise ChunkPersistenceConflict('Snapshot or chunk identity already exists') from None
    except DatabaseError:
        raise ChunkPersistenceError('PostgreSQL document and chunk persistence failed') from None


def _write_embeddings(cursor, select, update, version, profile, rows, by_id, schema):
    import math
    from ml.embeddings.core import validate_vector
    from ml.embeddings.profile import VECTOR_COMPARE_ATOL

    cursor.execute('SELECT pg_advisory_xact_lock(%s)', (_lock_key(schema, version, profile),))
    cursor.execute(select, (version, profile))
    existing = tuple(cursor.fetchall())
    if tuple(row[:8] for row in existing) != rows:
        raise ChunkPersistenceConflict('Stored chunk set differs from embedding input')
    pending = []
    for row in existing:
        item = by_id[row[0]]
        vector, model, embedding_version = row[8:]
        if vector is None and model is None and embedding_version is None:
            pending.append(item)
            continue
        if (model, embedding_version) != (item.embedding_model, item.embedding_version):
            raise ChunkPersistenceConflict('Existing embedding profile differs from input')
        try:
            stored = tuple(float(value) for value in json.loads(vector))
            validate_vector(stored)
        except (ValueError, TypeError):
            raise ChunkPersistenceConflict('Stored embedding is invalid') from None
        if any(not math.isclose(a, b, rel_tol=0.0, abs_tol=VECTOR_COMPARE_ATOL)
               for a, b in zip(stored, item.vector)):
            raise ChunkPersistenceConflict('Existing embedding differs from input')
    for item in pending:
        vector_literal = '[' + ','.join(repr(value) for value in item.vector) + ']'
        cursor.execute(update, (vector_literal, item.embedding_model,
                                item.embedding_version, UUID(item.chunk_id)))


def _prepare_embeddings(chunks, embeddings, schema):
    from ml.embeddings.core import ChunkEmbedding, text_hash, validate_vector

    chunks = tuple(chunks)
    rows = _rows(chunks)
    if any(chunk.chunking_profile_id != 'v1' for chunk in chunks):
        raise ValueError('Embedding v1 requires chunking v1')
    embeddings = tuple(embeddings)
    if any(not isinstance(item, ChunkEmbedding) for item in embeddings):
        raise ValueError('Expected ChunkEmbedding sequence')
    by_id = {UUID(item.chunk_id): item for item in embeddings}
    if len(by_id) != len(embeddings) or set(by_id) != {row[0] for row in rows}:
        raise ValueError('Embedding IDs must match the complete chunk set exactly')
    for chunk in chunks:
        item = by_id[UUID(chunk.id)]
        if item.text_sha256 != text_hash(chunk.chunk_text):
            raise ValueError('Embedding source text hash differs from chunk text')
        validate_vector(item.vector)
    table = sql.Identifier(schema, 'document_chunks')
    columns = sql.SQL(', ').join(map(sql.Identifier, _COLUMNS))
    select = sql.SQL(
        'SELECT {}, embedding::text, embedding_model, embedding_version FROM {} '
        'WHERE document_version_id = %s AND chunking_profile_id = %s ORDER BY chunk_no FOR UPDATE'
    ).format(columns, table)
    update = sql.SQL(
        'UPDATE {} SET embedding = %s, embedding_model = %s, embedding_version = %s WHERE id = %s'
    ).format(table)
    version, profile = rows[0][1:3]
    return select, update, version, profile, rows, by_id


def save_document_embeddings(connection: Connection, chunks: Sequence[DocumentChunk],
                             embeddings, *, schema: str = ML_SCHEMA) -> None:
    """Atomically attach validated v1 embeddings to an already persisted full set.

    Exact input rows and text hashes bind every vector to its source chunk.
    Existing identical-profile vectors within float32 roundoff are no-ops;
    different profiles/vectors are conflicts, never silently overwritten.
    Requires PostgreSQL embedding VECTOR(1024); no pgvector Python adapter.
    """
    select, update, version, profile, rows, by_id = _prepare_embeddings(chunks, embeddings, schema)
    _check_connection(connection, schema)
    try:
        with connection.transaction():
            with connection.cursor(row_factory=tuple_row) as cursor:
                cursor.execute('SET TRANSACTION ISOLATION LEVEL READ COMMITTED')
                _write_embeddings(cursor, select, update, version, profile, rows, by_id, schema)
    except DatabaseError:
        raise ChunkPersistenceError('PostgreSQL embedding persistence failed') from None


def save_indexed_document(connection: Connection, document: SavedDocumentVersion, chunks, embeddings,
                          *, schema: str = ML_SCHEMA, version=None) -> None:
    """One commit for snapshot, complete chunk set and complete embeddings.

    Inference happens before this call. Reuses the same locks, validation and
    immutable retry/conflict semantics as the separate persistence entry points.
    """
    chunks, embeddings = tuple(chunks), tuple(embeddings)
    doc = _prepare_document(document, chunks, schema)
    emb = _prepare_embeddings(chunks, embeddings, schema)
    _check_connection(connection, schema)
    if version is not None and (version.document_id, version.document_version_id) != (
            document.document_id, document.document_version_id):
        raise ValueError('Version metadata differs from the indexed document')
    try:
        with connection.transaction():
            with connection.cursor(row_factory=tuple_row) as cursor:
                cursor.execute('SET TRANSACTION ISOLATION LEVEL READ COMMITTED')
                _write_document(cursor, *doc, schema)
                _write_embeddings(cursor, *emb, schema)
                if version is not None:
                    cursor.execute(sql.SQL(
                        'UPDATE {} SET ready=true, indexed_at=coalesce(indexed_at, now()) '
                        'WHERE document_version_id=%s AND document_id=%s AND version_no=%s'
                    ).format(sql.Identifier(schema, 'document_version_catalog')),
                        (UUID(version.document_version_id), UUID(version.document_id), version.version_no))
                    if cursor.rowcount != 1:
                        raise ChunkPersistenceConflict('Version catalog metadata is absent or conflicting')
    except UniqueViolation:
        raise ChunkPersistenceConflict('Snapshot or chunk identity already exists') from None
    except DatabaseError:
        raise ChunkPersistenceError('PostgreSQL indexing failed; transaction rolled back') from None
