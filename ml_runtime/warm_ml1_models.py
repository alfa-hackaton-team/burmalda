"""Download the exact ML1 embedding model revision used by Docker workers."""

from __future__ import annotations

import os


def main() -> int:
    from huggingface_hub import snapshot_download
    from ml.embeddings.profile import V1

    cache_dir = os.environ.get("ML_MODEL_CACHE") or os.environ.get("HF_HOME")
    snapshot_download(
        repo_id=V1.model,
        revision=V1.revision,
        cache_dir=cache_dir,
    )
    print(f"Cached {V1.model}@{V1.revision}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
