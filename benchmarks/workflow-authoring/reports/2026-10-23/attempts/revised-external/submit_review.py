"""Independent local terminal submitter; no notification service or model calls."""
import json
import subprocess
import sys
from pathlib import Path

if len(sys.argv) != 3:
    raise SystemExit('Usage: python3 submit_review.py RUN_ID REVIEW_JSON_FILE')
root = Path(__file__).resolve().parent
payload = json.loads(Path(sys.argv[2]).read_text())
# The engine enforces the review schema. Pass the supplied JSON unchanged.
completed = subprocess.run(
    [str(root / 'agentflow'), 'run', 'submit', sys.argv[1], 'review',
     '--output', json.dumps(payload, ensure_ascii=False), '--format', 'json'],
    cwd=root,
)
raise SystemExit(completed.returncode)
