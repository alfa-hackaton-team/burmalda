FROM python:3.12.15-slim-bookworm

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HF_HOME=/models/hf \
    ML_MODEL_CACHE=/models/hf \
    HF_HUB_OFFLINE=1

WORKDIR /app

# ML1 pins torch/transformers/tokenizers; ML2 pins the API, LLM client and DB driver.
COPY ml/requirements-embeddings.txt ml/requirements-persistence.txt /app/ml/
COPY ml/requirements-chunking.txt ml/requirements.txt /app/ml/
COPY ml_runtime/requirements.txt /app/ml_runtime/
RUN python -m pip install --no-cache-dir --upgrade pip \
    && python -m pip install --no-cache-dir \
       -r /app/ml/requirements-embeddings.txt \
       -r /app/ml/requirements-persistence.txt \
       -r /app/ml_runtime/requirements.txt

COPY . /app

RUN groupadd --system --gid 10001 burmalda \
    && useradd --system --uid 10001 --gid burmalda --home-dir /home/burmalda burmalda \
    && mkdir -p /models/hf \
    && chown -R burmalda:burmalda /app /models /home/burmalda

USER 10001:10001
EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=3s --start-period=30m --retries=3 \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/healthz', timeout=2)" || exit 1

CMD ["python", "-m", "ml_runtime.serve_article_processing"]
