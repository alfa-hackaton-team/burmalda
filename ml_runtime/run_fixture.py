"""Run the first ML2 analysis fixture end to end: python3 -m ml_runtime.run_fixture."""

import json
from pathlib import Path

from .analysis import FixtureAnalysisService
from .retrieval import FixtureRetriever


def main() -> None:
    fixture_dir = Path(__file__).resolve().parents[1] / "docs/ml/fixtures/v1"
    request = json.loads((fixture_dir / "analysis.request.json").read_text(encoding="utf-8"))
    result = FixtureAnalysisService(FixtureRetriever(fixture_dir), fixture_dir).analyze(request)
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
