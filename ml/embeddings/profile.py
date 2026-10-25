"""Published embedding profile: behavior changes require a new version."""
from dataclasses import dataclass, field

from ml.chunking.profiles import QWEN_REPOSITORY, QWEN_REVISION, TOKENIZERS_VERSION


@dataclass(frozen=True)
class EmbeddingProfile:
    version: str = field(default='ml1-qwen3-embedding-1024-v1', init=False)
    model: str = field(default=QWEN_REPOSITORY, init=False)
    revision: str = field(default=QWEN_REVISION, init=False)
    dimension: int = field(default=1024, init=False)
    tokenizer_version: str = field(default=TOKENIZERS_VERSION, init=False)
    transformers_version: str = field(default='4.57.1', init=False)
    torch_version: str = field(default='2.9.0', init=False)
    max_input_tokens: int = field(default=8192, init=False)
    query_instruction: str = field(
        default='Given a web search query, retrieve relevant passages that answer the query', init=False,
    )
    # Other profile invariants: CPU float32, eager attention, eval/inference
    # mode, no special tokens/truncation, left padding, last-token pooling,
    # L2 normalization and float32 output. Documents contain body only.


V1 = EmbeddingProfile()
VECTOR_COMPARE_ATOL = 1e-6  # float32 storage / CPU batching roundoff, never an overwrite
