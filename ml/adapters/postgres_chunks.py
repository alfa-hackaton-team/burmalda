"""Atomic PostgreSQL persistence of one complete, immutable chunk set.

No DDL, parent-document writes, model calls or pipeline state transitions.
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

from ml.chunking.dto import DocumentChunk


class ChunkPersistenceError(RuntimeError):
    """Storage failure; the entire operation was rolled back."""

    code = 'CHUNK_PERSISTENCE_FAILED'


class ChunkPersistenceConflict(ChunkPersistenceError):
    """Existing immutable rows differ from the supplied complete set."""

    code = 'CHUNK_PERSISTENCE_CONFLICT'


_COLUMNS = ('id', 'document_version_id', 'chunking_profile_id', 'chunk_no',
            'chunk_text', 'char_start', 'char_end', 'token_count')


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


def save_document_chunks(
    connection: Connection, chunks: Sequence[DocumentChunk], *, schema: str = 'public',
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
    if not isinstance(schema, str) or not schema.strip():
        raise ValueError('Expected a nonblank schema name')
    if connection.info.transaction_status != TransactionStatus.IDLE:
        raise ValueError('Persistence requires an idle connection')
    table = sql.Identifier(schema, 'document_chunks')
    columns = sql.SQL(', ').join(map(sql.Identifier, _COLUMNS))
    select = sql.SQL(
        'SELECT {} FROM {} WHERE document_version_id = %s '
        'AND chunking_profile_id = %s ORDER BY chunk_no FOR UPDATE'
    ).format(columns, table)
    insert = sql.SQL('INSERT INTO {} ({}) VALUES ({})').format(
        table, columns, sql.SQL(', ').join(sql.Placeholder() for _ in _COLUMNS),
    )
    version, profile = rows[0][1:3]
    # Stable signed bigint, independent of randomized Python hash(). Hash
    # collisions only serialize unrelated sets; they cannot mix their rows.
    lock_name = json.dumps([schema, 'document_chunks', str(version), profile], separators=(',', ':'))
    lock_key = int.from_bytes(hashlib.sha256(lock_name.encode('utf-8')).digest()[:8],
                              byteorder='big', signed=True)
    try:
        with connection.transaction():
            with connection.cursor(row_factory=tuple_row) as cursor:
                cursor.execute('SET TRANSACTION ISOLATION LEVEL READ COMMITTED')
                cursor.execute('SELECT pg_advisory_xact_lock(%s)', (lock_key,))
                cursor.execute(select, (version, profile))
                existing = tuple(cursor.fetchall())
                if existing:
                    if existing != rows:
                        raise ChunkPersistenceConflict('Existing chunk set differs from input')
                    return
                # No UPDATE/UPSERT: created_at, generated FTS and any existing
                # embeddings must remain untouched by retries.
                for row in rows:
                    cursor.execute(insert, row)
    except UniqueViolation:
        raise ChunkPersistenceConflict('Chunk UUID or position already exists') from None
    except DatabaseError:
        # Do not expose DSN, document contents or PostgreSQL diagnostic details.
        raise ChunkPersistenceError('PostgreSQL chunk persistence failed') from None
