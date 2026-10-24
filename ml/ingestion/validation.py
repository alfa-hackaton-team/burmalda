"""Strict target R02 schema, with no legacy payload fallbacks."""
import json
from functools import lru_cache
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker

SCHEMA_PATH = Path(__file__).resolve().parents[2] / 'docs/ml/contracts/quality/v1/quality.schema.json'
_SCHEMA = json.loads(SCHEMA_PATH.read_text(encoding='utf-8'))


class DocumentInputError(ValueError):
    """Malformed metadata/input, distinct from a document failing quality rules."""

    code = 'INVALID_DOCUMENT_INPUT'


@lru_cache(maxsize=None)
def _validator(definition: str) -> Draft202012Validator:
    if definition not in _SCHEMA['$defs']:
        raise ValueError('Unknown quality contract definition')
    return Draft202012Validator(
        {'$ref': f'#/$defs/{definition}', '$defs': _SCHEMA['$defs']},
        format_checker=FormatChecker(),
    )


def validate(value: object, definition: str) -> None:
    if next(_validator(definition).iter_errors(value), None) is not None:
        # Do not expose input strings or the original jsonschema error instance.
        raise DocumentInputError(f'{definition}: quality contract validation failed')
