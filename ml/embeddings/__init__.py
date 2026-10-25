"""Qwen embedding stage; importing this package never loads model weights."""
from ml.embeddings.core import ChunkEmbedding, Embedder, EmbeddingError
from ml.embeddings.profile import V1

__all__ = ['ChunkEmbedding', 'Embedder', 'EmbeddingError', 'V1']
