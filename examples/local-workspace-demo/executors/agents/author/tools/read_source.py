#!/usr/bin/env python3
"""Local tool exposed through the demo's generic MCP adapter."""

import hashlib
import json
import os
import sys
from pathlib import Path


json.load(sys.stdin)
source = Path(os.environ["AGENTFLOW_ROOT"]) / "data" / "source.md"
raw = source.read_bytes()
text = raw.decode("utf-8")
print(json.dumps({
    "text": text,
    "sha256": hashlib.sha256(raw).hexdigest(),
    "word_count": len(text.split()),
}, ensure_ascii=False))
