"""Download exact ML1 embedding and reranker revisions into the shared HF cache."""

from __future__ import annotations

import os
from pathlib import Path


def main() -> int:
    from huggingface_hub import snapshot_download
    from ml.embeddings.profile import V1 as EMBEDDING
    from ml.reranking import PROFILE as RERANKER

    cache_dir = (os.environ.get("ML_MODEL_CACHE") or os.environ.get("HF_HOME")
                 or str(Path.cwd() / "docker-volume" / "models"))
    for profile in (EMBEDDING, RERANKER):
        snapshot_download(
            repo_id=profile.model,
            revision=profile.revision,
            cache_dir=cache_dir,
        )
        print(f"Cached {profile.model}@{profile.revision}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
