"""Legacy entry point: this placeholder is not a real code reviewer."""
import json
from pathlib import Path
import sys

data = json.loads(Path(sys.argv[1]).read_text())
data['findings'] = []
Path('report.json').write_text(json.dumps(data))
