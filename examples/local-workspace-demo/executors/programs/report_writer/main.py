#!/usr/bin/env python3
"""An ordinary program executor; the graph only observes JSON in and out."""

import json
import os
from pathlib import Path


payload = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
draft = payload["draft"]
review = payload["review"]
output = Path(os.environ["AGENTFLOW_ROOT"]) / "output"
output.mkdir(exist_ok=True)
report = output / "report.md"
report.write_text(
    "# Field note summary\n\n"
    + draft["summary"]
    + "\n\n## Verification\n\n"
    + review["note"]
    + "\n\nAccepted: "
    + str(review["accepted"]).lower()
    + "\n",
    encoding="utf-8",
)
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(
    json.dumps({"report_path": "output/report.md", "accepted": review["accepted"]}),
    encoding="utf-8",
)
