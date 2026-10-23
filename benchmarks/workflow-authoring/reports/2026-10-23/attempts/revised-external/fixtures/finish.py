import json, os
from pathlib import Path
request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
assert os.environ.get("QUALITY_SITE") == "plant-7"
assert isinstance(request["score"], (int,float)) and isinstance(request["defects"], list) and type(request["urgent"]) is bool
assert request["branch"] in ["accept", "recheck"]
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps(request))
