"""Pinned offline Qwen scorer shared by ML1 runtime and evaluation runners."""
from dataclasses import asdict, dataclass, replace
from importlib.metadata import version
import math


@dataclass(frozen=True)
class RerankerProfile:
    version: str = 'ml1-experiment-qwen3-reranker-06b-v1'
    model: str = 'Qwen/Qwen3-Reranker-0.6B'
    revision: str = 'e61197ed45024b0ed8a2d74b80b4d909f1255473'
    instruction: str = 'Given a web search query, retrieve relevant passages that answer the query'
    max_input_tokens: int = 8192
    batch_size: int = 4
    device: str = 'cpu'
    dtype: str = 'float32'
    attention: str = 'eager'
    score: str = 'logit(yes) - logit(no); not cosine or calibrated probability'
    tie_break: str = 'vector rank, then chunk ID'


PROFILE = RerankerProfile()
INFERENCE_PROFILES = {
    'cpu-eager-b4': PROFILE,
    'cpu-eager-b8': replace(PROFILE, version='ml1-exp-cpu-eager-b8-v1', batch_size=8),
    'cpu-sdpa-b4': replace(PROFILE, version='ml1-exp-cpu-sdpa-b4-v1', attention='sdpa'),
    'mps-sdpa-b4': replace(PROFILE, version='ml1-exp-mps-sdpa-b4-v1', device='mps', attention='sdpa'),
}
# Official Qwen Transformers ranking prompt, no domain/query-specific tuning.
PREFIX = ('<|im_start|>system\nJudge whether the Document meets the requirements based on the Query and the Instruct provided. '
          'Note that the answer can only be "yes" or "no".<|im_end|>\n<|im_start|>user\n')
SUFFIX = '<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n'


class RerankerError(ValueError):
    pass


def pair_text(query, text):
    if not isinstance(query, str) or not query.strip() or not isinstance(text, str) or not text.strip():
        raise RerankerError('Expected nonblank query and document text')
    return f'<Instruct>: {PROFILE.instruction}\n<Query>: {query}\n<Document>: {text}'


def keyed_scores(candidates, output):
    """Match by chunk ID, never rely on positional model output; no silent fallback."""
    ids = [c['chunk_id'] for c in candidates]
    output = tuple(output)
    if (len(set(ids)) != len(ids) or len(output) != len(ids)
            or {identity for identity, _ in output} != set(ids)
            or len({identity for identity, _ in output}) != len(output)):
        raise RerankerError('Reranker output IDs do not match candidates')
    if any(type(score) is not float or not math.isfinite(score) for _, score in output):
        raise RerankerError('Reranker scores must be finite floats')
    return dict(output)


class QwenReranker:
    profile = PROFILE

    def __init__(self, *, cache_dir=None, profile=PROFILE):
        if profile not in INFERENCE_PROFILES.values():
            raise RerankerError('Expected a registered experimental inference profile')
        self.profile = profile
        for package, expected in (('torch', '2.9.0'), ('transformers', '4.57.1'), ('tokenizers', '0.22.1')):
            if version(package).split('+', 1)[0] != expected:
                raise RerankerError(f'Expected {package}=={expected}')
        import torch
        from huggingface_hub import hf_hub_download
        from tokenizers import Tokenizer
        from transformers import AutoModelForCausalLM

        if profile.device == 'mps' and not torch.backends.mps.is_available():
            raise RerankerError('MPS accelerator is unavailable; no silent CPU fallback')
        path = hf_hub_download(profile.model, filename='tokenizer.json', revision=profile.revision,
                               cache_dir=cache_dir, local_files_only=True)
        self.tokenizer = Tokenizer.from_file(path)
        self.tokenizer.no_padding()
        self.tokenizer.no_truncation()
        self.pad_id = self.tokenizer.token_to_id('<|endoftext|>')
        self.yes_id, self.no_id = self.tokenizer.token_to_id('yes'), self.tokenizer.token_to_id('no')
        if None in (self.pad_id, self.yes_id, self.no_id) or self.yes_id == self.no_id:
            raise RerankerError('Missing ranking tokens')
        self.prefix = self.tokenizer.encode(PREFIX, add_special_tokens=False).ids
        self.suffix = self.tokenizer.encode(SUFFIX, add_special_tokens=False).ids
        self.model = AutoModelForCausalLM.from_pretrained(
            profile.model, revision=profile.revision, cache_dir=cache_dir, local_files_only=True,
            trust_remote_code=False, use_safetensors=True, torch_dtype=torch.float32,
            attn_implementation=profile.attention,
        ).to(profile.device).eval()

    def score(self, query, candidates):
        import torch

        candidates = tuple(candidates)
        if len({c['chunk_id'] for c in candidates}) != len(candidates):
            raise RerankerError('Duplicate candidate IDs')
        # Validate/tokenize the whole query before inference. Never truncate silently.
        encoded = self.tokenizer.encode_batch([pair_text(query, c['chunk_text']) for c in candidates],
                                              add_special_tokens=False) if candidates else []
        inputs = [self.prefix + item.ids + self.suffix for item in encoded]
        if any(len(ids) > self.profile.max_input_tokens for ids in inputs):
            raise RerankerError('Reranker token budget exceeded; truncation forbidden')
        output = []
        for offset in range(0, len(inputs), self.profile.batch_size):
            batch = inputs[offset:offset + self.profile.batch_size]
            width = max(map(len, batch))
            ids = [[self.pad_id] * (width - len(row)) + row for row in batch]
            masks = [[0] * (width - len(row)) + [1] * len(row) for row in batch]
            with torch.inference_mode():
                logits = self.model(input_ids=torch.tensor(ids, dtype=torch.long, device=self.profile.device),
                                    attention_mask=torch.tensor(masks, dtype=torch.long, device=self.profile.device),
                                    use_cache=False, logits_to_keep=1).logits[:, -1, :]
                scores = (logits[:, self.yes_id] - logits[:, self.no_id]).float().cpu().tolist()
            output.extend((c['chunk_id'], float(score))
                          for c, score in zip(candidates[offset:offset + len(batch)], scores, strict=True))
        keyed_scores(candidates, output)
        return tuple(output)

    def configuration(self):
        import torch
        return {**asdict(self.profile), 'torch_num_threads': torch.get_num_threads(),
                'prefix': PREFIX, 'suffix': SUFFIX, 'truncation': False, 'padding': 'left'}
