"""Explicit offline-first tokenizer loading; no model weights or import-time I/O."""
from importlib.metadata import version
from typing import Protocol

from ml.chunking.profiles import (
    QWEN_COUNTER_ID, QWEN_REPOSITORY, QWEN_REVISION, TOKENIZERS_VERSION,
)


class TokenCounter(Protocol):
    counter_id: str

    def count(self, text: str) -> int: ...


class QwenTokenCounter:
    counter_id = QWEN_COUNTER_ID

    def __init__(self, *, cache_dir: str | None = None, local_files_only: bool = True):
        if version('tokenizers') != TOKENIZERS_VERSION:
            raise ValueError(f'Expected tokenizers=={TOKENIZERS_VERSION}')
        from huggingface_hub import hf_hub_download
        from tokenizers import Tokenizer

        path = hf_hub_download(
            repo_id=QWEN_REPOSITORY, filename='tokenizer.json', revision=QWEN_REVISION,
            cache_dir=cache_dir, local_files_only=local_files_only,
        )
        self._tokenizer = Tokenizer.from_file(path)
        self._tokenizer.no_truncation()
        self._tokenizer.no_padding()

    def count(self, text: str) -> int:
        return len(self._tokenizer.encode(text, add_special_tokens=False).ids)
