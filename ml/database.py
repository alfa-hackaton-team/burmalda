"""ML-only database configuration. Never falls back to the Go DATABASE_URL."""
import os

import psycopg

ML_SCHEMA = 'ml'


def connect_runtime():
    """Caller owns/ closes this connection; migrations use a separate DSN/role."""
    dsn = os.environ.get('ML_DATABASE_URL')
    if not dsn:
        raise ValueError('ML_DATABASE_URL is required')
    return psycopg.connect(dsn, autocommit=True)
