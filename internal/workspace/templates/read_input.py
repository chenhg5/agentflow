import json
import os
import sys
from pathlib import Path

json.load(sys.stdin)
print(json.dumps(json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())))
