"""Offset-preserving chunking. All heuristics below are part of profile v1."""
import hashlib
import json
import re
from uuid import UUID, uuid5

from ml.chunking.dto import DocumentChunk
from ml.chunking.profiles import ChunkingProfile, V1
from ml.chunking.tokenization import TokenCounter
from ml.ingestion.dto import SavedDocumentVersion
from ml.ingestion.quality import check_document

# Project-specific namespace; never change for published profiles.
CHUNK_NAMESPACE = UUID('d1d86b1a-c8e3-5bf1-9c36-37c481aa40be')
_NEWLINE = r'(?:\r\n|\r(?!\n)|(?<!\r)\n)'
_PARAGRAPH = re.compile(_NEWLINE + r'[ \t]*(?:' + _NEWLINE + r'[ \t]*)+')
_SENTENCE = re.compile(r'''[.!?…]+[»”"')\]]*(?:\s+|$)''')
_ABBREVIATIONS = frozenset(('г.', 'гг.', 'ул.', 'стр.', 'рис.', 'им.', 'т.д.', 'т.п.', 'mr.', 'dr.', 'e.g.', 'i.e.'))


class ChunkingError(ValueError):
    """No complete valid chunk set could be produced; nothing may be persisted."""

    code = 'CHUNKING_FAILED'


def _boundaries(text: str) -> tuple[set[int], set[int]]:
    paragraphs = {match.end() for match in _PARAGRAPH.finditer(text)} | {len(text)}
    sentences = set(paragraphs)
    for match in _SENTENCE.finditer(text):
        prefix = text[:match.start() + 1]
        word = prefix.rsplit(None, 1)[-1].lower() if prefix.strip() else ''
        # Small explicit abbreviation/initial policy, not linguistic inference.
        if word in _ABBREVIATIONS or re.fullmatch(r'[^\W\d_]\.', word, re.UNICODE):
            continue
        sentences.add(match.end())
    return paragraphs, sentences


def chunk_document(
    document: SavedDocumentVersion, token_counter: TokenCounter,
    profile: ChunkingProfile = V1,
) -> tuple[DocumentChunk, ...]:
    """Return an entire immutable set or raise. No storage, clock or model calls.

    TokenCounter is a trusted dependency whose identity/behavior must agree with
    the immutable profile. Custom profiles must receive distinct IDs.
    """
    if not isinstance(document, SavedDocumentVersion):
        raise TypeError('Expected SavedDocumentVersion')
    if not isinstance(profile, ChunkingProfile):
        raise TypeError('Expected ChunkingProfile')
    if token_counter.counter_id != profile.counter_id:
        raise ChunkingError('Tokenizer does not match the chunking profile')
    if check_document(document).status != 'PASS':
        raise ChunkingError('Document did not pass the quality gate')

    text = document.normalized_text
    paragraphs, sentences = _boundaries(text)
    ordered = sorted(sentences)

    def count(start: int, end: int) -> int:
        try:
            value = token_counter.count(text[start:end])
        except Exception:
            raise ChunkingError('Token counting failed') from None
        if type(value) is not int or value <= 0:
            raise ChunkingError('Tokenizer returned an invalid count')
        return value

    def choose_end(start: int, previous_end: int) -> int | None:
        candidates = []
        for end in ordered:
            if end <= previous_end or not text[start:end].strip():
                continue
            size = count(start, end)
            if size > profile.maximum:
                break
            candidates.append((end, size))
            if end == len(text):
                return end
        eligible = [(end, size) for end, size in candidates if size >= profile.minimum]
        preferred = [(end, size) for end, size in eligible if end in paragraphs]
        if preferred or eligible:
            return min(preferred or eligible, key=lambda pair: (abs(pair[1] - profile.target), pair[0]))[0]
        if candidates:
            return candidates[-1][0]
        return None

    def hard_end(start: int, previous_end: int) -> int:
        # Binary probing is a deterministic fallback, not a claim that token
        # counts are monotonic. Only measured, fitting ranges are returned.
        low, high, best = start + 1, len(text), None
        while low <= high:
            end = (low + high) // 2
            if count(start, end) <= profile.maximum:
                best = end
                low = end + 1
            else:
                high = end - 1
        if best is None or best <= previous_end:
            raise ChunkingError('Cannot fit new text within the chunk limit')
        # Prefer an actual whitespace boundary within the measured fitting span.
        for match in reversed(list(re.finditer(r'\s+', text[start:best]))):
            end = start + match.end()
            if end > previous_end and text[start:end].strip() and count(start, end) <= profile.maximum:
                return end
        if not text[start:best].strip():
            raise ChunkingError('Whitespace span cannot fit without an empty chunk')
        return best

    ranges: list[tuple[int, int]] = []
    start = previous_end = 0
    while previous_end < len(text):
        end = choose_end(start, previous_end)
        if end is None and start < previous_end:
            # Drop overlap before hard-splitting a sentence or long word.
            start = previous_end
            end = choose_end(start, previous_end)
        if end is None:
            end = hard_end(start, previous_end)
        ranges.append((start, end))
        previous_end = end
        if end == len(text):
            break
        start = end
        if profile.overlap:
            # Only complete sentence suffixes wholly contained in this chunk.
            for boundary in ordered:
                if ranges[-1][0] < boundary < end and text[boundary:end].strip():
                    if count(boundary, end) <= profile.overlap:
                        start = boundary
                        break

    # Merge a short final chunk if its union with its predecessor fits.
    if len(ranges) > 1 and count(*ranges[-1]) < profile.minimum:
        merged = (ranges[-2][0], ranges[-1][1])
        if count(*merged) <= profile.maximum:
            ranges[-2:] = [merged]

    chunks = []
    for number, (start, end) in enumerate(ranges):
        chunk_text = text[start:end]
        size = count(start, end)
        if size > profile.maximum:
            raise ChunkingError('Chunk exceeds the profile limit')
        key = json.dumps([
            document.document_version_id, profile.profile_id, start, end,
            hashlib.sha256(chunk_text.encode('utf-8')).hexdigest(),
        ], ensure_ascii=True, separators=(',', ':'))
        chunks.append(DocumentChunk(
            id=str(uuid5(CHUNK_NAMESPACE, key)),
            document_version_id=document.document_version_id,
            chunking_profile_id=profile.profile_id, chunk_no=number,
            chunk_text=chunk_text, char_start=start, char_end=end, token_count=size,
        ))
    return tuple(chunks)
