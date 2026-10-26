"""Offline-first CPU reference inference. No import-time torch/model loading."""
from importlib.metadata import version

from ml.embeddings.core import EmbeddingError, EncodedVector
from ml.embeddings.profile import V1


class QwenBatchEncoder:
    profile = V1

    def __init__(self, *, cache_dir: str | None = None, local_files_only: bool = True):
        for package, expected in (('tokenizers', V1.tokenizer_version),
                                  ('transformers', V1.transformers_version), ('torch', V1.torch_version)):
            if version(package).split('+', 1)[0] != expected:
                raise EmbeddingError(f'Expected {package}=={expected}')
        import torch
        from huggingface_hub import hf_hub_download
        from tokenizers import Tokenizer
        from transformers import AutoModel

        path = hf_hub_download(V1.model, filename='tokenizer.json', revision=V1.revision,
                               cache_dir=cache_dir, local_files_only=local_files_only)
        self.tokenizer = Tokenizer.from_file(path)
        self.tokenizer.no_padding()
        self.tokenizer.no_truncation()
        self.pad_id = self.tokenizer.token_to_id('<|endoftext|>')
        if self.pad_id is None:
            raise EmbeddingError('Pinned tokenizer has no expected padding token')
        self.model = AutoModel.from_pretrained(
            V1.model, revision=V1.revision, cache_dir=cache_dir, local_files_only=local_files_only,
            trust_remote_code=False, use_safetensors=True, torch_dtype=torch.float32,
            attn_implementation='eager',
        ).to('cpu').eval()
        if self.model.config.hidden_size != V1.dimension:
            raise EmbeddingError('Model hidden size does not match the profile')

    def encode_batch(self, inputs):
        import torch

        if not inputs:
            return ()
        encodings = self.tokenizer.encode_batch([item.text for item in inputs], add_special_tokens=False)
        lengths = [len(encoding.ids) for encoding in encodings]
        if any(length < 1 or length > V1.max_input_tokens for length in lengths):
            raise EmbeddingError('Input token budget exceeded; truncation is forbidden')
        width = max(lengths)
        ids = [[self.pad_id] * (width - length) + encoding.ids
               for encoding, length in zip(encodings, lengths)]
        masks = [[0] * (width - length) + [1] * length for length in lengths]
        with torch.inference_mode():
            states = self.model(input_ids=torch.tensor(ids, dtype=torch.long),
                                attention_mask=torch.tensor(masks, dtype=torch.long)).last_hidden_state
            # Left padding ensures the final position is the last real token.
            vectors = states[:, -1, :].float().cpu().tolist()
        return tuple(EncodedVector(item.key, vector) for item, vector in zip(inputs, vectors))
