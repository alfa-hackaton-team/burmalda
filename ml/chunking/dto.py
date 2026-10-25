"""Internal chunk rows; deliberately separate from public retrieval Chunk v1."""
from dataclasses import asdict, dataclass
from uuid import UUID


@dataclass(frozen=True)
class DocumentChunk:
    id: str
    document_version_id: str
    chunking_profile_id: str
    chunk_no: int
    chunk_text: str
    char_start: int
    char_end: int
    token_count: int

    def __post_init__(self):
        UUID(self.id)
        UUID(self.document_version_id)
        if not self.chunking_profile_id.strip() or not self.chunk_text.strip():
            raise ValueError('Profile and chunk text must be nonblank')
        if any(type(value) is not int for value in (
            self.chunk_no, self.char_start, self.char_end, self.token_count,
        )):
            raise ValueError('Chunk positions and counts must be integers')
        if (self.chunk_no < 0 or self.char_start < 0 or self.char_end <= self.char_start
                or self.char_end - self.char_start != len(self.chunk_text) or self.token_count <= 0):
            raise ValueError('Invalid chunk range, position or token count')

    def to_dict(self) -> dict:
        return asdict(self)
