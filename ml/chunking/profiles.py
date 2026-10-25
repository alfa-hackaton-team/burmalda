"""Published profiles are immutable: behavioral changes require a new ID."""
from dataclasses import dataclass

QWEN_REPOSITORY = 'Qwen/Qwen3-Embedding-0.6B'
QWEN_REVISION = '97b0c614be4d77ee51c0cef4e5f07c00f9eb65b3'
TOKENIZERS_VERSION = '0.22.1'
QWEN_COUNTER_ID = f'{QWEN_REPOSITORY}@{QWEN_REVISION}:tokenizers-{TOKENIZERS_VERSION}:no-special-v1'


@dataclass(frozen=True)
class ChunkingProfile:
    profile_id: str
    counter_id: str
    target: int = 512
    minimum: int = 128
    maximum: int = 768
    overlap: int = 64

    def __post_init__(self):
        if not self.profile_id.strip() or not self.counter_id.strip():
            raise ValueError('Profile and counter IDs must be nonblank')
        values = (self.minimum, self.target, self.maximum, self.overlap)
        if any(type(value) is not int for value in values):
            raise ValueError('Chunk sizes must be integers')
        if not 0 < self.minimum <= self.target <= self.maximum or not 0 <= self.overlap < self.minimum:
            raise ValueError('Expected 0 <= overlap < minimum <= target <= maximum')
        if self.profile_id == 'v1' and (
            self.counter_id != QWEN_COUNTER_ID or values != (128, 512, 768, 64)
        ):
            raise ValueError('Published v1 profile cannot be reconfigured')


V1 = ChunkingProfile('v1', QWEN_COUNTER_ID)
