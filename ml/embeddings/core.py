"""Batch orchestration and validation; no heavyweight dependency or I/O."""
import hashlib
import math
import struct
from collections.abc import Sequence
from dataclasses import dataclass
from typing import Protocol
from uuid import UUID

from ml.chunking.dto import DocumentChunk
from ml.embeddings.profile import EmbeddingProfile, V1


class EmbeddingError(ValueError):
    code = 'EMBEDDING_FAILED'


def validate_vector(vector: Sequence[float]) -> None:
    if len(vector) != V1.dimension:
        raise EmbeddingError('Expected a 1024-dimensional embedding')
    if any(type(value) is not float or not math.isfinite(value) for value in vector):
        raise EmbeddingError('Embedding must contain finite floats')
    if not math.isclose(math.hypot(*vector), 1.0, rel_tol=0.0, abs_tol=1e-4):
        raise EmbeddingError('Embedding must be nonzero and L2-normalized')


def normalized_float32(vector: Sequence[float]) -> tuple[float, ...]:
    if len(vector) != V1.dimension:
        raise EmbeddingError('Expected a 1024-dimensional embedding')
    try:
        if any(isinstance(value, bool) for value in vector):
            raise ValueError
        values = tuple(float(value) for value in vector)
        if not all(math.isfinite(value) for value in values):
            raise ValueError
        norm = math.hypot(*values)
        if not math.isfinite(norm) or norm == 0:
            raise ValueError
        result = tuple(struct.unpack('!f', struct.pack('!f', value / norm))[0] for value in values)
    except (ValueError, TypeError, OverflowError):
        raise EmbeddingError('Embedding contains invalid values or has zero norm') from None
    validate_vector(result)
    return result


def text_hash(text: str) -> str:
    return hashlib.sha256(text.encode('utf-8')).hexdigest()


@dataclass(frozen=True)
class EmbeddingInput:
    key: str
    text: str


@dataclass(frozen=True)
class EncodedVector:
    key: str
    vector: Sequence[float]


@dataclass(frozen=True)
class ChunkEmbedding:
    chunk_id: str
    text_sha256: str
    vector: tuple[float, ...]
    embedding_model: str = V1.model
    embedding_version: str = V1.version

    def __post_init__(self):
        UUID(self.chunk_id)
        if (len(self.text_sha256) != 64
                or any(c not in '0123456789abcdef' for c in self.text_sha256)):
            raise EmbeddingError('Invalid source text hash')
        if not isinstance(self.vector, tuple):
            raise EmbeddingError('Expected an immutable vector tuple')
        validate_vector(self.vector)
        if (self.embedding_model, self.embedding_version) != (V1.model, V1.version):
            raise EmbeddingError('Embedding does not match the published profile')


class BatchEncoder(Protocol):
    profile: EmbeddingProfile

    def encode_batch(self, inputs: Sequence[EmbeddingInput]) -> Sequence[EncodedVector]: ...


class Embedder:
    def __init__(self, encoder: BatchEncoder, *, batch_size: int = 8):
        if type(batch_size) is not int or batch_size < 1:
            raise ValueError('Batch size must be a positive integer')
        if encoder.profile != V1:
            raise EmbeddingError('Encoder does not match the published profile')
        self.encoder = encoder
        self.batch_size = batch_size
        self.profile = V1

    def _encode(self, inputs: Sequence[EmbeddingInput]) -> tuple[tuple[float, ...], ...]:
        vectors = {}
        for offset in range(0, len(inputs), self.batch_size):
            batch = inputs[offset:offset + self.batch_size]
            expected = {item.key for item in batch}
            try:
                output = tuple(self.encoder.encode_batch(batch))
            except Exception:
                raise EmbeddingError('Embedding inference failed') from None
            if (any(not isinstance(item, EncodedVector) or not isinstance(item.key, str) for item in output)
                    or len(output) != len(batch) or {item.key for item in output} != expected
                    or len({item.key for item in output}) != len(output)):
                raise EmbeddingError('Encoder output keys do not match the input batch')
            for item in output:
                vectors[item.key] = normalized_float32(item.vector)
        return tuple(vectors[item.key] for item in inputs)

    def encode_chunks(self, chunks: Sequence[DocumentChunk]) -> tuple[ChunkEmbedding, ...]:
        chunks = tuple(chunks)
        if any(not isinstance(chunk, DocumentChunk) for chunk in chunks):
            raise TypeError('Expected DocumentChunk sequence')
        if len({UUID(chunk.id) for chunk in chunks}) != len(chunks):
            raise EmbeddingError('Duplicate chunk UUID')
        if any(chunk.chunking_profile_id != 'v1' for chunk in chunks):
            raise EmbeddingError('Embedding v1 requires chunking v1')
        vectors = self._encode(tuple(EmbeddingInput(chunk.id, chunk.chunk_text) for chunk in chunks))
        return tuple(ChunkEmbedding(chunk.id, text_hash(chunk.chunk_text), vector)
                     for chunk, vector in zip(chunks, vectors))

    def encode_queries(self, queries: Sequence[str]) -> tuple[tuple[float, ...], ...]:
        queries = tuple(queries)
        if any(not isinstance(query, str) or not query.strip() for query in queries):
            raise EmbeddingError('Queries must be nonblank strings')
        inputs = tuple(EmbeddingInput(f'query:{i}',
                                     f'Instruct: {V1.query_instruction}\nQuery:{query}')
                       for i, query in enumerate(queries))
        return self._encode(inputs)
