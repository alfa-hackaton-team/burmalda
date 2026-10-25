"""Run one pinned ML1 model behind an internal-only HTTP service."""

from __future__ import annotations

import argparse
import os
from threading import Lock
from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field


class EmbeddingInput(BaseModel):
    key: str = Field(min_length=1, max_length=256)
    text: str = Field(min_length=1)


class EmbeddingRequest(BaseModel):
    inputs: list[EmbeddingInput] = Field(min_length=1, max_length=8)


class RerankCandidate(BaseModel):
    chunk_id: str = Field(min_length=1, max_length=128)
    chunk_text: str = Field(min_length=1)


class RerankRequest(BaseModel):
    query: str = Field(min_length=1, max_length=20_000)
    candidates: list[RerankCandidate] = Field(min_length=1, max_length=50)


def create_app(role: str) -> FastAPI:
    if role not in {"embedding", "reranker"}:
        raise ValueError("role must be embedding or reranker")

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        cache_dir = os.environ.get("ML_MODEL_CACHE") or os.environ.get("HF_HOME")
        if role == "embedding":
            from ml.embeddings.qwen import QwenBatchEncoder

            app.state.model = QwenBatchEncoder(cache_dir=cache_dir, local_files_only=True)
        else:
            from ml.reranking import INFERENCE_PROFILES, QwenReranker

            app.state.model = QwenReranker(
                cache_dir=cache_dir,
                profile=INFERENCE_PROFILES["cpu-sdpa-b4"],
            )
        app.state.ready = True
        app.state.inference_lock = Lock()
        yield
        app.state.ready = False

    app = FastAPI(title=f"ML1 {role} model", docs_url=None, redoc_url=None, lifespan=lifespan)

    @app.get("/healthz")
    def healthz():
        return {"status": "ok" if getattr(app.state, "ready", False) else "starting", "role": role}

    if role == "embedding":
        @app.post("/v1/embeddings")
        def embeddings(request: EmbeddingRequest):
            from ml.embeddings.core import EmbeddingInput as CoreInput

            try:
                with app.state.inference_lock:
                    vectors = app.state.model.encode_batch(
                        [CoreInput(item.key, item.text) for item in request.inputs]
                    )
                return {"vectors": [{"key": item.key, "vector": list(item.vector)} for item in vectors]}
            except Exception:
                raise HTTPException(status_code=503, detail="Embedding inference failed") from None
    else:
        @app.post("/v1/rerank")
        def rerank(request: RerankRequest):
            try:
                candidates = [item.model_dump() for item in request.candidates]
                with app.state.inference_lock:
                    scores = app.state.model.score(request.query, candidates)
                return {"scores": [{"chunk_id": key, "score": score} for key, score in scores]}
            except Exception:
                raise HTTPException(status_code=503, detail="Reranker inference failed") from None

    return app


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--role", choices=("embedding", "reranker"), required=True)
    parser.add_argument("--port", type=int, default=None)
    args = parser.parse_args()

    import uvicorn

    default_port = 8101 if args.role == "embedding" else 8102
    uvicorn.run(create_app(args.role), host="0.0.0.0", port=args.port or default_port)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
