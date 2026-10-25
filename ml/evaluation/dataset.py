"""Fixed synthetic corpus and explicit, model-independent relevance judgments."""
import hashlib
import json
from pathlib import Path
from uuid import NAMESPACE_URL, UUID, uuid5

from ml.ingestion.dto import ExtractionMetadata, SavedDocumentVersion, SourceMetadata

DATASET = Path(__file__).parent / 'data' / 'business_news_v1.json'


def identity(key: str) -> str:
    return str(uuid5(NAMESPACE_URL, f'ml1-evaluation/business-news-v1/{key}'))


def saved_document(item: dict) -> SavedDocumentVersion:
    """Fixture construction only: this is not Go ingestion or Go hash repair."""
    key = item['key']
    real = item.get('provenance')
    source_name = real['publisher'] if real else 'Вымышленная деловая лента evaluation'
    return SavedDocumentVersion(
        document_id=identity(f'document:{key}'), document_version_id=identity(f'version:{key}:1'),
        title=item['title'], normalized_text=item['text'],
        source=SourceMetadata(identity('source:' + source_name) if real else identity('source'), source_name,
                              'regulator' if real else 'news', 'external', 'primary' if real else 'standard',
                              '1', 'regulator' if real else 'news', 'primary' if real else 'standard'),
        published_at=item['published_at'], retrieved_at='2026-10-25T12:00:00Z' if real else '2026-10-21T12:00:00Z',
        raw_ref='raw-artifact:' + identity(f'raw:{key}'),
        content_hash=hashlib.sha256(item['text'].encode('utf-8')).hexdigest(),
        extraction=ExtractionMetadata('full_article', ()), pipeline_version='evaluation-fixture-v1',
        trace_id=identity(f'ingestion-trace:{key}'), external_id=key,
        canonical_url=real['url'] if real else f'https://example.org/ml1-evaluation/{key}',
        language=item.get('language', 'ru'), author=None,
    )


def load_dataset(path: Path = DATASET) -> tuple[dict, str]:
    raw = path.read_bytes()
    dataset = json.loads(raw)
    validate_dataset(dataset)
    return dataset, hashlib.sha256(raw).hexdigest()


def validate_dataset(dataset: dict) -> None:
    if not dataset['dataset_version'].strip() or not dataset['relevance_policy'].strip():
        raise ValueError('Dataset must identify its version and relevance policy')
    documents, queries = dataset['documents'], dataset['queries']
    if not documents or not queries:
        raise ValueError('Evaluation needs documents and queries')
    if len({item['key'] for item in documents}) != len(documents):
        raise ValueError('Duplicate document keys')
    ids = []
    for item in documents:
        saved_document(item)  # Canonical R02 validation, without tokenizer/model imports.
        if not item['text'].strip() or not item['chunks']:
            raise ValueError('Empty fixture document/chunks')
        for chunk in item['chunks']:
            UUID(chunk['chunk_id'])
            ids.append(chunk['chunk_id'])
            if (len(chunk['text_sha256']) != 64
                    or any(c not in '0123456789abcdef' for c in chunk['text_sha256'])):
                raise ValueError('Invalid fixture chunk hash')
    if len(set(ids)) != len(ids):
        raise ValueError('Duplicate chunk IDs')
    known = set(ids)
    if len({q['query_id'] for q in queries}) != len(queries):
        raise ValueError('Duplicate query IDs')
    for query in queries:
        positives, negatives = query['relevant_chunk_ids'], query['hard_negative_chunk_ids']
        no_answer = query.get('answerable') is False
        if (not query['text'].strip() or not query['rationale'].strip()
                or (not positives and not no_answer) or (no_answer and positives)
                or (no_answer and not query.get('no_answer_reason', '').strip())):
            raise ValueError('Every query needs text, rationale and positive judgments')
        for judgments in (positives, negatives):
            if len(set(judgments)) != len(judgments) or not set(judgments) <= known:
                raise ValueError('Duplicate or unknown judgment IDs')
        if set(positives) & set(negatives):
            raise ValueError('Positive and negative judgments overlap')
        for aspect in query.get('aspects', []):
            if (not aspect['text'].strip() or not aspect['relevant_chunk_ids']
                    or not set(aspect['relevant_chunk_ids']) <= set(positives)):
                raise ValueError('Aspect must reference nonempty query positives')


def build_corpus(dataset, counter):
    from ml.chunking import chunk_document

    corpus = []
    for item in dataset['documents']:
        saved = saved_document(item)
        chunks = chunk_document(saved, counter)
        actual = [{'chunk_id': c.id, 'chunk_no': c.chunk_no,
                   'text_sha256': hashlib.sha256(c.chunk_text.encode('utf-8')).hexdigest()} for c in chunks]
        if actual != item['chunks']:
            raise ValueError(f"Frozen chunk manifest changed: {item['key']}; version dataset/qrels explicitly")
        corpus.append((saved, chunks))
    return corpus
