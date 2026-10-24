import hashlib
import json
from pathlib import Path

import yaml
from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parent
api_path = ROOT / "docs/api/ml-openapi.yaml"
schemas_path = ROOT / "docs/api/schemas.yaml"
api = yaml.safe_load(api_path.read_text())
schemas = yaml.safe_load(schemas_path.read_text())
request_schema = dict(api["paths"]["/internal/v0.1/article-processings"]["post"]["requestBody"]["content"]["application/json"]["schema"])
request_schema["$id"] = api_path.as_uri()
registry = Registry().with_resource(schemas_path.as_uri(), Resource.from_contents(schemas, default_specification=DRAFT202012))
validator = Draft202012Validator(request_schema, registry=registry, format_checker=FormatChecker())
count = 0
for folder in ("article", "rss", "bank-pdf"):
    directory = HERE / folder
    for entry in json.loads((directory / "manifest.json").read_text()):
        request = json.loads((directory / entry["file"]).read_text())
        validator.validate(request)
        article = request["article"]
        assert entry["fixture_only"] is True
        assert request["pipeline_version"] == "UNCONFIRMED-FIXTURE-ONLY"
        assert article["article_version_id"] == entry["article_version_id"]
        assert article["article_id"] == entry["article_id"]
        assert article["source_id"] == request["source"]["source_id"] == entry["source_id"]
        assert article["raw_ref"] == "raw-artifact:" + entry["raw_artifact_id"]
        assert len(article["body"].encode("utf-8")) == entry["text_bytes"] <= 131072
        assert entry["idempotency_key"] == article["article_version_id"] + ":" + request["pipeline_version"]
        if folder == "bank-pdf":
            original = ROOT / "data/hackathon" / article["external_id"]
            assert hashlib.sha256(original.read_bytes()).hexdigest() == entry["raw_sha256"]
            assert request["source"]["source_role"] == "bank"
            assert article["body"] == original.with_suffix(".txt").read_bytes().decode("utf-8")
            assert entry["extraction_method"] == "provided-text"
            assert "published_at" not in article
        else:
            assert request["source"]["source_role"] == "external"
        count += 1
synthetic = HERE / "synthetic"
first = (synthetic / "empty-title-no-date.request.json").read_bytes()
assert first == (synthetic / "repeat-same-version.request.json").read_bytes()
request = json.loads(first)
validator.validate(request)
assert request["article"]["title"] == ""
assert "published_at" not in request["article"]
manifest = json.loads((synthetic / "manifest.json").read_text())
assert request["article"]["raw_ref"] == "raw-artifact:" + manifest["raw_artifact_id"]
assert request["article"]["source_id"] == request["source"]["source_id"] == manifest["source_id"]
assert hashlib.sha256((synthetic / "original.txt").read_bytes()).hexdigest() == manifest["raw_sha256"]
refusal = json.loads((synthetic / "size-rejection.json").read_text())
assert refusal["http_requests"] == 0 and refusal["body_bytes"] == len(("я" * 65536 + "a").encode("utf-8")) == 131073
assert refusal["error"]["code"] == "PAYLOAD_TOO_LARGE"
print(f"Validated {count} actual requests, synthetic empty-title/replay requests and size refusal")
