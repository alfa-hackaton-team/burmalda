"""OpenAI-compatible Chat Completions client for the hackathon gateway."""

from __future__ import annotations

import json
import os
import sys
from typing import Any
from urllib.parse import urlsplit

from openai import APIConnectionError, APIStatusError, APITimeoutError, OpenAI

from .agent import AnalysisAgent, ModelCallError
from .model_policy import DEFAULT_AGENT_MODEL, require_allowed_model


DEFAULT_BASE_URL = "https://changellenge.up.railway.app/v1"
ALLOWED_BASE_URLS = frozenset({
    DEFAULT_BASE_URL,
    "https://api.alfahack-gateway26.ru/v1",
})


def _log_safe_diagnostic(exc: Exception, *, status_code: int | None = None) -> None:
    """Log provider diagnostics locally without printing credentials or payloads."""
    if os.environ.get("ML_LLM_DEBUG") != "1":
        return
    details = [f"error_type={type(exc).__name__}"]
    if status_code is not None:
        details.append(f"status_code={status_code}")
    request_id = getattr(exc, "request_id", None)
    if request_id:
        details.append(f"request_id={request_id}")
    print("ML gateway diagnostic: " + " ".join(details), file=sys.stderr)


class OpenAICompatibleModelClient:
    """Calls the configured organizer gateway and returns a parsed JSON object."""

    def __init__(self, api_key: str, base_url: str = DEFAULT_BASE_URL) -> None:
        if not api_key.strip():
            raise ValueError("ML_LLM_API_KEY must be set")
        normalized_url = base_url.rstrip("/")
        parsed_url = urlsplit(normalized_url)
        if (
            normalized_url not in ALLOWED_BASE_URLS
            or parsed_url.scheme != "https"
            or parsed_url.username is not None
            or parsed_url.password is not None
            or parsed_url.query
            or parsed_url.fragment
        ):
            raise ValueError("ML_LLM_BASE_URL must be one of the organizer-provided HTTPS endpoints")

        # Disable SDK-level retries so one application call cannot silently
        # trigger multiple model requests and consume the daily allowance.
        self._client = OpenAI(
            api_key=api_key.strip(),
            base_url=normalized_url,
            max_retries=0,
        )

    def generate_json(
        self,
        *,
        model_name: str,
        system_prompt: str,
        input_payload: dict[str, Any],
        timeout_seconds: float,
    ) -> dict[str, Any]:
        model = require_allowed_model(model_name)
        try:
            completion = self._client.chat.completions.create(
                model=model,
                messages=[
                    {"role": "system", "content": system_prompt},
                    {
                        "role": "user",
                        "content": json.dumps(input_payload, ensure_ascii=False),
                    },
                ],
                response_format={"type": "json_object"},
                timeout=max(0.1, timeout_seconds),
            )
        except APITimeoutError as exc:
            _log_safe_diagnostic(exc)
            raise ModelCallError("Model request timed out.", "TIMEOUT", True) from exc
        except APIConnectionError as exc:
            _log_safe_diagnostic(exc)
            raise ModelCallError("Model gateway is unavailable.", "MODEL_UNAVAILABLE", True) from exc
        except APIStatusError as exc:
            _log_safe_diagnostic(exc, status_code=exc.status_code)
            retryable = exc.status_code == 429 or exc.status_code >= 500
            raise ModelCallError(
                "Model gateway rejected or failed the request.",
                "MODEL_UNAVAILABLE",
                retryable,
            ) from exc
        except Exception as exc:
            # Never return raw provider exception text; it can contain request
            # details and is not a stable part of the ML contract.
            _log_safe_diagnostic(exc)
            raise ModelCallError("Model gateway request failed.", "MODEL_UNAVAILABLE", True) from exc

        if not completion.choices:
            raise ModelCallError("Model returned no choices.", "VERIFICATION_FAILED", False)
        content = completion.choices[0].message.content
        if not isinstance(content, str) or not content.strip():
            raise ModelCallError("Model returned no JSON content.", "VERIFICATION_FAILED", False)
        try:
            value = json.loads(content)
        except json.JSONDecodeError as exc:
            raise ModelCallError("Model response was not valid JSON.", "VERIFICATION_FAILED", False) from exc
        if not isinstance(value, dict):
            raise ModelCallError("Model response must be a JSON object.", "VERIFICATION_FAILED", False)
        return value


def create_agent_from_env() -> AnalysisAgent:
    """Build the agent from local environment variables without printing secrets."""
    api_key = os.environ.get("ML_LLM_API_KEY", "")
    base_url = os.environ.get("ML_LLM_BASE_URL", DEFAULT_BASE_URL)
    model_name = require_allowed_model(
        os.environ.get("ML_LLM_MODEL", DEFAULT_AGENT_MODEL).strip()
    )
    return AnalysisAgent(
        OpenAICompatibleModelClient(api_key=api_key, base_url=base_url),
        model_name=model_name,
    )
