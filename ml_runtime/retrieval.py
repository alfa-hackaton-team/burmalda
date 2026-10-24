
"""ML1 retrieval boundary and fixture implementation used before integration."""

from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Protocol

from .analysis_validation import validate_search_request


class RetrievalContractError(ValueError):
    """ML1 returned a response that does not satisfy the shared contract."""


class Retriever(Protocol):
    def search(self, request: dict[str, Any]) -> dict[str, Any]: ...

    def get_chunks(self, request: dict[str, Any]) -> dict[str, Any]: ...


class FixtureRetriever:
    """Small local equivalent of ML1 R01's canonical fixture configuration.

    Like ML1's InMemoryRetriever.from_fixtures(), it serves only the registered
    query/context and applies the SearchRequest filters. Unknown queries return
    an ordinary empty result; they are not treated as retrieval errors.
    """

    def __init__(self, fixture_dir: Path | None = None) -> None:
        self.fixture_dir = fixture_dir or (
            Path(__file__).resolve().parents[1] / "docs/ml/fixtures/v1"
        )

    def search(self, request: dict[str, Any]) -> dict[str, Any]:
        configured = json.loads(
            (self.fixture_dir / "search.request.json").read_text(encoding="utf-8")
        )
        response = json.loads(
            (self.fixture_dir / "search.ok.json").read_text(encoding="utf-8")
        )
        empty = json.loads(
            (self.fixture_dir / "search.empty.json").read_text(encoding="utf-8")
        )
        response["trace_id"] = request["trace_id"]
        empty["trace_id"] = request["trace_id"]
        if request["business_context_id"] != configured["business_context_id"]:
            return {
                **empty,
                "status": "error",
                "error": {
                    "code": "CONTEXT_MISMATCH",
                    "message": "Retrieval operation could not be completed.",
                    "retryable": False,
                },
            }
        if request["query"].strip() != configured["query"]:
            return empty

        filters = request["filters"]
        ranked = [
            item for item in response["results"]
            if (not filters["document_version_ids"] or item["document_version_id"] in filters["document_version_ids"])
            and (not filters["source_ids"] or item["source_id"] in filters["source_ids"])
            and (not filters["source_roles"] or item["source_role"] in filters["source_roles"])
            and self._matches_date(item, filters)
        ]
        if not ranked:
            return empty
        response["results"] = ranked[:request["top_k"]]
        for rank, item in enumerate(response["results"], start=1):
            item["rank"] = rank
        response["status"] = "ok" if response["results"] else "empty"
        return response

    def get_chunks(self, request: dict[str, Any]) -> dict[str, Any]:
        configured = json.loads(
            (self.fixture_dir / "search.request.json").read_text(encoding="utf-8")
        )
        source = json.loads(
            (self.fixture_dir / "chunks.ok.json").read_text(encoding="utf-8")
        )
        if request["business_context_id"] != configured["business_context_id"]:
            return {
                "contract_version": "v1",
                "trace_id": request["trace_id"],
                "status": "error",
                "results": [],
                "unresolved_chunk_ids": [],
                "warnings": [],
                "error": {
                    "code": "CONTEXT_MISMATCH",
                    "message": "Retrieval operation could not be completed.",
                    "retryable": False,
                },
            }
        available = {chunk["chunk_id"]: chunk for chunk in source["results"]}
        results, unresolved = [], []
        for chunk_id in dict.fromkeys(request["chunk_ids"]):
            if chunk_id in available:
                results.append(available[chunk_id])
            else:
                unresolved.append(chunk_id)
        status = "ok" if results and not unresolved else "partial" if results else "empty"
        warnings = ["Some requested chunks could not be resolved."] if unresolved else []
        return {
            "contract_version": "v1",
            "trace_id": request["trace_id"],
            "status": status,
            "results": results,
            "unresolved_chunk_ids": unresolved,
            "warnings": warnings,
            "error": None,
        }

    @staticmethod
    def _matches_date(chunk: dict[str, Any], filters: dict[str, Any]) -> bool:
        start, end, published = filters["published_from"], filters["published_to"], chunk["published_at"]
        if start is None and end is None:
            return True
        if published is None:
            return filters["include_undated"]
        timestamp = lambda value: datetime.fromisoformat(value.replace("Z", "+00:00"))
        published_at = timestamp(published)
        return (start is None or published_at >= timestamp(start)) and (
            end is None or published_at < timestamp(end)
        )


@dataclass(frozen=True)
class RetrievalOutcome:
    search_request: dict[str, Any]
    search_response: dict[str, Any]
    chunks_request: dict[str, Any] | None
    chunks_response: dict[str, Any] | None

    @property
    def usable_chunks(self) -> list[dict[str, Any]]:
        return list((self.chunks_response or {}).get("results", []))


def build_search_request(
    analysis_request: dict[str, Any],
    top_k: int = 10,
    query_override: str | None = None,
) -> dict[str, Any]:
    """Create the first-pass SearchRequest from the event snapshot.

    The first contract fixture scopes retrieval to the event's source versions.
    Company profile data is intentionally not sent to the retriever.
    """
    if not isinstance(top_k, int) or isinstance(top_k, bool) or not 1 <= top_k <= 50:
        raise ValueError("top_k must be an integer from 1 through 50")
    event = analysis_request["event"]
    query_parts = [event["title"].strip(), event["summary"].strip()]
    locations = [place["name"].strip() for place in event["geography"] if place["name"].strip()]
    if locations:
        query_parts.append("География: " + ", ".join(locations))
    query = query_override if query_override is not None else ". ".join(query_parts)
    if not isinstance(query, str) or not query.strip():
        raise ValueError("search query must be a non-empty string")
    return {
        "contract_version": "v1",
        "trace_id": analysis_request["trace_id"],
        "business_context_id": analysis_request["context"]["business_context_id"],
        "query": query.strip(),
        "filters": {
            "published_from": None,
            "published_to": None,
            "source_ids": [],
            "source_roles": [],
            "document_version_ids": list(event["document_version_ids"]),
            "include_undated": True,
        },
        "top_k": top_k,
    }


def retrieve_for_analysis(
    retriever: Retriever,
    analysis_request: dict[str, Any],
    top_k: int = 10,
    query_override: str | None = None,
) -> RetrievalOutcome:
    """Run search, then fetch exact chunks while preserving contract semantics."""
    search_request = build_search_request(analysis_request, top_k, query_override)
    return retrieve_for_search(retriever, search_request)


def retrieve_for_search(retriever: Retriever, search_request: dict[str, Any]) -> RetrievalOutcome:
    """Execute one already planned SearchRequest and fetch its exact chunks."""
    search_request = validate_search_request(search_request)
    trace_id = search_request.get("trace_id")
    top_k = search_request.get("top_k")
    search_response = retriever.search(search_request)
    _validate_search_response(search_response, trace_id, top_k)
    outcome = RetrievalOutcome(search_request, search_response, None, None)
    if search_response["status"] in ("empty", "error"):
        return outcome

    chunk_ids = [item["chunk_id"] for item in search_response["results"]]
    chunks_request = {
        "contract_version": "v1",
        "trace_id": trace_id,
        "business_context_id": search_request["business_context_id"],
        "chunk_ids": chunk_ids,
    }
    chunks_response = retriever.get_chunks(chunks_request)
    _validate_chunks_response(chunks_response, trace_id, chunk_ids)
    return RetrievalOutcome(search_request, search_response, chunks_request, chunks_response)


def _validate_search_response(response: dict[str, Any], trace_id: str, top_k: int) -> None:
    if response.get("contract_version") != "v1" or response.get("trace_id") != trace_id:
        raise RetrievalContractError("SearchResponse contract_version/trace_id mismatch")
    status = response.get("status")
    results = response.get("results")
    if status not in {"ok", "empty", "partial", "error"} or not isinstance(results, list):
        raise RetrievalContractError("SearchResponse has invalid status or results")
    if len(results) > top_k:
        raise RetrievalContractError("SearchResponse exceeds top_k")
    if status in {"empty", "error"} and results:
        raise RetrievalContractError(f"SearchResponse {status} must have no results")
    if status in {"ok", "partial"} and not results:
        raise RetrievalContractError(f"SearchResponse {status} must have results")
    if status == "error" and response.get("error") is None:
        raise RetrievalContractError("SearchResponse error status requires error details")
    if status != "error" and response.get("error") is not None:
        raise RetrievalContractError("non-error SearchResponse must not contain error details")
    if status == "partial" and not response.get("warnings"):
        raise RetrievalContractError("partial SearchResponse must explain its limitation")
    if any(not isinstance(item, dict) for item in results):
        raise RetrievalContractError("SearchResponse results must contain objects")
    ids = [item.get("chunk_id") for item in results]
    if any(not isinstance(value, str) or not value for value in ids) or len(ids) != len(set(ids)):
        raise RetrievalContractError("SearchResponse has missing or duplicate chunk IDs")
    if [item.get("rank") for item in results] != list(range(1, len(results) + 1)):
        raise RetrievalContractError("SearchResponse ranks must be contiguous from 1")


def _validate_chunks_response(response: dict[str, Any], trace_id: str, requested_ids: list[str]) -> None:
    if response.get("contract_version") != "v1" or response.get("trace_id") != trace_id:
        raise RetrievalContractError("GetChunksResponse contract_version/trace_id mismatch")
    status = response.get("status")
    results = response.get("results")
    unresolved = response.get("unresolved_chunk_ids")
    if status not in {"ok", "empty", "partial", "error"} or not isinstance(results, list) or not isinstance(unresolved, list):
        raise RetrievalContractError("GetChunksResponse has invalid status or result fields")
    if any(not isinstance(item, dict) for item in results):
        raise RetrievalContractError("GetChunksResponse results must contain objects")
    returned_ids = [item.get("chunk_id") for item in results]
    if status == "error" and (results or unresolved or response.get("error") is None):
        raise RetrievalContractError("error GetChunksResponse must have empty results and an error")
    if status == "empty" and results:
        raise RetrievalContractError("empty GetChunksResponse must have no results")
    if status == "empty" and unresolved != requested_ids:
        raise RetrievalContractError("empty GetChunksResponse must mark every requested ID unresolved")
    if status == "ok" and (not results or unresolved):
        raise RetrievalContractError("ok GetChunksResponse must resolve every requested chunk")
    if status == "ok" and returned_ids != requested_ids:
        raise RetrievalContractError("ok GetChunksResponse must return every requested chunk")
    if status == "partial" and (not results or not unresolved or not response.get("warnings")):
        raise RetrievalContractError("partial GetChunksResponse must explain unresolved chunks")
    if len(returned_ids) != len(set(returned_ids)) or any(value not in requested_ids for value in returned_ids):
        raise RetrievalContractError("GetChunksResponse returned duplicate or unrequested chunks")
    if len(unresolved) != len(set(unresolved)) or any(value not in requested_ids for value in unresolved):
        raise RetrievalContractError("GetChunksResponse contains duplicate or unrequested unresolved IDs")
    if set(returned_ids) & set(unresolved):
        raise RetrievalContractError("GetChunksResponse marks an ID both resolved and unresolved")
    if status == "partial" and set(returned_ids) | set(unresolved) != set(requested_ids):
        raise RetrievalContractError("partial GetChunksResponse must account for every requested ID")
    if [requested_ids.index(value) for value in returned_ids] != sorted(
        requested_ids.index(value) for value in returned_ids
    ):
        raise RetrievalContractError("GetChunksResponse results do not preserve request order")
    if status != "error" and response.get("error") is not None:
        raise RetrievalContractError("non-error GetChunksResponse must not contain error details")
