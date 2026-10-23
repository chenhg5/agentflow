"""Submit a JSON file to a persisted review from an independent terminal."""
import argparse
import json
import subprocess
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('run_id')
parser.add_argument('json_file', type=Path)
args = parser.parse_args()
output = json.loads(args.json_file.read_text())
root = Path(__file__).resolve().parent
result = subprocess.run([
    str(root / 'agentflow'), 'run', 'submit', args.run_id, 'review',
    '--output', json.dumps(output, ensure_ascii=False), '--format', 'json',
], cwd=root)
raise SystemExit(result.returncode)
