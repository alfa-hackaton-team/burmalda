"""Run backend-only follow-up E2E in Docker; no gateway or real model calls.

Requires the existing local PostgreSQL container, ML dependency image and cached
Go dependencies. Creates/drops isolated test databases and a temporary test-only
Python HTTP bridge. Does not restart services or touch saved demo insights.
"""
import argparse
import json
from pathlib import Path
import secrets
import subprocess
import sys
from urllib.parse import quote


def run(args):
    result = subprocess.run(args, capture_output=True)
    if result.returncode:
        # Never print argv: it can contain the local PostgreSQL password.
        print(result.stdout.decode(), end='')
        print(result.stderr.decode(), file=sys.stderr, end='')
        raise RuntimeError('E2E command failed (arguments suppressed)')
    return result.stdout.decode()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--db-container', default='burmalda-db-1')
    parser.add_argument('--ml-image', default='burmalda-ml-worker')
    parser.add_argument('--go-image', default='golang:1.27.3-alpine')
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    inspected = json.loads(run(['docker', 'inspect', args.db_container]))[0]
    env = dict(value.split('=', 1) for value in inspected['Config']['Env'] if '=' in value)
    user = env.get('POSTGRES_USER', 'postgres')
    dsn = ('postgresql://' + quote(user, safe='') + ':' +
           quote(env['POSTGRES_PASSWORD'], safe='') + '@127.0.0.1:5432/postgres?sslmode=disable')
    name = 'burmalda-follow-up-e2e-' + secrets.token_hex(5)
    port = str(20000 + secrets.randbelow(20000))
    token = secrets.token_urlsafe(32)
    network = 'container:' + args.db_container
    started = False
    try:
        run(['docker', 'run', '-d', '--rm', '--name', name, '--network', network,
             '-v', str(repo) + ':/app:ro', '-w', '/app',
             '-e', 'FOLLOW_UP_E2E_TOKEN=' + token, args.ml_image,
             'python', '-m', 'ml_runtime.tests.follow_up_e2e_bridge', '--port', port])
        started = True
        ready = '''import os,time,urllib.request
for attempt in range(50):
 try:
  r=urllib.request.Request('http://127.0.0.1:PORT/test/stats',headers={'Authorization':'Bearer '+os.environ['FOLLOW_UP_E2E_TOKEN']})
  with urllib.request.urlopen(r,timeout=1) as response: assert response.status==200
  break
 except Exception:
  if attempt==49: raise
  time.sleep(.1)
'''.replace('PORT', port)
        run(['docker', 'exec', name, 'python', '-c', ready])
        print(run(['docker', 'run', '--rm', '--network', network,
                   '-v', str(repo) + ':/src:ro', '-v', 'burmalda-go-mod:/go/pkg/mod',
                   '-v', 'burmalda-go-build:/root/.cache/go-build', '-w', '/src/backend',
                   '-e', 'TEST_DATABASE_URL=' + dsn,
                   '-e', 'FOLLOW_UP_E2E_ML_URL=http://127.0.0.1:' + port,
                   '-e', 'FOLLOW_UP_E2E_TOKEN=' + token,
                   '-e', 'GOPROXY=off', '-e', 'GOTOOLCHAIN=local', args.go_image,
                   'go', 'test', '-count=1', '-failfast', '-v', '-run',
                   '^TestFollowUpSnapshotBackendE2E$', './internal/userapi']), end='')
    finally:
        if started:
            run(['docker', 'stop', '--time', '2', name])


if __name__ == '__main__':
    main()
