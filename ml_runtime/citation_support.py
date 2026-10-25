"""Bounded exact-span attribution guard, run before semantic verification.

Checks explicit source names and named forecast/assessment attributions. This is
not an entailment checker: numbers, negation and the full claim still require
the mandatory production verifier. No claim, source text or metadata is edited.
"""
import re

from .verification import OutputVerificationError


_SOURCE_ALIASES = (
    r'\b(?:Банк(?:а|у|ом|е)?\s+России|ЦБ(?:\s+РФ)?)\b',
    r'\bАльфа[\s-]Банк(?:а|у|ом|е)?\b',
)
_ATTRIBUTION = re.compile(
    r'(?:прогноз[а-яё]*|оценк[а-яё]*|сценари[а-яё]*)\s+'
    r'([А-ЯЁA-Z][\w-]+(?:\s+[А-ЯЁA-Z][\w-]+){0,3})')
_BACK_REFERENCE = re.compile(
    r'\b(?:это|этот|этого|этому|мы|наш[а-яё]*|он[а-яё]*)\b', re.IGNORECASE)
_FORWARD_REFERENCE = re.compile(
    r'^\s*(?:Таков[а-яё]*\s+(?:прогноз|оценка)|Это\s+(?:прогноз|оценка)|'
    r'(?:Этот\s+прогноз|Эта\s+оценка))\b', re.IGNORECASE)


def _attributions(claim_text, chunks):
    patterns = [re.compile(p, re.IGNORECASE) for p in _SOURCE_ALIASES]
    patterns += [re.compile(re.escape(c['source_name']), re.IGNORECASE)
                 for c in chunks if c.get('source_name') and
                 not any(re.fullmatch(p, c['source_name'], re.IGNORECASE)
                         for p in _SOURCE_ALIASES)]
    patterns += [re.compile(re.escape(m.group(1)), re.IGNORECASE)
                 for m in _ATTRIBUTION.finditer(claim_text)
                 if not any(re.fullmatch(p, m.group(1), re.IGNORECASE)
                            for p in _SOURCE_ALIASES)]
    return [p for p in patterns if p.search(claim_text)]


def _expand(text, quote, names):
    if not quote or quote not in text:
        raise OutputVerificationError('Support quote must be an exact source span')
    if all(p.search(quote) for p in names):
        return quote
    if any(re.search(p, quote, re.IGNORECASE) and
           not any(name.pattern == p for name in names) for p in _SOURCE_ALIASES):
        raise OutputVerificationError('Support names a different source; cannot expand attribution')
    if text.count(quote) != 1:
        raise OutputVerificationError('Attribution expansion has an ambiguous source span')
    start = text.index(quote)
    end = start + len(quote)
    # Stay within one paragraph. Sentence boundaries leave decimals/abbreviations
    # intact; all results are slices of the original text, including whitespace.
    previous_break = text.rfind('\n\n', 0, start)
    para_start = previous_break + 2 if previous_break >= 0 else 0
    para_end = text.find('\n\n', end)
    if para_end < 0:
        para_end = len(text)
    boundaries = [para_start] + [para_start + m.end() for m in re.finditer(
        r'[.!?](?=\s+[А-ЯЁA-Z])', text[para_start:para_end])] + [para_end]
    sentences = list(zip(boundaries, boundaries[1:]))
    candidates = []
    for index, (left, right) in enumerate(sentences):
        if not (left <= start < right and end <= right):
            continue
        # Attribution earlier/later in the same sentence is directly connected.
        candidates.append((left, max(end, right)))
        sentence = text[left:right]
        if index and _BACK_REFERENCE.search(sentence):
            candidates.append((sentences[index - 1][0], end))
        if index + 1 < len(sentences):
            next_start, next_end = sentences[index + 1]
            if _FORWARD_REFERENCE.search(text[next_start:next_end]):
                candidates.append((start, next_end))
        break
    sufficient = [text[left:right].strip() for left, right in candidates
                  if all(p.search(text[left:right]) for p in names)]
    if not sufficient:
        raise OutputVerificationError('No directly linked exact support for claim attribution')
    return min(sufficient, key=len)


def ensure_attributed_support(response, chunks):
    """Expand only existing exact support, or reject before the LLM verifier.

Requires each document support to retain the claim's explicit attribution.
Separate unlinked sentences/chunks and metadata-only authors cannot supply it.
Named source aliases cover grammatical cases of Bank of Russia and Alfa-Bank;
other names are literal trusted source names or explicit attribution phrases.
"""
    if response['status'] != 'succeeded':
        return
    result = response['insight'] or response['message']
    by_id = {c['chunk_id']: c for c in chunks}
    for claim in result['claims']:
        names = _attributions(claim['claim_text'], chunks)
        if not names:
            continue
        supports = [e for e in claim['evidence'] if e['evidence_role'] == 'support']
        if not supports or any(e['chunk_id'] not in by_id for e in supports):
            raise OutputVerificationError('Attributed claim requires exact document support')
        for evidence in supports:
            evidence['quote_text'] = _expand(
                by_id[evidence['chunk_id']]['chunk_text'], evidence['quote_text'], names)
