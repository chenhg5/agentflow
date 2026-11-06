import json, os
from pathlib import Path
request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
assert request["recipient"] and request["content"] and isinstance(request["actions"], list)
responses = json.loads(os.environ.get("BENCH_RESPONSES", '[{"signal":"happy","note":"local test"}]'))
counter = Path(os.environ.get("BENCH_CHANNEL_COUNTER", "channel-count.txt"))
index = int(counter.read_text()) if counter.exists() else 0
counter.write_text(str(index + 1))
result = responses[min(index, len(responses)-1)]
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps(result))
