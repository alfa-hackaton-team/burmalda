#!/bin/bash
set -eu

psql --set=ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set=runtime_password="$ML_RUNTIME_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE ml_runtime LOGIN PASSWORD %L', :'runtime_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ml_runtime')
\gexec
SQL
