"""Count returns to drafting using persisted input from the last review visit."""
import json
import os
from pathlib import Path

request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
previous = request["previous_reworks"]
reworks = 0 if previous is None else previous + 1
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps({"reworks": reworks}))
