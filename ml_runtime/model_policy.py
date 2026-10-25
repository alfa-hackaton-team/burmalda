"""Hackathon model allowlist and default selection for the ML2 analysis agent."""

from __future__ import annotations


ALLOWED_MODELS = frozenset({
    "google/gemma-4-26b-a4b-it",
    "z-ai/glm-5.3-flash",
    "qwen/qwen3.6-35b-a3b",
    "deepseek/deepseek-v4.1-flash",
    "qwen/qwen3-vl-30b-a3b-instruct",
    "moonshotai/kimi-k2.6",
})

# The task is business-event analysis and explanation, which the appendix
# assigns to models intended for deeper analysis and reasoning.
DEFAULT_AGENT_MODEL = "z-ai/glm-5.3-flash"
# Use a different allowlisted model for the second-pass evidence entailment check.
DEFAULT_VERIFIER_MODEL = "qwen/qwen3.6-35b-a3b"


def require_allowed_model(model_name: str) -> str:
    """Reject model names outside the organizer-approved list."""
    if model_name not in ALLOWED_MODELS:
        raise ValueError(f"model is not on the hackathon allowlist: {model_name!r}")
    return model_name
