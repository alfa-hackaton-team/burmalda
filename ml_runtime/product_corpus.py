"""Read the real shared ML corpus and create an event-scoped ML1 retriever."""
from contextlib import contextmanager
import os

from .product_store import ProductStore


class EventEvidenceRetriever:
    """Resolve ML2's verified event citations through ML1's exact get_chunks API.

    The event already identifies its evidence. Re-embedding its own title is
    unnecessary for this first analysis; no synthetic similarity score is used.
    """
    def __init__(self, reader, chunk_ids):
        self.reader, self.chunk_ids = reader, list(dict.fromkeys(chunk_ids))[:100]

    def get_chunks(self, request):
        return self.reader.get_chunks(request)

    def search(self, request):
        from .analysis_validation import validate_search_request, validate_contract_message
        from datetime import datetime
        validate_search_request(request)
        if not self.chunk_ids:
            return {'contract_version': 'v1', 'trace_id': request['trace_id'], 'status': 'empty',
                'results': [], 'retrieval_config_version': 'stored-event-evidence-v1', 'warnings': [], 'error': None}
        found = self.get_chunks({'contract_version': 'v1', 'trace_id': request['trace_id'],
            'business_context_id': request['business_context_id'], 'chunk_ids': self.chunk_ids})
        result = {'contract_version': 'v1', 'trace_id': request['trace_id'],
            'status': 'empty', 'results': [], 'retrieval_config_version': 'stored-event-evidence-v1',
            'warnings': found['warnings'], 'error': found['error']}
        if found['status'] == 'error':
            result['status'] = 'error'
        else:
            filters = request['filters']
            def eligible(chunk):
                for field, values in (('document_version_id', filters['document_version_ids']),
                                      ('source_id', filters['source_ids']), ('source_role', filters['source_roles'])):
                    if values and chunk[field] not in values:
                        return False
                date = chunk['published_at']
                if date is None:
                    return filters['include_undated']
                instant = datetime.fromisoformat(date.replace('Z', '+00:00'))
                return ((filters['published_from'] is None or instant >= datetime.fromisoformat(filters['published_from'].replace('Z', '+00:00')))
                    and (filters['published_to'] is None or instant < datetime.fromisoformat(filters['published_to'].replace('Z', '+00:00'))))
            chunks = [chunk for chunk in found['results'] if eligible(chunk)][:request['top_k']]
            result['results'] = [{**chunk, 'rank': rank, 'retrieval_score': None}
                                 for rank, chunk in enumerate(chunks, 1)]
            if chunks:
                result['status'] = 'partial' if found['unresolved_chunk_ids'] else 'ok'
        validate_contract_message(result, 'SearchResponse')
        return result


class ProductSearchPlanner:
    """Keep event evidence and independently authorized bank context separate."""
    def __init__(self, bank_versions):
        self.bank_versions = list(bank_versions)

    def plan(self, request):
        from .retrieval import build_search_request
        event = build_search_request(request, top_k=5)
        if not self.bank_versions:
            return [event]
        bank = build_search_request(request, top_k=5)
        bank['filters']['document_version_ids'] = self.bank_versions
        bank['filters']['source_roles'] = ['bank']
        return [event, bank]


class ProductContextRetriever:
    def __init__(self, event, bank, reader):
        self.event, self.bank, self.reader = event, bank, reader

    def search(self, request):
        if request['filters']['source_roles'] == ['bank']:
            return self.bank.search(request)
        return self.event.search(request)

    def get_chunks(self, request):
        return self.reader.get_chunks(request)


def event_topic(event_type):
    text = event_type.casefold()
    for topic, words in (
        ('financing', ('rate', 'credit', 'loan', 'monetary policy', 'fomc', 'денежно-кредит', 'ставк', 'кредит', 'финанс')),
        ('logistics', ('supply', 'transport', 'logistic', 'постав', 'перевоз', 'логист')),
        ('liquidity', ('deposit', 'liquid', 'депозит', 'ликвид')),
        ('regulation', ('regulat', 'tax', 'report', 'налог', 'регул', 'отчет', 'отчёт')),
    ):
        if any(word in text for word in words):
            return topic
    return 'market'


class ProductCorpus:
    def __init__(self, store=None):
        self.store = store or ProductStore()

    def feed_items(self, topic=None):
        with self.store.connect() as connection:
            rows = connection.execute('''SELECT e.id, e.event_type, e.title, e.summary,
                min((d.snapshot->>'published_at')::timestamptz) AS published_at,
                jsonb_agg(DISTINCT jsonb_strip_nulls(jsonb_build_object(
                    'source_id', d.snapshot#>>'{source,source_id}',
                    'name', d.snapshot#>>'{source,name}',
                    'source_role', d.snapshot#>>'{source,source_role}',
                    'url', d.snapshot->>'canonical_url'))) AS sources
                FROM ml.events e JOIN ml.event_documents ed ON ed.event_id=e.id
                JOIN ml.document_version_catalog c ON c.document_version_id=ed.document_version_id AND c.ready
                JOIN ml.document_versions d ON d.id=c.document_version_id
                WHERE e.status <> 'rejected'
                  AND d.snapshot#>>'{source,source_role}' IN ('external','bank','expert')
                GROUP BY e.id ORDER BY e.created_at DESC, e.id DESC LIMIT 5000''').fetchall()
        items = []
        for row in rows:
            category = event_topic(row['event_type'])
            if topic and category != topic:
                continue
            items.append({
                'preview_id': str(row['id']),
                'event': {'event_id': str(row['id']), 'title': row['title'], 'summary': row['summary']},
                'published_at': row['published_at'].isoformat() if row['published_at'] else None,
                'topic': category,
                'why_relevant': 'Событие выделено из сохранённых источников. Применимость к вашей компании требует разбора.',
                'feature_refs': [], 'sources': row['sources'],
                'impact': {'label': 'Требует разбора',
                    'explanation': 'Влияние на компанию ещё не оценено.',
                    'policy_version': 'shared-events-recency-v1'},
            })
        return items

    @contextmanager
    def analysis_service(self, request):
        from .model_client import create_agent_from_env
        from .service import AnalysisService
        agent = create_agent_from_env()
        if request['request_type'] == 'follow_up':
            # AnalysisService uses exactly Go's persisted initial evidence snapshot.
            yield AnalysisService(None, agent)
            return
        from ml.adapters.postgres_catalog import load_versions
        from ml.database import connect_runtime
        from ml.embeddings import Embedder
        from ml.pipeline import ML1Pipeline
        from ml.adapters.postgres_retrieval import PostgresRetriever
        from .ml1_model_client import RemoteEmbeddingEncoder, RemoteQwenReranker
        versions = request['event']['document_version_ids']
        if not versions:
            raise ValueError('Event must have indexed source versions')
        with self.store.connect() as connection:
            # Recheck event linkage/readiness; never grant the arbitrary IDs in a request.
            rows = connection.execute('''SELECT DISTINCT ch.id FROM ml.document_chunks ch
                JOIN ml.document_version_catalog c ON c.document_version_id=ch.document_version_id AND c.ready
                JOIN ml.document_versions d ON d.id=c.document_version_id
                JOIN ml.event_documents ed ON ed.document_version_id=c.document_version_id
                JOIN ml.events e ON e.id=ed.event_id AND e.status<>'rejected'
                WHERE e.id=%s AND c.document_version_id=ANY(%s::uuid[])
                  AND d.snapshot#>>'{source,source_role}' IN ('external','bank','expert')
                  AND EXISTS (SELECT 1 FROM jsonb_array_elements(ed.evidence_quotes) q
                              WHERE q->>'chunk_id'=ch.id::text)
                ORDER BY ch.id''',
                (request['event']['id'], versions)).fetchall()
            # The only additional corpus is the shared, primary bank-report
            # source registered by dataset import. Never trust IDs from HTTP.
            from uuid import NAMESPACE_URL, uuid5
            bank_source = str(uuid5(NAMESPACE_URL, 'alfa-hackathon-reference-pdfs'))
            bank_rows = connection.execute('''SELECT ch.id, c.document_version_id
                FROM ml.document_chunks ch
                JOIN ml.document_version_catalog c ON c.document_version_id=ch.document_version_id AND c.ready
                JOIN ml.document_versions d ON d.id=c.document_version_id
                WHERE EXISTS (SELECT 1 FROM ml.event_documents event_doc
                      JOIN ml.document_versions event_version ON event_version.id=event_doc.document_version_id
                      WHERE event_doc.event_id=%s AND event_version.snapshot#>>'{source,source_role}'='external')
                  AND d.snapshot#>>'{source,source_id}'=%s
                  AND d.snapshot#>>'{source,source_role}'='bank'
                  AND d.snapshot#>>'{source,trust_level}'='primary'
                  AND NOT EXISTS (SELECT 1 FROM ml.document_version_catalog newer
                      WHERE newer.document_id=c.document_id AND newer.version_no>c.version_no)
                  AND (%s::timestamptz IS NULL OR d.snapshot->>'published_at' IS NULL
                      OR (d.snapshot->>'published_at')::timestamptz<=%s::timestamptz)
                ORDER BY c.document_version_id,ch.id''',
                (request['event']['id'], bank_source, request['event']['occurred_at'], request['event']['occurred_at'])).fetchall()
        with connect_runtime() as connection:
            catalog = load_versions(connection)
            ids = [str(row['id']) for row in rows]
            access = {request['context']['business_context_id']: ids}
            embedder = Embedder(RemoteEmbeddingEncoder(timeout=20))
            mode = os.environ.get('ML_ANALYSIS_RETRIEVAL_MODE', 'event-evidence')
            if mode == 'event-evidence':
                reader = PostgresRetriever(connection, embedder, versions=catalog, context_chunks=access)
                retriever = EventEvidenceRetriever(reader, ids)
            elif mode == 'semantic':
                retriever = ML1Pipeline(connection, embedder, None, versions=catalog,
                    context_chunks=access, reranker=RemoteQwenReranker(timeout=25))
            else:
                raise ValueError('Unsupported ML_ANALYSIS_RETRIEVAL_MODE')
            bank_ids = [str(row['id']) for row in bank_rows]
            bank_versions = tuple(sorted({str(row['document_version_id']) for row in bank_rows}
                                         - set(versions)))
            if not ids:
                bank_versions = ()  # Bank context cannot replace missing event evidence.
            if bank_versions:
                reader = PostgresRetriever(connection, embedder, versions=catalog,
                    context_chunks={request['context']['business_context_id']: ids + bank_ids})
                bank = ML1Pipeline(connection, embedder, None, versions=catalog,
                    context_chunks={request['context']['business_context_id']: bank_ids},
                    reranker=RemoteQwenReranker(timeout=25))
                retriever = ProductContextRetriever(retriever, bank, reader)
                # This scope is constructed from trusted catalog/source records,
                # never from caller-supplied document IDs.
                agent.context_document_version_ids = frozenset(bank_versions)
            yield AnalysisService(retriever, agent, planner=ProductSearchPlanner(bank_versions))
