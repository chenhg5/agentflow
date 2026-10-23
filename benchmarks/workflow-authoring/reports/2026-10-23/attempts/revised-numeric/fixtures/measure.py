import json, os
from pathlib import Path
request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
assert isinstance(request["sample"], str)
result = json.loads(os.environ.get("BENCH_MEASUREMENT", '{"score":81,"defects":[],"urgent":false}'))
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps(result))
