"""Composition roots for the lightweight API and the model-backed worker."""

from __future__ import annotations

import importlib
import os
from dataclasses import dataclass
from typing import Any

from fastapi import FastAPI

from .article_processing_api import create_article_processing_app_from_env
from .event_extraction import (
    DEFAULT_EVENT_PIPELINE_CONFIG_VERSION,
    LLMEventCandidateVerifier,
    LLMEventExtractor,
)
from .event_store import EventCandidateStore, ML2EventProcessor, PostgresEventStore
from .model_client import DEFAULT_BASE_URL, OpenAICompatibleModelClient
from .model_policy import DEFAULT_AGENT_MODEL, DEFAULT_VERIFIER_MODEL, require_allowed_model
from .ml1_indexer import ML1IndexerAdapter, ML1Pipeline
from .operations import (
    ArticleProcessingCoordinator,
    ArticleProcessingSubmissionService,
    OperationStore,
)
from .postgres_store import PostgresOperationStore
from .quality_gate import ML1R02QualityGate
from .worker import ArticleProcessingWorker


@dataclass(frozen=True)
class ArticleProcessingRuntime:
    """Combined in-process runtime retained for tests and local embedding."""

    app: FastAPI
    coordinator: ArticleProcessingCoordinator
    worker: ArticleProcessingWorker


@dataclass(frozen=True)
class ArticleProcessingAPIRuntime:
    app: FastAPI
    service: ArticleProcessingSubmissionService


@dataclass(frozen=True)
class ArticleProcessingWorkerRuntime:
    coordinator: ArticleProcessingCoordinator
    worker: ArticleProcessingWorker


def load_indexer_from_env() -> ML1Pipeline:
    """Load ML1's deployment-owned indexer factory from ``module:factory``."""
    target = os.environ.get("ML_INDEXER_FACTORY", "").strip()
    module_name, separator, factory_name = target.partition(":")
    if not separator or not module_name or not factory_name or ":" in factory_name:
        raise ValueError("ML_INDEXER_FACTORY must use the module.path:factory_name form")
    module = importlib.import_module(module_name)
    factory = getattr(module, factory_name, None)
    if not callable(factory):
        raise ValueError("ML_INDEXER_FACTORY must identify a callable factory")
    indexer = factory()
    if not callable(getattr(indexer, "index", None)) or not callable(getattr(indexer, "get_chunks", None)):
        raise ValueError("ML indexer factory must return an ML1Pipeline with index() and get_chunks()")
    return indexer


def create_article_processing_api_runtime(
    *,
    operation_store: OperationStore | None = None,
    quality_gate: Any | None = None,
) -> ArticleProcessingAPIRuntime:
    """Create the Go-facing API without loading embeddings or the LLM client."""
    service = ArticleProcessingSubmissionService(
        store=operation_store if operation_store is not None else PostgresOperationStore(),
        quality_gate=quality_gate if quality_gate is not None else ML1R02QualityGate(),
    )
    return ArticleProcessingAPIRuntime(
        app=create_article_processing_app_from_env(service),
        service=service,
    )


def create_article_processing_worker_runtime(
    indexer: ML1Pipeline,
    *,
    model_client: Any | None = None,
    operation_store: OperationStore | None = None,
    event_store: EventCandidateStore | None = None,
) -> ArticleProcessingWorkerRuntime:
    """Create the model/indexing worker without requiring HTTP credentials."""
    coordinator = _create_coordinator(
        indexer,
        model_client=model_client,
        operation_store=operation_store,
        event_store=event_store,
    )
    return ArticleProcessingWorkerRuntime(coordinator, ArticleProcessingWorker(coordinator))


def create_article_processing_runtime(
    indexer: ML1Pipeline,
    *,
    model_client: Any | None = None,
    operation_store: OperationStore | None = None,
    event_store: EventCandidateStore | None = None,
) -> ArticleProcessingRuntime:
    """Wire the API and worker together for tests or a single-process setup."""
    coordinator = _create_coordinator(
        indexer,
        model_client=model_client,
        operation_store=operation_store,
        event_store=event_store,
    )
    app = create_article_processing_app_from_env(coordinator)
    worker = ArticleProcessingWorker(coordinator)
    app.state.article_processing_worker = worker
    return ArticleProcessingRuntime(app=app, coordinator=coordinator, worker=worker)


def _create_coordinator(
    indexer: ML1Pipeline,
    *,
    model_client: Any | None,
    operation_store: OperationStore | None,
    event_store: EventCandidateStore | None,
) -> ArticleProcessingCoordinator:
    client = model_client if model_client is not None else OpenAICompatibleModelClient(
        api_key=os.environ.get("ML_LLM_API_KEY", ""),
        base_url=os.environ.get("ML_LLM_BASE_URL", DEFAULT_BASE_URL),
        reasoning_effort='low',
    )
    event_model = require_allowed_model(
        os.environ.get("ML_LLM_EVENT_MODEL", os.environ.get("ML_LLM_MODEL", DEFAULT_AGENT_MODEL)).strip()
    )
    verifier_model = require_allowed_model(
        os.environ.get(
            "ML_LLM_EVENT_VERIFIER_MODEL",
            os.environ.get("ML_LLM_VERIFIER_MODEL", DEFAULT_VERIFIER_MODEL),
        ).strip()
    )
    event_config_version = os.environ.get(
        "ML_EVENT_PIPELINE_CONFIG_VERSION", DEFAULT_EVENT_PIPELINE_CONFIG_VERSION
    ).strip()
    if not event_config_version:
        raise ValueError("ML_EVENT_PIPELINE_CONFIG_VERSION must be non-empty")

    extractor = LLMEventExtractor(
        client,
        model_name=event_model,
        verifier=LLMEventCandidateVerifier(client, verifier_model),
        event_pipeline_config_version=event_config_version,
    )
    coordinator = ArticleProcessingCoordinator(
        store=operation_store if operation_store is not None else PostgresOperationStore(),
        quality_gate=ML1R02QualityGate(),
        indexer=ML1IndexerAdapter(indexer),
        event_processor=ML2EventProcessor(
            extractor,
            event_store if event_store is not None else PostgresEventStore(),
        ),
    )
    return coordinator
