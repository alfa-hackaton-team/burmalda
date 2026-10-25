from pathlib import Path
import argparse
import json
import yaml

root = Path(__file__).resolve().parents[3]
parser = argparse.ArgumentParser()
parser.add_argument('--check', action='store_true')
args = parser.parse_args()
public = yaml.safe_load((root / 'docs/api/schemas.yaml').read_text())
public['$schema'] = 'https://json-schema.org/draft/2020-12/schema'
files = {
    'ml.json': (root / 'docs/ml/contracts/v1/contracts.schema.json').read_text(),
    'public.json': json.dumps(public, ensure_ascii=False, indent=2) + '\n',
}
for name, content in files.items():
    path = Path(__file__).parent / name
    if args.check:
        if not path.exists() or path.read_text() != content:
            raise SystemExit(f'{path}: run python backend/internal/apicontract/generate.py')
    else:
        path.write_text(content)
