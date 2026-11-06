import json
import os
from pathlib import Path

request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps(request), encoding="utf-8")
