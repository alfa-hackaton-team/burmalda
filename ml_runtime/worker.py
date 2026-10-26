"""Worker entry points for draining persisted article-processing operations."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from .operations import ArticleProcessingCoordinator


@dataclass
class ArticleProcessingWorker:
    coordinator: ArticleProcessingCoordinator

    def run_once(self) -> dict[str, Any] | None:
        """Claim and execute at most one QUEUED operation; safe across replicas."""
        return self.coordinator.execute_next()
