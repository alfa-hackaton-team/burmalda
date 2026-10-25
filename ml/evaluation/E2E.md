# Real E2E diagnostics

Use the frozen `data/official_holdout_v1.json` (12 official publications, 51 chunks,
18 queries, including 4 no-answer). Gold spans/UUIDs predate inference. Do not
change the corpus/qrels to improve these results.

The scripts use the existing pinned embedding, chunking and CPU SDPA batch-4
reranker profiles. They do not implement Go routes, authorization or a frontend.
A component run is not evidence of complete product integration.

## Run

Install existing `ml/requirements-embeddings.txt`, `ml/requirements-persistence.txt`
and `ml_runtime/requirements.txt` in a Python environment. Models must already be
cached. Set `HF_HUB_OFFLINE=1` and `ML1_EVAL_POSTGRES_DSN` to an explicitly disposable
PostgreSQL database with pgvector installed. `e2e` creates/drops a unique schema;
`http_smoke` needs CREATE DATABASE and drops only its uniquely named database.

```sh
# ML1: real BackendInput -> quality -> chunking -> embeddings -> pgvector -> reranker.
# Keep gateway credentials unset here to separate the two diagnostic stages.
env -u ML_LLM_API_KEY python -m ml.evaluation.e2e --output .local/e2e/ml1.json

# Load ML_LLM_API_KEY securely from your ignored local configuration, without echoing it.
# Real agent + existing independent verifier over exactly the saved Top-5.
python -m ml.evaluation.e2e_ml2 --input .local/e2e/ml1.json --output .local/e2e/ml2.json
python -m ml.evaluation.grounding --output .local/e2e/grounding.json
python -m ml.evaluation.http_smoke --output .local/e2e/http.json
python -m ml.evaluation.e2e_report --input .local/e2e/ml2.json --output .local/e2e/table.md
```

`e2e_ml2` checkpoints every query and resumes completed model calls in an existing
output file. For a BEFORE/AFTER comparison use a new output filename. It records
actual model inputs, generated claims and verifier outputs, including rejected
responses; it never records API credentials. The context is explicitly a neutral
evaluation context with no company profile facts. This is not a test of real
company applicability or a trusted context assembled by Go.

Read each claim against `ml2_evidence` and the predeclared `expected` spans before
setting `ml2_grounded` / `final_acceptable` in a reviewed copy. Successful schema
and semantic-verifier validation alone do not establish final answer quality.
No-answer requires an explicit insufficient-evidence response, not a timeout.

`component_request_seconds` in a replay is a sum of separately measured stages,
not a real HTTP wall-clock time. The initial ML1 diagnostic run performs an extra
vector search to capture Top-20. Its own wall time includes that instrumentation.
HTTP smoke times are measured directly. Cold load/inference and local hardware
can affect latency; these runs are not performance tuning or an SLA benchmark.

The HTTP smoke uses FastAPI TestClient with the real worker, models, LLM gateway,
production migrations and durable stores. It checks repeat, rejected quality,
critical HTTP errors, reconstructed API state and reranker outage fallback. It
does not invoke a running Go backend or implement missing analysis/insight routes.

Existing rollback, partial write and stale-version cases remain in the production
test suites; do not infer those guarantees from the happy-path smoke alone.
