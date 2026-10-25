#!/bin/bash
set -eu

psql --set=ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set=runtime_password="$ML_RUNTIME_PASSWORD" \
  --set=backend_password="$BACKEND_RUNTIME_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE burmalda LOGIN PASSWORD %L', :'backend_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'burmalda')
\gexec

SELECT format('CREATE ROLE ml_runtime LOGIN PASSWORD %L', :'runtime_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ml_runtime')
\gexec

GRANT CONNECT, CREATE ON DATABASE burmalda TO burmalda;
GRANT USAGE, CREATE ON SCHEMA public TO burmalda;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO burmalda;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO burmalda;
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
  GRANT ALL PRIVILEGES ON TABLES TO burmalda;
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
  GRANT ALL PRIVILEGES ON SEQUENCES TO burmalda;
SQL
