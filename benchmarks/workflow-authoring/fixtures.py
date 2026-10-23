"""Public local services supplied to agents; no reference workflow lives here."""

CHANNEL = r"""import json, os
from pathlib import Path
request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
assert request["recipient"] and request["content"] and isinstance(request["actions"], list)
responses = json.loads(os.environ.get("BENCH_RESPONSES", '[{"signal":"happy","note":"local test"}]'))
counter = Path(os.environ.get("BENCH_CHANNEL_COUNTER", "channel-count.txt"))
index = int(counter.read_text()) if counter.exists() else 0
counter.write_text(str(index + 1))
result = responses[min(index, len(responses)-1)]
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps(result))
"""
MEASURE = r"""import json, os
from pathlib import Path
request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
assert isinstance(request["sample"], str)
result = json.loads(os.environ.get("BENCH_MEASUREMENT", '{"score":81,"defects":[],"urgent":false}'))
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps(result))
"""
FINISH = r"""import json, os
from pathlib import Path
request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
assert os.environ.get("QUALITY_SITE") == "plant-7"
assert isinstance(request["score"], (int,float)) and isinstance(request["defects"], list) and type(request["urgent"]) is bool
assert request["branch"] in ["accept", "recheck"]
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps(request))
"""
