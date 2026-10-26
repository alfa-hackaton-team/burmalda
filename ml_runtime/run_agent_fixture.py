"""Run the agent and, for a successful response, a second semantic check.

The successful path makes two paid model calls; an insufficient-evidence
response exits after generation and does not invoke the semantic verifier.

Run from the repository root after setting ML_LLM_API_KEY locally:
    python3 -m ml_runtime.run_agent_fixture
"""

from __future__ import annotations

import json
from pathlib import Path

from .model_client import create_agent_from_env
from .retrieval import FixtureRetriever
from .search_plan import FixtureSearchPlanner
from .service import AnalysisService


def main() -> int:
    fixture_dir = Path(__file__).resolve().parents[1] / "docs/ml/fixtures/v1"
    request = json.loads((fixture_dir / "analysis.request.json").read_text(encoding="utf-8"))
    service = AnalysisService(
        retriever=FixtureRetriever(fixture_dir),
        planner=FixtureSearchPlanner(fixture_dir),
        agent=create_agent_from_env(),
    )
    response = service.analyze(request)
    print(json.dumps(response, ensure_ascii=False, indent=2))
    return 0 if response["status"] in {"succeeded", "insufficient_evidence"} else 1


if __name__ == "__main__":
    raise SystemExit(main())
